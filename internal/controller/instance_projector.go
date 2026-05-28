// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// InstanceProjector watches Instance objects written back to the downstream
// control plane by POP-cell InstanceReconcilers and creates read-only
// projections in the corresponding project namespace within each project cluster.
//
// Namespace resolution: a downstream Instance lives in namespace
// `ns-<project-namespace-uid>`. The UID portion is matched against the UID of
// namespaces in the project cluster to find the target namespace.
//
// Ownership: each projected Instance is owned by the project WorkloadDeployment
// so that it is garbage-collected via cascading deletion when the deployment is
// removed from the project cluster.
//
// The controller is registered with a standard manager.Manager pointed at the
// downstream control plane — NOT the multicluster-runtime manager — so informer
// watches are scoped to the downstream control plane.
type InstanceProjector struct {
	// DownstreamClient reads Instance objects from the downstream control plane.
	// Must be set before SetupWithManager is called.
	DownstreamClient client.Client

	// MCManager provides access to project cluster clients via GetCluster.
	MCManager mcmanager.Manager
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/status,verbs=get;update;patch

func (r *InstanceProjector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("instance", req.NamespacedName)

	// 1. Fetch the Instance from the downstream control plane.
	var downstreamInstance computev1alpha.Instance
	if err := r.DownstreamClient.Get(ctx, req.NamespacedName, &downstreamInstance); err != nil {
		if apierrors.IsNotFound(err) {
			// Instance was deleted from the downstream control plane. Projections
			// are owned by the project WorkloadDeployment, so cascading deletion
			// handles cleanup.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed getting downstream instance: %w", err)
	}

	// Only project Instances that carry the upstream tracking label; others were
	// not written by our InstanceReconciler write-back logic.
	encodedClusterName, ok := downstreamInstance.Labels[downstreamclient.UpstreamOwnerClusterNameLabel]
	if !ok {
		logger.V(1).Info("skipping instance without upstream cluster label")
		return ctrl.Result{}, nil
	}

	// 2. Resolve the project cluster name.
	// The encoded form is "cluster-<name>" with "/" replaced by "_".
	clusterName := strings.TrimPrefix(encodedClusterName, "cluster-")
	clusterName = strings.ReplaceAll(clusterName, "_", "/")

	// 3. Obtain the project cluster client.
	projectCluster, err := r.MCManager.GetCluster(ctx, multicluster.ClusterName(clusterName))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed getting project cluster %q: %w", clusterName, err)
	}
	projectClient := projectCluster.GetClient()

	// 4. Resolve the target project namespace from the Instance label.
	// The InstanceReconciler stamps UpstreamOwnerNamespaceLabel with the project
	// namespace name (read from the downstream namespace label set by the federator),
	// so we can resolve the target namespace directly without scanning.
	targetNamespace := downstreamInstance.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	if targetNamespace == "" {
		logger.Info("Instance missing upstream-namespace label, requeueing",
			"namespace", downstreamInstance.Namespace, "name", downstreamInstance.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// 5. Find the owning WorkloadDeployment in the project cluster by UID.
	// The downstream Instance carries WorkloadDeploymentUIDLabel so we can find
	// the owning deployment without relying on field selectors.
	wdUID := downstreamInstance.Labels[computev1alpha.WorkloadDeploymentUIDLabel]

	var wdList computev1alpha.WorkloadDeploymentList
	if err := projectClient.List(ctx, &wdList, client.InNamespace(targetNamespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed listing WorkloadDeployments in %s/%s: %w", clusterName, targetNamespace, err)
	}

	var ownerWD *computev1alpha.WorkloadDeployment
	for i := range wdList.Items {
		if string(wdList.Items[i].UID) == wdUID {
			ownerWD = &wdList.Items[i]
			break
		}
	}

	// 6. Create or update the projection in the project namespace.
	projection := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      downstreamInstance.Name,
			Namespace: targetNamespace,
		},
	}

	operationResult, err := controllerutil.CreateOrUpdate(ctx, projectClient, projection, func() error {
		// Propagate upstream tracking labels so consumers can filter by origin.
		if projection.Labels == nil {
			projection.Labels = make(map[string]string)
		}
		for k, v := range downstreamInstance.Labels {
			projection.Labels[k] = v
		}

		projection.Spec = downstreamInstance.Spec

		// Attach an owner reference to the WorkloadDeployment so the projection
		// is garbage-collected when the deployment is removed.
		if ownerWD != nil {
			return controllerutil.SetOwnerReference(ownerWD, projection, projectCluster.GetScheme())
		}
		return nil
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed upserting Instance projection in %s/%s: %w", clusterName, targetNamespace, err)
	}

	logger.Info("reconciled Instance projection", "operation", operationResult, "namespace", targetNamespace, "cluster", clusterName)

	// 7. Sync status — status is a separate subresource.
	projection.Status = downstreamInstance.Status
	if err := projectClient.Status().Update(ctx, projection); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("failed updating Instance projection status: %w", err)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers the InstanceProjector with downstreamMgr, a standard
// manager.Manager configured against the downstream control plane REST config.
// DownstreamClient and MCManager must be set before calling this method.
func (r *InstanceProjector) SetupWithManager(downstreamMgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(downstreamMgr).
		For(&computev1alpha.Instance{}).
		Named("instance-projector").
		Complete(r)
}
