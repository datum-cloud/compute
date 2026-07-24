// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"

	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	quotametrics "go.datum.net/compute/internal/quota"
)

// labelServiceName is the label key the consumer provider uses to scope
// deactivation cleanup. Every Instance the compute operator creates in a
// consumer project carries this label so disengage can target them by service.
const labelServiceName = "services.miloapis.com/service-name"

// ComputeTeardown implements consumer.Teardown. It is invoked after the
// consumer provider has cancelled the per-cluster context and marked labeled
// Instances for deletion via ManagedResources. It handles the parts a
// label-scoped delete cannot: releasing ResourceClaims, removing write-back
// Instances from the federation hub, and stripping finalizers so Kubernetes
// can complete the deletions.
type ComputeTeardown struct {
	// nil when quota enforcement is disabled
	quotaClientManager *quotametrics.ProjectQuotaClientManager
	// nil when federation is disabled
	federationClient client.Client
	scheme           *runtime.Scheme
}

// NewComputeTeardown creates a ComputeTeardown. quotaClientManager and
// federationClient are optional; pass nil to skip those cleanup steps.
func NewComputeTeardown(
	quotaClientManager *quotametrics.ProjectQuotaClientManager,
	federationClient client.Client,
	scheme *runtime.Scheme,
) *ComputeTeardown {
	return &ComputeTeardown{
		quotaClientManager: quotaClientManager,
		federationClient:   federationClient,
		scheme:             scheme,
	}
}

// TeardownConsumer implements consumer.Teardown.
func (ct *ComputeTeardown) TeardownConsumer(
	ctx context.Context,
	consumerProject string,
	cl client.Client,
	serviceNames []string,
) error {
	logger := log.FromContext(ctx).WithValues("consumerProject", consumerProject)

	for _, serviceName := range serviceNames {
		var instances computev1alpha.InstanceList
		if err := cl.List(ctx, &instances, client.MatchingLabels{labelServiceName: serviceName}); err != nil {
			return fmt.Errorf("listing instances for service %q: %w", serviceName, err)
		}
		for i := range instances.Items {
			if err := ct.teardownInstance(ctx, consumerProject, cl, &instances.Items[i]); err != nil {
				return err
			}
		}
	}

	// WDs are not labeled (projected by Karmada, not by the compute operator),
	// so list all of them. Stripping the compute-owned finalizer unblocks GC
	// once Karmada un-propagates them.
	var wds computev1alpha.WorkloadDeploymentList
	if err := cl.List(ctx, &wds); err != nil {
		return fmt.Errorf("listing workload deployments: %w", err)
	}
	for i := range wds.Items {
		wd := &wds.Items[i]
		base := wd.DeepCopy()
		if !controllerutil.RemoveFinalizer(wd, workloadControllerFinalizer) {
			continue
		}
		if err := cl.Patch(ctx, wd, client.MergeFrom(base)); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("stripping WD finalizer for %s/%s: %w", wd.Namespace, wd.Name, err)
		}
		logger.V(1).Info("stripped WD finalizer", "name", wd.Name, "namespace", wd.Namespace)
	}

	return nil
}

func (ct *ComputeTeardown) teardownInstance(
	ctx context.Context,
	consumerProject string,
	cl client.Client,
	instance *computev1alpha.Instance,
) error {
	if controllerutil.ContainsFinalizer(instance, instanceControllerFinalizer) {
		if ct.federationClient != nil {
			downstream := &computev1alpha.Instance{}
			err := ct.federationClient.Get(ctx, client.ObjectKeyFromObject(instance), downstream)
			if err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("getting write-back instance %s/%s: %w",
					instance.Namespace, instance.Name, err)
			}
			if err == nil {
				if err := ct.federationClient.Delete(ctx, downstream); client.IgnoreNotFound(err) != nil {
					return fmt.Errorf("deleting write-back instance %s/%s: %w",
						instance.Namespace, instance.Name, err)
				}
			}
		}
	}

	if controllerutil.ContainsFinalizer(instance, instanceQuotaFinalizer) {
		if ct.quotaClientManager != nil {
			if err := ct.releaseQuotaClaim(ctx, consumerProject, instance); err != nil {
				if !errors.Is(err, errProjectIdentityUnresolvable) {
					return fmt.Errorf("releasing quota claim for %s/%s: %w",
						instance.Namespace, instance.Name, err)
				}
				// Mirrors reconcileDeletion: unresolvable identity must not wedge
				// deletion. Cannot happen in consumer-provider mode where
				// projectID == consumerProject, but kept for defensive parity.
				log.FromContext(ctx).Error(err,
					"project identity unresolvable during teardown; ResourceClaim may be orphaned",
					"instance", instance.Name, "namespace", instance.Namespace)
			}
		}
	}

	base := instance.DeepCopy()
	c1 := controllerutil.RemoveFinalizer(instance, instanceControllerFinalizer)
	c2 := controllerutil.RemoveFinalizer(instance, instanceQuotaFinalizer)
	if !c1 && !c2 {
		return nil
	}
	if err := cl.Patch(ctx, instance, client.MergeFrom(base)); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("stripping finalizers for %s/%s: %w",
			instance.Namespace, instance.Name, err)
	}
	return nil
}

func (ct *ComputeTeardown) releaseQuotaClaim(
	ctx context.Context,
	projectID string,
	instance *computev1alpha.Instance,
) error {
	projectClient, err := ct.quotaClientManager.ClientForProject(ctx, projectID, ct.scheme)
	if err != nil {
		return fmt.Errorf("getting quota client for project %q: %w", projectID, err)
	}

	claimName := quotaClaimName(instance)
	var claim quotav1alpha1.ResourceClaim
	if err := projectClient.Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: claimName}, &claim); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("getting resource claim %s/%s: %w", instance.Namespace, claimName, err)
	}

	if err := projectClient.Delete(ctx, &claim); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting resource claim %s/%s: %w", instance.Namespace, claimName, err)
	}
	return nil
}
