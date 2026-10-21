// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"
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
// so that it is garbage-collected when the deployment is removed. The projector
// finalizes write-back Instances by removing their project projections before
// the write-backs disappear, including during scale-down.
//
// The controller is registered on the leader-elected local manager so only the
// elected replica writes projections, and watches Instances through the
// federation cluster's cache, which runs on every replica.
type InstanceProjector struct {
	// FederationClient reads Instance objects from the Karmada federation control
	// plane (configured via --federation-kubeconfig). Must be set before
	// SetupWithManager is called.
	FederationClient client.Client

	// MCManager provides access to project cluster clients via GetCluster.
	MCManager mcmanager.Manager
}

const instanceProjectionFinalizer = "compute.datumapis.com/instance-projection"

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/finalizers,verbs=update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/status,verbs=get;update;patch

func (r *InstanceProjector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("instance", req.NamespacedName)

	var downstreamInstance computev1alpha.Instance
	if err := r.FederationClient.Get(ctx, req.NamespacedName, &downstreamInstance); err != nil {
		if apierrors.IsNotFound(err) {
			// The Instance is gone, so recover its project route from the hub
			// namespace, which the federator labels before creating write-backs.
			var namespace corev1.Namespace
			if err := r.FederationClient.Get(ctx, client.ObjectKey{Name: req.Namespace}, &namespace); err != nil {
				if apierrors.IsNotFound(err) {
					// Project teardown also removes the hub namespace; the project
					// deployment's owner reference handles its projections.
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, fmt.Errorf("failed getting federation namespace %q for deleted instance: %w", req.Namespace, err)
			}
			return ctrl.Result{}, r.deleteProjection(ctx, req.NamespacedName, namespace.Labels)
		}
		return ctrl.Result{}, fmt.Errorf("failed getting upstream instance: %w", err)
	}

	// Keep the write-back until its project projection has been deleted.
	if !downstreamInstance.DeletionTimestamp.IsZero() {
		if err := r.deleteProjection(ctx, req.NamespacedName, downstreamInstance.Labels); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.ContainsFinalizer(&downstreamInstance, instanceProjectionFinalizer) {
			base := downstreamInstance.DeepCopy()
			controllerutil.RemoveFinalizer(&downstreamInstance, instanceProjectionFinalizer)
			if err := r.FederationClient.Patch(ctx, &downstreamInstance, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("failed removing projection finalizer from write-back instance %s: %w", req.NamespacedName, err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Federation-plane Instances exist exclusively as write-back copies, and the
	// InstanceReconciler stamps both upstream-owner labels atomically when it
	// writes the copy — "not ours" cannot occur. A missing cluster label is a
	// stamping-invariant violation, so surface it as an error for backoff and
	// visibility rather than silently dropping the projection.
	encodedClusterName := downstreamInstance.Labels[downstreamclient.UpstreamOwnerClusterNameLabel]
	if encodedClusterName == "" {
		return ctrl.Result{}, fmt.Errorf("downstream instance %s/%s is missing the %s label; cannot resolve the project cluster",
			downstreamInstance.Namespace, downstreamInstance.Name,
			downstreamclient.UpstreamOwnerClusterNameLabel)
	}

	// Derive the project cluster key the same way the federator does. The label
	// encodes the full "org/project" path, while the multicluster provider keys
	// clusters by bare project name.
	clusterName := projectClusterNameFromLabel(encodedClusterName)
	if clusterName == "" {
		return ctrl.Result{}, fmt.Errorf("downstream instance %s/%s carries an undecodable %s label (%q)",
			downstreamInstance.Namespace, downstreamInstance.Name,
			downstreamclient.UpstreamOwnerClusterNameLabel, encodedClusterName)
	}

	// Both upstream-owner labels are stamped together with non-empty values, so a
	// cluster label without a namespace label is the same invariant violation.
	targetNamespace := downstreamInstance.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	if targetNamespace == "" {
		return ctrl.Result{}, fmt.Errorf("downstream instance %s/%s carries %s but is missing the %s label; cannot resolve the project namespace",
			downstreamInstance.Namespace, downstreamInstance.Name,
			downstreamclient.UpstreamOwnerClusterNameLabel, downstreamclient.UpstreamOwnerNamespaceLabel)
	}

	// Resolve the owning WorkloadDeployment by NAME in the project cluster. Core
	// invariant: build the ownerReference from a project-cluster object fetched
	// with projectClient.Get, never from an edge or Karmada identity. The WD name
	// is stable across all planes and is carried by WorkloadDeploymentNameLabel,
	// stamped by the edge stateful control strategy.
	wdName := downstreamInstance.Labels[computev1alpha.WorkloadDeploymentNameLabel]
	if wdName == "" {
		return ctrl.Result{}, fmt.Errorf("downstream instance %s/%s is missing the %s label; cannot resolve its WorkloadDeployment",
			downstreamInstance.Namespace, downstreamInstance.Name, computev1alpha.WorkloadDeploymentNameLabel)
	}

	projectCluster, err := r.MCManager.GetCluster(ctx, multicluster.ClusterName(clusterName))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed getting project cluster %q: %w", clusterName, err)
	}
	projectClient := projectCluster.GetClient()

	// Fetch the project-cluster WD directly by name. The returned object carries
	// the project-cluster metadata.uid — the only UID that GC in the project
	// cluster can act on.
	var ownerWD computev1alpha.WorkloadDeployment
	if err := projectClient.Get(ctx, client.ObjectKey{Namespace: targetNamespace, Name: wdName}, &ownerWD); err != nil {
		if apierrors.IsNotFound(err) {
			// Never create an ownerless projection. The controller only watches
			// Instances, so no event fires when the WD appears — returning an error
			// retries with backoff. A hub Instance cannot outlive its project
			// deployment, so this is a creation-ordering race that the retry
			// resolves.
			return ctrl.Result{}, fmt.Errorf("workload deployment %q not found in project cluster %q for instance %s/%s",
				wdName, clusterName, downstreamInstance.Namespace, downstreamInstance.Name)
		}
		return ctrl.Result{}, fmt.Errorf("failed getting WorkloadDeployment %s/%s in project cluster %s: %w",
			targetNamespace, wdName, clusterName, err)
	}
	// Adopt older write-backs that predate the finalizer before projecting them.
	// New write-backs already carry it from the cell reconciler.
	if !controllerutil.ContainsFinalizer(&downstreamInstance, instanceProjectionFinalizer) {
		base := downstreamInstance.DeepCopy()
		controllerutil.AddFinalizer(&downstreamInstance, instanceProjectionFinalizer)
		if err := r.FederationClient.Patch(ctx, &downstreamInstance, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed adding projection finalizer to write-back instance %s: %w", req.NamespacedName, err)
		}
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

	// Status is a separate subresource.
	projection.Status = downstreamInstance.Status
	if err := projectClient.Status().Update(ctx, projection); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("failed updating Instance projection status: %w", err)
	}

	return ctrl.Result{}, nil
}

// deleteProjection only removes the projected copy for the source route. The
// labels on the hub namespace allow this even after the source Instance is gone.
func (r *InstanceProjector) deleteProjection(ctx context.Context, key types.NamespacedName, route map[string]string) error {
	encodedClusterName := route[downstreamclient.UpstreamOwnerClusterNameLabel]
	targetNamespace := route[downstreamclient.UpstreamOwnerNamespaceLabel]
	clusterName := projectClusterNameFromLabel(encodedClusterName)
	if clusterName == "" || targetNamespace == "" {
		return fmt.Errorf("cannot resolve project route for deleted instance %s from cluster %q and namespace %q", key, encodedClusterName, targetNamespace)
	}

	projectCluster, err := r.MCManager.GetCluster(ctx, multicluster.ClusterName(clusterName))
	if err != nil {
		return fmt.Errorf("failed getting project cluster %q for deleted instance %s: %w", clusterName, key, err)
	}
	projectClient := projectCluster.GetClient()
	var projection computev1alpha.Instance
	projectionKey := client.ObjectKey{Namespace: targetNamespace, Name: key.Name}
	if err := projectClient.Get(ctx, projectionKey, &projection); err != nil {
		return client.IgnoreNotFound(err)
	}
	if projection.Labels[downstreamclient.UpstreamOwnerClusterNameLabel] != encodedClusterName ||
		projection.Labels[downstreamclient.UpstreamOwnerNamespaceLabel] != targetNamespace {
		return fmt.Errorf("refusing to delete instance %s in project cluster %q: projection route does not match federation instance %s", projectionKey, clusterName, key)
	}
	if err := projectClient.Delete(ctx, &projection); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed deleting instance projection %s in project cluster %q: %w", projectionKey, clusterName, err)
	}
	// A project-side finalizer may keep the Instance after Delete returns. Do
	// not release the write-back until the project API confirms it is gone.
	if err := projectClient.Get(ctx, projectionKey, &projection); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed checking instance projection %s deletion in project cluster %q: %w", projectionKey, clusterName, err)
	}
	return fmt.Errorf("waiting for instance projection %s to be deleted in project cluster %q", projectionKey, clusterName)
}

// SetupWithManager registers the InstanceProjector on mgr, which must be the
// leader-elected local manager, watching Instances from federationCluster.
// FederationClient and MCManager must be set before calling this method.
func (r *InstanceProjector) SetupWithManager(mgr manager.Manager, federationCluster cluster.Cluster) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("instance-projector").
		WatchesRawSource(source.Kind(
			federationCluster.GetCache(),
			&computev1alpha.Instance{},
			&handler.TypedEnqueueRequestForObject[*computev1alpha.Instance]{},
		)).
		Complete(r)
}
