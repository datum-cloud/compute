// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// InstanceProjector watches Instance objects written back to the Karmada API
// Server by POP-cell InstanceReconcilers and creates read-only projections in
// the corresponding project namespace within each project cluster.
//
// Namespace resolution: a Karmada Instance lives in namespace
// `ns-<project-namespace-uid>`. The UID portion is matched against the UID of
// namespaces in the project cluster to find the target namespace.
//
// Ownership: each projected Instance is owned by the project WorkloadDeployment
// so that it is garbage-collected via cascading deletion when the deployment is
// removed from the project cluster.
//
// The controller is registered with a standard manager.Manager pointed at the
// Karmada API Server — NOT the multicluster-runtime manager — so informer
// watches are scoped to the Karmada control plane.
type InstanceProjector struct {
	// KarmadaClient reads Instance objects from the Karmada API Server.
	// Must be set before SetupWithManager is called.
	KarmadaClient client.Client

	// MCManager provides access to project cluster clients via GetCluster.
	MCManager mcmanager.Manager
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/status,verbs=get;update;patch

func (r *InstanceProjector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("instance", req.NamespacedName)

	// 1. Fetch the Instance from the Karmada API Server.
	var karmadaInstance computev1alpha.Instance
	if err := r.KarmadaClient.Get(ctx, req.NamespacedName, &karmadaInstance); err != nil {
		if apierrors.IsNotFound(err) {
			// Instance was deleted from Karmada.  Projections are owned by the
			// project WorkloadDeployment, so cascading deletion handles cleanup.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed getting Karmada instance: %w", err)
	}

	// Only project Instances that carry the upstream tracking label; others were
	// not written by our InstanceReconciler write-back logic.
	encodedClusterName, ok := karmadaInstance.Labels[downstreamclient.UpstreamOwnerClusterNameLabel]
	if !ok {
		logger.V(1).Info("skipping instance without upstream cluster label")
		return ctrl.Result{}, nil
	}

	// 2. Resolve the project cluster name.
	// The encoded form is "cluster-<name>" with "/" replaced by "_".
	clusterName := strings.TrimPrefix(encodedClusterName, "cluster-")
	clusterName = strings.ReplaceAll(clusterName, "_", "/")

	// 3. Obtain the project cluster client.
	projectCluster, err := r.MCManager.GetCluster(ctx, clusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed getting project cluster %q: %w", clusterName, err)
	}
	projectClient := projectCluster.GetClient()

	// 4. Resolve the target project namespace by UID.
	// Karmada namespace names follow the convention "ns-<uid>"; strip the prefix
	// to obtain the UID, then scan the project cluster's namespace list for a match.
	namespaceUID := strings.TrimPrefix(karmadaInstance.Namespace, "ns-")

	var nsList corev1.NamespaceList
	if err := projectClient.List(ctx, &nsList); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed listing namespaces in project cluster %q: %w", clusterName, err)
	}

	var targetNamespace string
	for _, ns := range nsList.Items {
		if string(ns.UID) == namespaceUID {
			targetNamespace = ns.Name
			break
		}
	}
	if targetNamespace == "" {
		// The namespace hasn't been propagated to the project cluster yet.
		logger.Info("target namespace not found in project cluster, requeueing",
			"namespaceUID", namespaceUID, "cluster", clusterName)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// 5. Find the owning WorkloadDeployment in the project cluster by UID.
	// The Karmada Instance carries WorkloadDeploymentUIDLabel so we can find the
	// owning deployment without relying on field selectors.
	wdUID := karmadaInstance.Labels[computev1alpha.WorkloadDeploymentUIDLabel]

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
			Name:      karmadaInstance.Name,
			Namespace: targetNamespace,
		},
	}

	operationResult, err := controllerutil.CreateOrUpdate(ctx, projectClient, projection, func() error {
		// Propagate upstream tracking labels so consumers can filter by origin.
		if projection.Labels == nil {
			projection.Labels = make(map[string]string)
		}
		for k, v := range karmadaInstance.Labels {
			projection.Labels[k] = v
		}

		projection.Spec = karmadaInstance.Spec

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
	projection.Status = karmadaInstance.Status
	if err := projectClient.Status().Update(ctx, projection); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("failed updating Instance projection status: %w", err)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers the InstanceProjector with karmadaMgr, a standard
// manager.Manager configured against the Karmada API Server REST config.
// KarmadaClient and MCManager must be set before calling this method.
func (r *InstanceProjector) SetupWithManager(karmadaMgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(karmadaMgr).
		For(&computev1alpha.Instance{}).
		Named("instance-projector").
		Complete(r)
}
