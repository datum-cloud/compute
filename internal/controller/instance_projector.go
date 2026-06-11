// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"

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

// InstanceProjector watches Instance objects written back to the upstream
// Karmada/management control plane by POP-cell InstanceReconcilers and creates
// read-only projections in the corresponding project namespace within each
// project cluster.
//
// Namespace resolution: an upstream Instance lives in namespace
// `ns-<project-namespace-uid>`. The UID portion is matched against the UID of
// namespaces in the project cluster to find the target namespace.
//
// Ownership: each projected Instance is owned by the project WorkloadDeployment
// so that it is garbage-collected via cascading deletion when the deployment is
// removed from the project cluster.
//
// The controller is registered with a standard manager.Manager pointed at the
// upstream Karmada control plane — NOT the multicluster-runtime manager — so
// informer watches are scoped to the upstream control plane.
type InstanceProjector struct {
	// FederationClient reads Instance objects from the Karmada federation control
	// plane (configured via --federation-kubeconfig). Must be set before
	// SetupWithManager is called.
	FederationClient client.Client

	// MCManager provides access to project cluster clients via GetCluster.
	MCManager mcmanager.Manager
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/status,verbs=get;update;patch

func (r *InstanceProjector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("instance", req.NamespacedName)

	var downstreamInstance computev1alpha.Instance
	if err := r.FederationClient.Get(ctx, req.NamespacedName, &downstreamInstance); err != nil {
		if apierrors.IsNotFound(err) {
			// Instance was deleted from the upstream control plane. Projections
			// are owned by the project WorkloadDeployment, so cascading deletion
			// handles cleanup.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed getting upstream instance: %w", err)
	}

	// Federation-plane Instances exist exclusively as write-back copies, and
	// the InstanceReconciler stamps both upstream-owner labels atomically when
	// it writes the copy — "not ours" cannot occur. A missing cluster label is
	// a stamping-invariant violation that never self-heals, so surface it as an
	// error for backoff and visibility rather than silently dropping the
	// projection.
	encodedClusterName := downstreamInstance.Labels[downstreamclient.UpstreamOwnerClusterNameLabel]
	if encodedClusterName == "" {
		return ctrl.Result{}, fmt.Errorf("downstream instance %s/%s is missing the %s label; cannot resolve the project cluster",
			downstreamInstance.Namespace, downstreamInstance.Name,
			downstreamclient.UpstreamOwnerClusterNameLabel)
	}

	// The encoded form is "cluster-<name>" with "/" replaced by "_".
	clusterName := DecodeClusterName(encodedClusterName)

	projectCluster, err := r.MCManager.GetCluster(ctx, multicluster.ClusterName(clusterName))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed getting project cluster %q: %w", clusterName, err)
	}
	projectClient := projectCluster.GetClient()

	// The InstanceReconciler stamps UpstreamOwnerNamespaceLabel with the project
	// namespace name (read from the upstream Karmada namespace label set by the federator),
	// so we can resolve the target namespace directly without scanning. Both
	// upstream-owner labels are stamped together with non-empty values, so a
	// cluster label without a namespace label is an invariant violation that
	// never self-heals — surface it as an error for backoff and visibility
	// rather than requeueing at a flat rate.
	targetNamespace := downstreamInstance.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	if targetNamespace == "" {
		return ctrl.Result{}, fmt.Errorf("downstream instance %s/%s carries %s but is missing the %s label; cannot resolve the project namespace",
			downstreamInstance.Namespace, downstreamInstance.Name,
			downstreamclient.UpstreamOwnerClusterNameLabel, downstreamclient.UpstreamOwnerNamespaceLabel)
	}

	// Resolve the owning WorkloadDeployment by NAME in the project cluster.
	// Core invariant: the ownerReference MUST be built from a project-cluster
	// object obtained via projectClient.Get — never from any edge/Karmada
	// identity. The WD name is stable across all planes (project cluster,
	// Karmada, edge) and is the correct cross-plane identifier, carried by
	// WorkloadDeploymentNameLabel (stamped by the edge stateful control
	// strategy).
	wdName := downstreamInstance.Labels[computev1alpha.WorkloadDeploymentNameLabel]
	if wdName == "" {
		// A write-back copy that cannot identify its WorkloadDeployment violates
		// the same stamping invariant as the labels above — surface it as an
		// error for backoff and visibility instead of silently dropping the
		// projection.
		return ctrl.Result{}, fmt.Errorf("downstream instance %s/%s is missing the %s label; cannot resolve its WorkloadDeployment",
			downstreamInstance.Namespace, downstreamInstance.Name, computev1alpha.WorkloadDeploymentNameLabel)
	}

	// Fetch the project-cluster WD directly by name. The returned object carries
	// the project-cluster metadata.uid — the only UID that GC in the project
	// cluster can act on.
	var ownerWD computev1alpha.WorkloadDeployment
	if err := projectClient.Get(ctx, client.ObjectKey{Namespace: targetNamespace, Name: wdName}, &ownerWD); err != nil {
		if apierrors.IsNotFound(err) {
			// Never create an ownerless projection. The controller only watches
			// Instances, so no event fires when the WD appears — returning an
			// error retries with backoff and surfaces the wait in error metrics.
			// A transient ordering race (Instance projected before
			// WorkloadReconciler created the project WD) resolves on retry; a
			// deleted WD ends the retries once its write-back copies are gone.
			return ctrl.Result{}, fmt.Errorf("workload deployment %q not found in project cluster %q for instance %s/%s",
				wdName, clusterName, downstreamInstance.Namespace, downstreamInstance.Name)
		}
		return ctrl.Result{}, fmt.Errorf("failed getting WorkloadDeployment %s/%s in project cluster %s: %w",
			targetNamespace, wdName, clusterName, err)
	}

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

		// Attach an owner reference using the live project-cluster WD object.
		// controllerutil.SetOwnerReference reads UID and GVK from ownerWD, which
		// was fetched from projectClient — satisfying the core invariant.
		return controllerutil.SetOwnerReference(&ownerWD, projection, projectCluster.GetScheme())
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

// SetupWithManager registers the InstanceProjector with upstreamMgr, a standard
// manager.Manager configured against the upstream Karmada/federation control plane
// REST config. FederationClient and MCManager must be set before calling this method.
func (r *InstanceProjector) SetupWithManager(upstreamMgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(upstreamMgr).
		For(&computev1alpha.Instance{}).
		Named("instance-projector").
		Complete(r)
}
