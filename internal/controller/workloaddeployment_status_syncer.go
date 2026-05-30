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

// WorkloadDeploymentStatusSyncer syncs a Karmada-hub WorkloadDeployment's
// aggregated status to its project-cluster counterpart. It runs on the
// federation hub so status reaches the project cluster the moment Karmada
// aggregates it; the federator's own syncStatusFromDownstream only fires on
// project-side spec changes.
type WorkloadDeploymentStatusSyncer struct {
	FederationClient client.Client
	MCManager        mcmanager.Manager
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/status,verbs=get;update;patch

func (r *WorkloadDeploymentStatusSyncer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("workloaddeployment", req.NamespacedName)

	var karmadaWD computev1alpha.WorkloadDeployment
	if err := r.FederationClient.Get(ctx, req.NamespacedName, &karmadaWD); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed getting Karmada WorkloadDeployment: %w", err)
	}

	// The federator stamps the hub namespace with the upstream cluster name and
	// project namespace; resolve both to locate the project-side WD.
	var ns corev1.Namespace
	if err := r.FederationClient.Get(ctx, client.ObjectKey{Name: req.Namespace}, &ns); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Karmada namespace not found, skipping status sync", "namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed getting Karmada namespace %q: %w", req.Namespace, err)
	}

	encodedClusterName, ok := ns.Labels[downstreamclient.UpstreamOwnerClusterNameLabel]
	if !ok {
		logger.Info("Karmada namespace missing upstream-cluster-name label, skipping status sync",
			"namespace", req.Namespace)
		return ctrl.Result{}, nil
	}

	// Encoded as "cluster-<name>" with "/" replaced by "_".
	clusterName := strings.TrimPrefix(encodedClusterName, "cluster-")
	clusterName = strings.ReplaceAll(clusterName, "_", "/")

	// Prefer the WD's own namespace label; fall back to the hub namespace's.
	targetNamespace := karmadaWD.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	if targetNamespace == "" {
		targetNamespace = ns.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	}
	if targetNamespace == "" {
		logger.Info("cannot resolve project namespace for Karmada WorkloadDeployment, skipping status sync",
			"namespace", req.Namespace, "name", req.Name)
		return ctrl.Result{}, nil
	}

	projectCluster, err := r.MCManager.GetCluster(ctx, multicluster.ClusterName(clusterName))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed getting project cluster %q: %w", clusterName, err)
	}
	projectClient := projectCluster.GetClient()

	// The WD name is stable across planes, so it is the cross-plane key.
	var projectWD computev1alpha.WorkloadDeployment
	if err := projectClient.Get(ctx, client.ObjectKey{
		Namespace: targetNamespace,
		Name:      req.Name,
	}, &projectWD); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("project WorkloadDeployment not found, skipping status sync",
				"cluster", clusterName, "namespace", targetNamespace, "name", req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed getting project WorkloadDeployment %s/%s in cluster %q: %w",
			targetNamespace, req.Name, clusterName, err)
	}

	if equality.Semantic.DeepEqual(projectWD.Status, karmadaWD.Status) {
		return ctrl.Result{}, nil
	}

	// Update (not Patch): MergeFrom omits zero-value int32s, which would drop
	// required status fields such as currentReplicas/readyReplicas.
	projectWD.Status = karmadaWD.Status
	if err := projectClient.Status().Update(ctx, &projectWD); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed updating project WorkloadDeployment status: %w", err)
	}

	logger.Info("synced WorkloadDeployment status from Karmada hub to project cluster",
		"cluster", clusterName, "namespace", targetNamespace)

	return ctrl.Result{}, nil
}

// SetupWithManager registers the syncer on the federation hub manager.
func (r *WorkloadDeploymentStatusSyncer) SetupWithManager(federationMgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(federationMgr).
		For(&computev1alpha.WorkloadDeployment{}).
		Named("workload-deployment-status-syncer").
		Complete(r)
}
