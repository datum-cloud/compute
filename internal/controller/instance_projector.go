// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"maps"
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

	// 1. Fetch the Instance from the upstream Karmada control plane.
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
	// namespace name (read from the upstream Karmada namespace label set by the federator),
	// so we can resolve the target namespace directly without scanning.
	targetNamespace := downstreamInstance.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	if targetNamespace == "" {
		logger.Info("Instance missing upstream-namespace label, requeueing",
			"namespace", downstreamInstance.Namespace, "name", downstreamInstance.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// 5. Resolve the owning WorkloadDeployment by NAME in the project cluster.
	//
	// Core invariant: the ownerReference MUST be built from a project-cluster
	// object obtained via projectClient.Get — never from any edge/Karmada
	// identity. The WD name is stable across all planes (project cluster, Karmada,
	// edge) and is the correct cross-plane identifier.
	//
	// Resolution order:
	//  a) Read WorkloadDeploymentNameLabel from the downstream Instance (stamped by
	//     the edge stateful control strategy).
	//  b) If absent (Instances created before the label was introduced), fall back
	//     to stripping the trailing "-<ordinal>" suffix from the Instance name.
	wdName := downstreamInstance.Labels[computev1alpha.WorkloadDeploymentNameLabel]
	if wdName == "" {
		wdName = wdNameFromInstanceName(downstreamInstance.Name)
	}
	if wdName == "" {
		logger.Info("cannot resolve WorkloadDeployment name from Instance — skipping projection",
			"instance", downstreamInstance.Name)
		return ctrl.Result{}, nil
	}

	// Fetch the project-cluster WD directly by name. The returned object carries
	// the project-cluster metadata.uid — the only UID that GC in the project
	// cluster can act on.
	var ownerWD computev1alpha.WorkloadDeployment
	if err := projectClient.Get(ctx, client.ObjectKey{Namespace: targetNamespace, Name: wdName}, &ownerWD); err != nil {
		if apierrors.IsNotFound(err) {
			// Either a transient ordering race (Instance projected before
			// WorkloadReconciler created the project WD) or the WD has been
			// deleted. In both cases, do NOT create an ownerless projection.
			// Requeue so the projection is created with a correct owner
			// reference once the WD exists. The 5 s interval matches the
			// existing upstream-namespace label requeue above.
			logger.Info("project WorkloadDeployment not found — requeueing without creating projection",
				"wdName", wdName, "namespace", targetNamespace)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed getting WorkloadDeployment %s/%s in project cluster %s: %w",
			targetNamespace, wdName, clusterName, err)
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
		maps.Copy(projection.Labels, downstreamInstance.Labels)
		// Overwrite the WD UID label with the project-cluster WD UID. The
		// downstream Instance carries the cell-plane WD UID (assigned by Karmada
		// when it propagated the WD), which never matches the project WD UID.
		// Consumers doing label-selector lookups by WorkloadDeploymentUIDLabel
		// (e.g. CLI CITY column) must see the project-side UID.
		projection.Labels[computev1alpha.WorkloadDeploymentUIDLabel] = string(ownerWD.UID)

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

// wdNameFromInstanceName derives the WorkloadDeployment name from an Instance
// name by stripping the trailing "-<ordinal>" suffix. Instance names follow the
// convention "<wd-name>-<ordinal>" (e.g. "my-api-default-dfw-0"), which is
// structurally enforced by the stateful control strategy. Returns empty string
// if the name does not contain a numeric suffix (unrecognised format).
//
// This is used as a fallback when the WorkloadDeploymentNameLabel is absent on
// Instances created before that label was introduced.
func wdNameFromInstanceName(name string) string {
	lastDash := strings.LastIndex(name, "-")
	if lastDash <= 0 {
		return ""
	}
	suffix := name[lastDash+1:]
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return ""
		}
	}
	if len(suffix) == 0 {
		return ""
	}
	return name[:lastDash]
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
