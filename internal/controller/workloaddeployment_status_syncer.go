// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// WorkloadDeploymentStatusSyncer watches WorkloadDeployments on the Karmada
// federation hub and syncs their aggregated status back to the corresponding
// project-cluster WorkloadDeployment.
//
// It is registered with the federationMgr (a standard manager.Manager pointed
// at the Karmada control plane) so it reacts to status changes written by
// Karmada's statusAggregation interpreter. This complements the existing
// syncStatusFromDownstream call in WorkloadDeploymentFederator, which only runs
// when the project-side WD changes (spec updates, restarts). The syncer makes
// status propagation reactive: status flows to the project cluster as soon as
// Karmada aggregates it from the cell.
//
// Namespace resolution: the Karmada WD lives in ns-<project-namespace-uid>.
// That namespace is labeled by WorkloadDeploymentFederator.ensureDownstreamNamespace
// with UpstreamOwnerClusterNameLabel and UpstreamOwnerNamespaceLabel. The syncer
// reads those labels to locate the project cluster and namespace, then looks up
// the project WD by name (which is stable across all planes).
type WorkloadDeploymentStatusSyncer struct {
	// FederationClient reads WorkloadDeployments from the Karmada federation
	// control plane.
	FederationClient client.Client

	// MCManager provides project-cluster clients via GetCluster.
	MCManager mcmanager.Manager
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/status,verbs=get;update;patch

func (r *WorkloadDeploymentStatusSyncer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("workloaddeployment", req.NamespacedName)

	// 1. Fetch the WorkloadDeployment from the Karmada federation hub.
	var karmadaWD computev1alpha.WorkloadDeployment
	if err := r.FederationClient.Get(ctx, req.NamespacedName, &karmadaWD); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted from the hub — no projection to update.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed getting Karmada WorkloadDeployment: %w", err)
	}

	// 2. Read the Karmada namespace to resolve the upstream cluster name.
	// WorkloadDeploymentFederator.ensureDownstreamNamespace stamps the namespace
	// with UpstreamOwnerClusterNameLabel ("cluster-<name>") and
	// UpstreamOwnerNamespaceLabel (project namespace name).
	var ns corev1.Namespace
	if err := r.FederationClient.Get(ctx, client.ObjectKey{Name: req.Namespace}, &ns); err != nil {
		if apierrors.IsNotFound(err) {
			// Namespace not yet labeled or removed — not an error; skip quietly.
			logger.Info("Karmada namespace not found, skipping status sync", "namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed getting Karmada namespace %q: %w", req.Namespace, err)
	}

	encodedClusterName, ok := ns.Labels[downstreamclient.UpstreamOwnerClusterNameLabel]
	if !ok {
		// Namespace not yet stamped by the federator (e.g. WD predates the label).
		logger.Info("Karmada namespace missing upstream-cluster-name label, skipping status sync",
			"namespace", req.Namespace)
		return ctrl.Result{}, nil
	}

	// 3. Decode the cluster name ("cluster-<name>" with "/" encoded as "_").
	clusterName := strings.TrimPrefix(encodedClusterName, "cluster-")
	clusterName = strings.ReplaceAll(clusterName, "_", "/")

	// 4. Resolve the project namespace from the WD's own label (stamped by
	// upsertDownstreamDeployment). Fall back to the namespace label if absent.
	targetNamespace := karmadaWD.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	if targetNamespace == "" {
		targetNamespace = ns.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	}
	if targetNamespace == "" {
		logger.Info("cannot resolve project namespace for Karmada WorkloadDeployment, skipping status sync",
			"namespace", req.Namespace, "name", req.Name)
		return ctrl.Result{}, nil
	}

	// 5. Obtain the project cluster client.
	projectCluster, err := r.MCManager.GetCluster(ctx, multicluster.ClusterName(clusterName))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed getting project cluster %q: %w", clusterName, err)
	}
	projectClient := projectCluster.GetClient()

	// 6. Fetch the project-cluster WD by name. The WD name is stable across all
	// planes (project, Karmada, cell) and is the correct cross-plane identifier.
	var projectWD computev1alpha.WorkloadDeployment
	if err := projectClient.Get(ctx, client.ObjectKey{
		Namespace: targetNamespace,
		Name:      req.Name,
	}, &projectWD); err != nil {
		if apierrors.IsNotFound(err) {
			// Project WD may not exist yet (ordering race) or may have been
			// deleted. Not an error — the federator will handle cleanup.
			logger.Info("project WorkloadDeployment not found, skipping status sync",
				"cluster", clusterName, "namespace", targetNamespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed getting project WorkloadDeployment %s/%s in cluster %q: %w",
			targetNamespace, req.Name, clusterName, err)
	}

	// 7. No-op when status is already equal — avoid spurious writes.
	if equality.Semantic.DeepEqual(projectWD.Status, karmadaWD.Status) {
		return ctrl.Result{}, nil
	}

	// 8. Write the Karmada-aggregated status to the project WD.
	// Use Status().Update() rather than Patch() so that zero-value int32 fields
	// (currentReplicas, readyReplicas) are always included in the request body.
	// MergeFrom omits unchanged zero-value fields, which would silently drop
	// required status sub-fields on the project side.
	projectWD.Status = karmadaWD.Status
	if err := projectClient.Status().Update(ctx, &projectWD); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed updating project WorkloadDeployment status: %w", err)
	}

	logger.Info("synced WorkloadDeployment status from Karmada hub to project cluster",
		"cluster", clusterName, "namespace", targetNamespace)

	return ctrl.Result{}, nil
}

// SetupWithManager registers the WorkloadDeploymentStatusSyncer with
// federationMgr, a standard manager.Manager pointed at the Karmada federation
// control plane. It watches WorkloadDeployments on the hub and reacts when their
// status is updated by Karmada's statusAggregation interpreter.
func (r *WorkloadDeploymentStatusSyncer) SetupWithManager(federationMgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(federationMgr).
		For(&computev1alpha.WorkloadDeployment{}).
		Named("workload-deployment-status-syncer").
		Complete(r)
}
