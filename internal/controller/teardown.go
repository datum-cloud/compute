// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"

	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	"go.miloapis.com/milo/pkg/downstreamclient"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// labelServiceNameValue is this operator's canonical service name, the value
// paired with labelServiceName on every resource the compute operator scopes
// suspend/resume/teardown by (WorkloadDeployment, Instance).
const labelServiceNameValue = "compute.datumapis.com"

// ComputeTeardown implements consumer.Teardown. It is invoked after the
// consumer provider has cancelled the per-cluster context and marked labeled
// Instances for deletion via ManagedResources. Its job is to finish the work the
// per-object finalizers would have done, now that the controllers that own those
// finalizers will no longer reconcile this project.
//
// Teardown must never release a finalizer it has not first honoured. A finalizer
// removed without its work done is indistinguishable, from the outside, from
// work that succeeded — and on the federation plane it strands the hub objects
// the finalizer existed to remove (datum-cloud/compute#218). So each finalizer
// released here is released by the same code path that just completed its work,
// and any failure aborts teardown so the provider retries it.
//
// WorkloadDeployments are deliberately untouched. Their hub copy is removed by
// the federator's finalizer along the ordinary deletion path, which the project
// control plane guarantees runs — project deletion waits for the project's
// resources to be deleted first. Short-circuiting that finalizer here would
// reintroduce exactly the bypass this type is documented not to take.
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

	logger.V(1).Info("consumer teardown complete")
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
			if err := ct.deleteWriteBackInstance(ctx, consumerProject, cl, instance); err != nil {
				return err
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

// deleteWriteBackInstance removes the hub write-back copy of a project Instance
// — the work the instance-controller finalizer does — before that finalizer is
// released.
//
// The copy does not live in the project namespace: it lives in the hub namespace
// that the project namespace maps to. Resolving that mapping is what makes the
// delete target the object that actually exists; keying it by the project
// namespace silently finds nothing and leaves the copy behind.
func (ct *ComputeTeardown) deleteWriteBackInstance(
	ctx context.Context,
	consumerProject string,
	cl client.Client,
	instance *computev1alpha.Instance,
) error {
	strategy := downstreamclient.NewMappedNamespaceResourceStrategy(consumerProject, cl, ct.federationClient)
	hubNamespace, err := strategy.GetDownstreamNamespaceNameForUpstreamNamespace(ctx, instance.Namespace)
	if err != nil {
		return fmt.Errorf("resolving hub namespace for instance %s/%s: %w",
			instance.Namespace, instance.Name, err)
	}

	writeBack := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{Namespace: hubNamespace, Name: instance.Name},
	}
	if err := ct.federationClient.Delete(ctx, writeBack); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting write-back instance %s/%s: %w", hubNamespace, instance.Name, err)
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
