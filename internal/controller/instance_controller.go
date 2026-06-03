// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	"go.miloapis.com/milo/pkg/downstreamclient"

	"go.datum.net/compute/internal/controller/instancecontrol"
	quotametrics "go.datum.net/compute/internal/quota"
)

const (
	// instanceQuotaFinalizer ensures the quota ResourceClaim is deleted when
	// an Instance is removed.
	instanceQuotaFinalizer = "quota.compute.datumapis.com/claim-cleanup"

	// instanceControllerFinalizer is registered with the finalizer framework and
	// triggers downstream write-back cleanup on deletion.
	instanceControllerFinalizer = "compute.datumapis.com/instance-controller"

	// instanceQuotaClaimSourceLabel is stamped on ResourceClaim objects with the
	// name of the edge cluster that created them. The claim watch predicate uses
	// this label to filter out claims written by other edge controllers targeting
	// the same project control planes.
	instanceQuotaClaimSourceLabel = "compute.datumapis.com/source-cluster"

	// instanceQuotaClaimNamespaceLabel records the source Instance's namespace on
	// the ResourceClaim. The claim lives in the project's quota namespace (not the
	// Instance's namespace), so the claim watch reads this label to map a grant
	// back to the owning Instance.
	instanceQuotaClaimNamespaceLabel = "compute.datumapis.com/instance-namespace"

	// instanceQuotaClaimNamePrefix namespaces an Instance's ResourceClaim name by
	// resource type. Claims for different resource kinds share the project quota
	// namespace, so the Instance name alone (unique among Instances, but not
	// across kinds) could collide with another kind's claim — the prefix prevents
	// that. The claim watch strips it to recover the Instance name.
	instanceQuotaClaimNamePrefix = "instance-"

	// quotaResourceTypeInstances is the quota resource type for Instance count.
	quotaResourceTypeInstances = "compute.datumapis.com/instances"

	// miloProjectAPIGroup is the API group for Milo resource-manager resources.
	miloProjectAPIGroup = "resourcemanager.miloapis.com"

	// miloProjectKind is the Kind used for Milo Project resources.
	miloProjectKind = "Project"

	// msgNotProgrammed is the human-readable message for the not-programmed state.
	msgNotProgrammed = "Instance has not been programmed"

	// msgInstanceReady is the human-readable message for the ready state.
	msgInstanceReady = "Instance is ready"

	// msgInstanceProgrammed is the human-readable message for the programmed state.
	msgInstanceProgrammed = "Instance has been programmed"

	// msgInstanceRunning is the human-readable message for the running state.
	msgInstanceRunning = "Instance is running"

	// reasonNetworkFailedToCreate is the reason code for network creation failure.
	reasonNetworkFailedToCreate = "NetworkFailedToCreate"
)

// clusterGetter is the subset of mcmanager.Manager used by InstanceReconciler.
// Keeping it narrow allows unit tests to substitute a minimal fake.
type clusterGetter interface {
	GetCluster(ctx context.Context, clusterName multicluster.ClusterName) (cluster.Cluster, error)
}

// InstanceProjectIDFunc derives the Milo project ID for a given Instance.
// In Milo mode the project ID equals the multicluster ClusterName. In
// single-cell mode it is decoded from the upstream-cluster-name namespace label.
// Returns ("", nil) when the instance has no project affiliation (skip quota).
// Returns ("", err) for transient failures that should trigger a requeue.
type InstanceProjectIDFunc func(
	ctx context.Context,
	clusterName multicluster.ClusterName,
	instance *computev1alpha.Instance,
) (string, error)

// InstanceProjectNamespaceFunc derives the in-project namespace where
// ResourceClaims for a given Instance should be created. In Milo mode this
// equals instance.Namespace. In single-cell mode it comes from the
// upstream-namespace namespace label.
// Returns ("", nil) when the instance has no project affiliation (skip quota).
// Returns ("", err) for transient failures that should trigger a requeue.
type InstanceProjectNamespaceFunc func(
	ctx context.Context,
	clusterName multicluster.ClusterName,
	instance *computev1alpha.Instance,
) (string, error)

// InstanceReconciler reconciles an Instance object
type InstanceReconciler struct {
	mgr                clusterGetter
	scheme             *runtime.Scheme
	quotaClientManager *quotametrics.ProjectQuotaClientManager
	edgeClusterName    string
	// recorder emits Kubernetes events on the Instance object for quota failure
	// modes so operators can diagnose issues via `kubectl describe`.
	recorder record.EventRecorder
	// projectIDForInstance derives the Milo project ID used for quota
	// ResourceClaim management. In Milo mode it returns string(clusterName); in
	// single-cell mode it reads the upstream-cluster-name label from the edge
	// namespace and decodes "cluster-<name>" → "<name>".
	projectIDForInstance InstanceProjectIDFunc
	// projectNamespaceForInstance derives the in-project namespace where
	// ResourceClaims must be created. In Milo mode the ResourceClaim lives in
	// instance.Namespace (the project-level namespace); in single-cell mode the
	// edge namespace is ns-{uid} which does not exist in the project control
	// plane — the real namespace is the upstream-namespace label value (e.g.
	// "default"). When nil, falls back to instance.Namespace.
	projectNamespaceForInstance InstanceProjectNamespaceFunc
	// clusterNameForProject maps a Milo project ID back to the multicluster
	// ClusterName that owns that project's workloads. In Milo mode the
	// ClusterName equals the project ID. In single-cell mode the only registered
	// cluster is "single" regardless of project ID. When nil, falls back to
	// multicluster.ClusterName(projectID), which is correct for Milo mode.
	clusterNameForProject func(projectID string) multicluster.ClusterName
	// FederationClient is an optional client pointing at the upstream
	// Karmada/federation control plane (configured via --federation-kubeconfig).
	// When non-nil, the reconciler writes a copy of each Instance back to the
	// federation control plane so that the InstanceProjector (running in the
	// management cluster) can aggregate status across all POP cells. Set to nil to
	// disable federation write-back (e.g. in non-federation deployments).
	FederationClient client.Client
	finalizers       finalizer.Finalizers
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups=quota.miloapis.com,resources=resourceclaims,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get

func (r *InstanceReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (_ ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	ctx = mccontext.WithCluster(ctx, req.ClusterName)
	var instance computev1alpha.Instance
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &instance); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Run the finalizer framework first. This handles downstream write-back cleanup
	// via the Finalize method registered below.
	finalizationResult, err := r.finalizers.Finalize(ctx, &instance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to finalize: %w", err)
	}
	if finalizationResult.Updated {
		if err = cl.GetClient().Update(ctx, &instance); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update based on finalization result: %w", err)
		}
		return ctrl.Result{}, nil
	}

	logger.Info("reconciling instance")
	defer logger.Info("reconcile complete")

	if !instance.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.reconcileDeletion(ctx, cl.GetClient(), req.ClusterName, &instance)
	}

	if !controllerutil.ContainsFinalizer(&instance, instanceQuotaFinalizer) {
		controllerutil.AddFinalizer(&instance, instanceQuotaFinalizer)
		if err := cl.GetClient().Update(ctx, &instance); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed adding quota finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	statusChanged, quotaErr := r.reconcileQuotaCondition(ctx, req.ClusterName, &instance)

	// Even when reconcileQuotaCondition returns a transient error, persist any
	// condition change first so the failure reason is visible on the Instance.
	// We return the error afterwards so controller-runtime requeues with backoff.
	readyChanged, err := r.reconcileInstanceReadyCondition(ctx, cl.GetClient(), &instance, r.checkForNetworkCreationFailure)
	if err != nil {
		return ctrl.Result{}, err
	}

	if statusChanged || readyChanged {
		if err := cl.GetClient().Status().Update(ctx, &instance); err != nil {
			return ctrl.Result{}, err
		}
		// Return with the quota error (nil or transient) so controller-runtime
		// requeues with backoff on failures. On the success path (quotaErr==nil)
		// we fall through to removeQuotaSchedulingGate below instead of returning
		// early, so the gate is cleared in the same reconcile pass rather than
		// waiting for a requeue that may never come (ResourceClaim is immutable
		// and local Instances are not watched).
		if quotaErr != nil {
			if err := r.writeBackToUpstream(ctx, req.ClusterName, &instance); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, quotaErr
		}
	} else if quotaErr != nil {
		// No status change but quota evaluation failed — return error to requeue.
		return ctrl.Result{}, quotaErr
	}

	if err := r.removeQuotaSchedulingGate(ctx, cl.GetClient(), &instance); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.writeBackToUpstream(ctx, req.ClusterName, &instance); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileDeletion handles quota-claim cleanup when an Instance is being
// deleted. It removes the quota finalizer once the ResourceClaim is gone.
func (r *InstanceReconciler) reconcileDeletion(ctx context.Context, cl client.Client, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) error {
	if !controllerutil.ContainsFinalizer(instance, instanceQuotaFinalizer) {
		return nil
	}

	if r.quotaClientManager != nil {
		projectID, err := r.resolveProjectID(ctx, clusterName, instance)
		if err != nil {
			return fmt.Errorf("resolving project ID during deletion: %w", err)
		}
		if projectID == "" {
			// Cannot locate the claim without a project ID. Log at ERROR and emit an
			// event so the operator is aware of the orphaned claim. Fall through to
			// finalizer removal so the Instance is not permanently stuck in Terminating.
			// The orphaned claim will count against project budget until Milo's TTL/GC
			// removes it.
			log.FromContext(ctx).Error(nil, "project ID unresolvable during deletion; ResourceClaim may be orphaned — budget leak possible",
				"instance", instance.Name, "namespace", instance.Namespace)
			r.recorder.Event(instance, corev1.EventTypeWarning,
				"QuotaClaimOrphaned",
				"Skipping ResourceClaim cleanup: project ID could not be resolved; claim may be orphaned in Milo project control plane")
			quotametrics.ClaimOrphanedTotal.Inc()
		} else {
			projectClient, err := r.quotaClientManager.ClientForProject(ctx, projectID, r.scheme)
			if err != nil {
				return fmt.Errorf("failed getting quota client for deletion: %w", err)
			}

			claimNamespace, err := r.resolveProjectNamespace(ctx, clusterName, instance)
			if err != nil {
				return fmt.Errorf("resolving project namespace during deletion: %w", err)
			}
			claimName := quotaClaimName(instance)
			var claim quotav1alpha1.ResourceClaim
			if err := projectClient.Get(ctx, client.ObjectKey{Namespace: claimNamespace, Name: claimName}, &claim); err != nil {
				if !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed getting resource claim for deletion: %w", err)
				}
			} else {
				if err := projectClient.Delete(ctx, &claim); client.IgnoreNotFound(err) != nil {
					return fmt.Errorf("failed deleting resource claim: %w", err)
				}
			}
		}
	}

	controllerutil.RemoveFinalizer(instance, instanceQuotaFinalizer)
	if err := cl.Update(ctx, instance); err != nil {
		return fmt.Errorf("failed removing quota finalizer: %w", err)
	}
	return nil
}

// quotaClaimName returns the name of the ResourceClaim backing an Instance's
// quota: the Instance name (unique among Instances within the project control
// plane) prefixed by instanceQuotaClaimNamePrefix to avoid colliding with other
// resource kinds' claims in the shared quota namespace. The owning Instance's
// namespace is preserved on the claim via instanceQuotaClaimNamespaceLabel so
// the claim watch can map a grant back to the Instance.
func quotaClaimName(instance *computev1alpha.Instance) string {
	return instanceQuotaClaimNamePrefix + instance.Name
}

// reconcileQuotaCondition reconciles the ResourceClaim and updates the
// InstanceQuotaGranted status condition. It returns (changed, err) where
// changed=true means a status update is required, and err non-nil means the
// reconciler should requeue (with backoff) in addition to writing the condition.
func (r *InstanceReconciler) reconcileQuotaCondition(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) (bool, error) {
	grantedCondition, claimErr := r.reconcileQuotaClaim(ctx, clusterName, instance)

	// reconcileQuotaClaim returns (condition, err). A non-nil error signals a
	// transient infrastructure failure; a non-nil condition carries the reason to
	// write. Both can be non-nil: write the condition AND requeue with backoff.
	switch {
	case grantedCondition == nil && claimErr == nil:
		// No claim yet and no error: labels not yet propagated. Stay PendingEvaluation.
		return apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceQuotaGranted,
			Status:             metav1.ConditionUnknown,
			Reason:             computev1alpha.InstanceQuotaGrantedReasonPendingEvaluation,
			Message:            "Waiting for quota evaluation",
			ObservedGeneration: instance.Generation,
		}), nil

	case grantedCondition != nil && grantedCondition.Status == metav1.ConditionFalse &&
		grantedCondition.Reason == quotav1alpha1.ResourceClaimPendingReason:
		// Claim exists but pending — no AllowanceBucket. Distinct from "evaluating".
		changed := apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceQuotaGranted,
			Status:             metav1.ConditionUnknown,
			Reason:             computev1alpha.InstanceQuotaGrantedReasonNoBudget,
			Message:            "ResourceClaim is pending: no AllowanceBucket configured for this project",
			ObservedGeneration: instance.Generation,
		})
		r.recorder.Event(instance, corev1.EventTypeWarning,
			computev1alpha.InstanceQuotaGrantedReasonNoBudget,
			"ResourceClaim pending: no AllowanceBucket configured for this project")
		quotametrics.EvalFailuresTotal.WithLabelValues(quotametrics.ReasonNoBudget).Inc()
		return changed, claimErr

	case grantedCondition != nil && grantedCondition.Type == computev1alpha.InstanceQuotaGranted:
		// reconcileQuotaClaim populated a structured failure condition.
		changed := apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceQuotaGranted,
			Status:             grantedCondition.Status,
			Reason:             grantedCondition.Reason,
			Message:            grantedCondition.Message,
			ObservedGeneration: instance.Generation,
		})
		return changed, claimErr

	case grantedCondition != nil && grantedCondition.Status == metav1.ConditionTrue:
		return apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceQuotaGranted,
			Status:             metav1.ConditionTrue,
			Reason:             computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable,
			Message:            grantedCondition.Message,
			ObservedGeneration: instance.Generation,
		}), claimErr

	case grantedCondition != nil: // False, non-pending reason from ResourceClaim
		reason := computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded
		if grantedCondition.Reason == quotav1alpha1.ResourceClaimValidationFailedReason {
			reason = computev1alpha.InstanceQuotaGrantedReasonValidationFailed
		}
		return apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceQuotaGranted,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            grantedCondition.Message,
			ObservedGeneration: instance.Generation,
		}), claimErr

	default: // grantedCondition == nil && claimErr != nil — should not reach here
		return false, claimErr
	}
}

// removeQuotaSchedulingGate removes the quota scheduling gate from the
// Instance spec once QuotaGranted=True has been persisted to status.
// It guards on ObservedGeneration to prevent a stale True condition from
// generation N unblocking a generation N+1 instance before quota for the
// new spec has been evaluated.
func (r *InstanceReconciler) removeQuotaSchedulingGate(ctx context.Context, cl client.Client, instance *computev1alpha.Instance) error {
	quotaGrantedCond := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceQuotaGranted)
	if quotaGrantedCond == nil || quotaGrantedCond.Status != metav1.ConditionTrue {
		return nil
	}
	// Stale condition guard: only remove the gate if the condition reflects the
	// current spec generation. A condition from an older generation means quota
	// has not yet been evaluated for the current spec.
	if quotaGrantedCond.ObservedGeneration != instance.Generation {
		return nil
	}
	if instance.Spec.Controller == nil {
		return nil
	}

	newGates := make([]computev1alpha.SchedulingGate, 0, len(instance.Spec.Controller.SchedulingGates))
	gateRemoved := false
	for _, gate := range instance.Spec.Controller.SchedulingGates {
		if gate.Name == instancecontrol.QuotaSchedulingGate.String() {
			gateRemoved = true
			continue
		}
		newGates = append(newGates, gate)
	}
	if !gateRemoved {
		return nil
	}

	patch := client.MergeFrom(instance.DeepCopy())
	instance.Spec.Controller.SchedulingGates = newGates
	if err := cl.Patch(ctx, instance, patch); err != nil {
		return fmt.Errorf("failed patching quota scheduling gate: %w", err)
	}
	return nil
}

// Finalize removes the downstream write-back Instance when the local Instance is
// deleted. It is a no-op when downstream federation is disabled.
func (r *InstanceReconciler) Finalize(ctx context.Context, obj client.Object) (finalizer.Result, error) {
	if r.FederationClient == nil {
		return finalizer.Result{}, nil
	}

	instance := obj.(*computev1alpha.Instance)

	downstreamInstance := &computev1alpha.Instance{}
	err := r.FederationClient.Get(ctx, client.ObjectKeyFromObject(instance), downstreamInstance)
	if apierrors.IsNotFound(err) {
		// Already gone — nothing to do.
		return finalizer.Result{}, nil
	}
	if err != nil {
		return finalizer.Result{}, fmt.Errorf("failed getting downstream instance for deletion: %w", err)
	}

	if err := r.FederationClient.Delete(ctx, downstreamInstance); client.IgnoreNotFound(err) != nil {
		return finalizer.Result{}, fmt.Errorf("failed deleting downstream write-back instance: %w", err)
	}

	return finalizer.Result{}, nil
}

// writeBackToUpstream copies the Instance spec and status to the upstream
// Karmada/federation control plane so that the InstanceProjector can aggregate
// state from all POP cells. It is a no-op when FederationClient is nil (federation disabled).
func (r *InstanceReconciler) writeBackToUpstream(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) error {
	if r.FederationClient == nil {
		return nil
	}

	// Encode the POP-cell cluster name using the same convention as NSO's
	// MappedNamespaceResourceStrategy: "cluster-<name>" with "/" → "_".
	// This is the fallback; the namespace label takes precedence when present.
	encodedClusterName := "cluster-" + strings.ReplaceAll(string(clusterName), "/", "_")

	// Read the upstream project namespace name and cluster name from the namespace
	// labels stamped by NSO's MappedNamespaceResourceStrategy. These carry the true
	// project cluster name (e.g. "cluster-datum-cloud") and upstream namespace (e.g.
	// "default"), which the InstanceProjector needs to find the right project cluster.
	upstreamNamespace := instance.Namespace // fallback: cell namespace (ns-<uid>)
	var downstreamNS corev1.Namespace
	if err := r.FederationClient.Get(ctx, client.ObjectKey{Name: instance.Namespace}, &downstreamNS); err == nil {
		if v := downstreamNS.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]; v != "" {
			upstreamNamespace = v
		}
		if v := downstreamNS.Labels[downstreamclient.UpstreamOwnerClusterNameLabel]; v != "" {
			encodedClusterName = v
		}
	}

	logger := log.FromContext(ctx)
	missingLabels := []string{}
	for _, key := range []string{
		computev1alpha.WorkloadUIDLabel,
		computev1alpha.WorkloadDeploymentUIDLabel,
		computev1alpha.InstanceIndexLabel,
	} {
		if instance.Labels[key] == "" {
			missingLabels = append(missingLabels, key)
		}
	}
	if len(missingLabels) > 0 {
		logger.Info("instance is missing linking labels for write-back; projection owner-ref will not be set",
			"instance", instance.Name, "namespace", instance.Namespace,
			"missingLabels", missingLabels)
	}

	writeBack := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				downstreamclient.UpstreamOwnerClusterNameLabel: encodedClusterName,
				downstreamclient.UpstreamOwnerNamespaceLabel:   upstreamNamespace,
				computev1alpha.WorkloadUIDLabel:                instance.Labels[computev1alpha.WorkloadUIDLabel],
				computev1alpha.WorkloadDeploymentUIDLabel:      instance.Labels[computev1alpha.WorkloadDeploymentUIDLabel],
				computev1alpha.InstanceIndexLabel:              instance.Labels[computev1alpha.InstanceIndexLabel],
				computev1alpha.WorkloadDeploymentNameLabel:     instance.Labels[computev1alpha.WorkloadDeploymentNameLabel],
				computev1alpha.CityCodeLabel:                   instance.Labels[computev1alpha.CityCodeLabel],
				computev1alpha.WorkloadNameLabel:               instance.Labels[computev1alpha.WorkloadNameLabel],
				computev1alpha.PlacementNameLabel:              instance.Labels[computev1alpha.PlacementNameLabel],
			},
		},
		Spec: instance.Spec,
	}

	existing := &computev1alpha.Instance{}
	err := r.FederationClient.Get(ctx, client.ObjectKeyFromObject(writeBack), existing)
	if apierrors.IsNotFound(err) {
		// Ensure the namespace exists in the downstream control plane before creating the Instance.
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instance.Namespace}}
		if err := r.FederationClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed ensuring downstream namespace: %w", err)
		}
		if err := r.FederationClient.Create(ctx, writeBack); err != nil {
			return fmt.Errorf("failed creating downstream write-back instance: %w", err)
		}
		writeBack.Status = instance.Status
		if err := r.FederationClient.Status().Update(ctx, writeBack); err != nil {
			return fmt.Errorf("failed updating downstream write-back instance status after create: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed getting downstream instance: %w", err)
	}

	// Build a comparable map containing only the keys this function owns so that
	// Karmada-managed labels on the existing object do not cause spurious updates.
	ownedLabels := make(map[string]string, len(writeBack.Labels))
	for k := range writeBack.Labels {
		ownedLabels[k] = existing.Labels[k]
	}

	// Update spec + labels only if owned keys differ.
	if !apiequality.Semantic.DeepEqual(existing.Spec, instance.Spec) ||
		!apiequality.Semantic.DeepEqual(ownedLabels, writeBack.Labels) {
		existing.Spec = instance.Spec
		// Merge writeBack.Labels into existing.Labels. Only keys owned by
		// writeBackToUpstream are written; any labels Karmada or other actors
		// have placed on the downstream object are preserved.
		if existing.Labels == nil {
			existing.Labels = make(map[string]string)
		}
		maps.Copy(existing.Labels, writeBack.Labels)
		if err := r.FederationClient.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed updating downstream write-back instance: %w", err)
		}
	}

	// Update status only if it differs.
	if !apiequality.Semantic.DeepEqual(existing.Status, instance.Status) {
		existing.Status = instance.Status
		if err := r.FederationClient.Status().Update(ctx, existing); err != nil {
			return fmt.Errorf("failed updating downstream write-back instance status: %w", err)
		}
	}

	return nil
}

// reconcileQuotaClaim attempts to create or observe a ResourceClaim for the
// given instance. It returns:
//   - (nil, nil)       — labels not yet propagated; caller sets PendingEvaluation
//   - (condition, nil) — terminal condition (True/False/Unknown from claim or failure)
//   - (condition, err) — condition to write + transient error to requeue with backoff
//
// The condition's Type field is always InstanceQuotaGranted when set by this function
// to distinguish it from ResourceClaim conditions returned directly.
func (r *InstanceReconciler) reconcileQuotaClaim(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) (*metav1.Condition, error) {
	if r.quotaClientManager == nil {
		return &metav1.Condition{
			Type:    computev1alpha.InstanceQuotaGranted,
			Status:  metav1.ConditionTrue,
			Reason:  computev1alpha.InstanceQuotaGrantedReasonQuotaDisabled,
			Message: "Quota enforcement disabled: no credential configured",
		}, nil
	}

	logger := log.FromContext(ctx)

	projectID, err := r.resolveProjectID(ctx, clusterName, instance)
	if err != nil {
		// Transient: namespace API unreachable. Return structured condition + error.
		msg := fmt.Sprintf("Could not resolve project ID: %v", err)
		r.recorder.Event(instance, corev1.EventTypeWarning,
			computev1alpha.InstanceQuotaGrantedReasonProjectIDUnresolvable, msg)
		quotametrics.EvalFailuresTotal.WithLabelValues(quotametrics.ReasonProjectIDUnresolvable).Inc()
		return &metav1.Condition{
			Type:    computev1alpha.InstanceQuotaGranted,
			Status:  metav1.ConditionFalse,
			Reason:  computev1alpha.InstanceQuotaGrantedReasonProjectIDUnresolvable,
			Message: msg,
		}, fmt.Errorf("resolving project ID for instance %s/%s: %w", instance.Namespace, instance.Name, err)
	}
	if projectID == "" {
		// Labels not yet propagated — bootstrap transient, not an error.
		return nil, nil
	}

	projectClient, err := r.quotaClientManager.ClientForProject(ctx, projectID, r.scheme)
	if err != nil {
		msg := fmt.Sprintf("Failed to build quota client for project %q: %v", projectID, err)
		r.recorder.Event(instance, corev1.EventTypeWarning,
			computev1alpha.InstanceQuotaGrantedReasonBackendUnavailable, msg)
		quotametrics.EvalFailuresTotal.WithLabelValues(quotametrics.ReasonBackendUnavailable).Inc()
		return &metav1.Condition{
			Type:    computev1alpha.InstanceQuotaGranted,
			Status:  metav1.ConditionFalse,
			Reason:  computev1alpha.InstanceQuotaGrantedReasonBackendUnavailable,
			Message: msg,
		}, fmt.Errorf("failed getting quota client for project %q: %w", projectID, err)
	}

	claimNamespace, err := r.resolveProjectNamespace(ctx, clusterName, instance)
	if err != nil {
		msg := fmt.Sprintf("Could not resolve project namespace: %v", err)
		r.recorder.Event(instance, corev1.EventTypeWarning,
			computev1alpha.InstanceQuotaGrantedReasonProjectIDUnresolvable, msg)
		quotametrics.EvalFailuresTotal.WithLabelValues(quotametrics.ReasonProjectIDUnresolvable).Inc()
		return &metav1.Condition{
			Type:    computev1alpha.InstanceQuotaGranted,
			Status:  metav1.ConditionFalse,
			Reason:  computev1alpha.InstanceQuotaGrantedReasonProjectIDUnresolvable,
			Message: msg,
		}, fmt.Errorf("resolving project namespace for instance %s/%s: %w", instance.Namespace, instance.Name, err)
	}
	if claimNamespace == "" {
		return nil, nil
	}

	claimName := quotaClaimName(instance)

	requests := []quotav1alpha1.ResourceRequest{
		{
			ResourceType: quotaResourceTypeInstances,
			Amount:       1,
		},
	}

	cpuMillicores, memMiB, resolved := resolveInstanceResources(instance)
	if !resolved {
		logger.Info("unable to resolve resource amounts from instance spec, claiming instance count only")
	} else {
		requests = append(requests,
			quotav1alpha1.ResourceRequest{
				ResourceType: "compute.datumapis.com/vcpus",
				Amount:       cpuMillicores,
			},
			quotav1alpha1.ResourceRequest{
				ResourceType: "compute.datumapis.com/memory",
				Amount:       memMiB,
			},
		)
	}

	desired := &quotav1alpha1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claimName,
			Namespace: claimNamespace,
			Labels: map[string]string{
				instanceQuotaClaimSourceLabel:    r.edgeClusterName,
				instanceQuotaClaimNamespaceLabel: instance.Namespace,
			},
		},
		Spec: quotav1alpha1.ResourceClaimSpec{
			ConsumerRef: quotav1alpha1.ConsumerRef{
				APIGroup: miloProjectAPIGroup,
				Kind:     miloProjectKind,
				Name:     projectID,
			},
			ResourceRef: quotav1alpha1.UnversionedObjectReference{
				APIGroup: miloProjectAPIGroup,
				Kind:     miloProjectKind,
				Name:     projectID,
			},
			Requests: requests,
		},
	}

	var existing quotav1alpha1.ResourceClaim
	if err := projectClient.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			// Claim doesn't exist yet — attempt to create it.
			createErr := projectClient.Create(ctx, desired)
			if createErr == nil {
				return nil, nil
			}
			return r.classifyCreateError(instance, projectID, claimNamespace, createErr)
		}
		// GET itself failed — treat as backend unavailable.
		msg := fmt.Sprintf("Quota backend unreachable getting ResourceClaim: %v", err)
		r.recorder.Event(instance, corev1.EventTypeWarning,
			computev1alpha.InstanceQuotaGrantedReasonBackendUnavailable, msg)
		quotametrics.EvalFailuresTotal.WithLabelValues(quotametrics.ReasonBackendUnavailable).Inc()
		return &metav1.Condition{
			Type:    computev1alpha.InstanceQuotaGranted,
			Status:  metav1.ConditionFalse,
			Reason:  computev1alpha.InstanceQuotaGrantedReasonBackendUnavailable,
			Message: msg,
		}, fmt.Errorf("failed getting resource claim: %w", err)
	}

	grantedCondition := apimeta.FindStatusCondition(existing.Status.Conditions, quotav1alpha1.ResourceClaimGranted)
	return grantedCondition, nil
}

// classifyCreateError maps a ResourceClaim creation error to a structured
// QuotaGranted condition with a specific reason, emits a Kubernetes event, and
// increments the appropriate metric counter.
func (r *InstanceReconciler) classifyCreateError(
	instance *computev1alpha.Instance,
	projectID, claimNamespace string,
	err error,
) (*metav1.Condition, error) {
	var reason, metricLabel, msg string

	switch {
	case apierrors.IsNotFound(err):
		// 404 on Create: either the project control plane path doesn't exist
		// (project deleted) or the namespace doesn't exist yet.
		if claimNamespace != "" {
			// Namespace-level 404.
			reason = computev1alpha.InstanceQuotaGrantedReasonNamespaceNotFound
			metricLabel = quotametrics.ReasonNamespaceNotFound
			msg = fmt.Sprintf("Quota claim namespace %q not found on project %q control plane", claimNamespace, projectID)
		} else {
			reason = computev1alpha.InstanceQuotaGrantedReasonProjectNotFound
			metricLabel = quotametrics.ReasonProjectNotFound
			msg = fmt.Sprintf("Milo project %q not found", projectID)
		}
	case apierrors.IsForbidden(err) || apierrors.IsInvalid(err):
		// 403/422: quota admission plugin rejected the claim.
		reason = computev1alpha.InstanceQuotaGrantedReasonMisconfigured
		metricLabel = quotametrics.ReasonMisconfigured
		msg = fmt.Sprintf("Quota admission rejected ResourceClaim for project %q: %v", projectID, err)
	default:
		// Connectivity or server error — treat as backend unavailable.
		reason = computev1alpha.InstanceQuotaGrantedReasonBackendUnavailable
		metricLabel = quotametrics.ReasonBackendUnavailable
		msg = fmt.Sprintf("Quota backend unreachable creating ResourceClaim: %v", err)
	}

	r.recorder.Event(instance, corev1.EventTypeWarning, reason, msg)
	quotametrics.EvalFailuresTotal.WithLabelValues(metricLabel).Inc()
	return &metav1.Condition{
		Type:    computev1alpha.InstanceQuotaGranted,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: msg,
	}, fmt.Errorf("failed creating resource claim: %w", err)
}

func resolveInstanceResources(instance *computev1alpha.Instance) (cpuMillicores int64, memMiB int64, resolved bool) {
	rt := instance.Spec.Runtime
	if rt.Sandbox != nil {
		var totalCPU resource.Quantity
		var totalMem resource.Quantity
		allSet := true
		for _, c := range rt.Sandbox.Containers {
			if c.Resources == nil || c.Resources.Limits == nil {
				allSet = false
				break
			}
			cpu, hasCPU := c.Resources.Limits[corev1.ResourceCPU]
			mem, hasMem := c.Resources.Limits[corev1.ResourceMemory]
			if !hasCPU || !hasMem {
				allSet = false
				break
			}
			totalCPU.Add(cpu)
			totalMem.Add(mem)
		}
		if !allSet || len(rt.Sandbox.Containers) == 0 {
			return 0, 0, false
		}
		return totalCPU.MilliValue(), totalMem.Value() / (1024 * 1024), true
	}

	cpu, hasCPU := rt.Resources.Requests[corev1.ResourceCPU]
	mem, hasMem := rt.Resources.Requests[corev1.ResourceMemory]
	if !hasCPU || !hasMem {
		return 0, 0, false
	}
	return cpu.MilliValue(), mem.Value() / (1024 * 1024), true
}

// networkFailureChecker is a function that checks if a network creation failure
// has occurred. It returns a boolean indicating if a failure has occurred, a
// message describing the failure, and an error if the check fails.
type networkFailureChecker func(ctx context.Context, upstreamClient client.Client, instance *computev1alpha.Instance) (failed bool, message string, err error)

func (r *InstanceReconciler) reconcileInstanceReadyCondition(
	ctx context.Context,
	clusterClient client.Client,
	instance *computev1alpha.Instance,
	networkFailureChecker networkFailureChecker,
) (changed bool, err error) {
	logger := log.FromContext(ctx)

	quotaGrantedCondition := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceQuotaGranted)
	if quotaGrantedCondition != nil && quotaGrantedCondition.Status == metav1.ConditionFalse {
		msg := quotaGrantedCondition.Message
		changed = apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceProgrammed,
			Status:             metav1.ConditionFalse,
			Reason:             computev1alpha.InstanceProgrammedReasonPendingQuota,
			Message:            msg,
			ObservedGeneration: instance.Generation,
		})
		changed = apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceRunning,
			Status:             metav1.ConditionFalse,
			Reason:             computev1alpha.InstanceProgrammedReasonPendingQuota,
			Message:            msg,
			ObservedGeneration: instance.Generation,
		}) || changed
		changed = apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceReady,
			Status:             metav1.ConditionFalse,
			Reason:             computev1alpha.InstanceProgrammedReasonPendingQuota,
			Message:            msg,
			ObservedGeneration: instance.Generation,
		}) || changed
		return changed, nil
	}

	readyCondition := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceReady)
	if readyCondition == nil {
		readyCondition = &metav1.Condition{
			Type:               computev1alpha.InstanceReady,
			Status:             metav1.ConditionFalse,
			Reason:             computev1alpha.InstanceProgrammedReasonPendingProgramming,
			ObservedGeneration: instance.Generation,
			Message:            msgNotProgrammed,
		}
	} else {
		readyCondition = readyCondition.DeepCopy()
	}

	if instance.Spec.Controller != nil && len(instance.Spec.Controller.SchedulingGates) > 0 {
		var schedulingGateNames []string
		for _, gate := range instance.Spec.Controller.SchedulingGates {
			schedulingGateNames = append(schedulingGateNames, gate.Name)
		}

		networkCreationFailure, networkCreationFailureMessage, err := networkFailureChecker(ctx, clusterClient, instance)
		if err != nil {
			return false, fmt.Errorf("failed checking for network creation failure: %w", err)
		}

		readyCondition.Status = metav1.ConditionFalse
		if networkCreationFailure {
			readyCondition.Reason = reasonNetworkFailedToCreate
			readyCondition.Message = networkCreationFailureMessage
		} else {
			readyCondition.Reason = computev1alpha.InstanceReadyReasonSchedulingGatesPresent
			readyCondition.Message = fmt.Sprintf("Scheduling gates present: %s", strings.Join(schedulingGateNames, ", "))
		}

		return apimeta.SetStatusCondition(&instance.Status.Conditions, *readyCondition), nil
	}

	pendingReason := "Pending"
	programmedCondition := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceProgrammed)
	if programmedCondition == nil || programmedCondition.Status != metav1.ConditionTrue {
		logger.Info("instance is not programmed", "instance", instance.Name)

		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = computev1alpha.InstanceProgrammedReasonPendingProgramming
		if programmedCondition != nil && programmedCondition.Reason != pendingReason {
			readyCondition.Reason = programmedCondition.Reason
		}

		readyCondition.Message = msgNotProgrammed
		if programmedCondition != nil && programmedCondition.Status != metav1.ConditionUnknown {
			readyCondition.Message = programmedCondition.Message
		}

		return apimeta.SetStatusCondition(&instance.Status.Conditions, *readyCondition), nil
	}

	logger.Info("instance is programmed", "instance", instance.Name)

	runningCondition := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceRunning)
	if runningCondition == nil || runningCondition.Status != metav1.ConditionTrue {
		logger.Info("instance is not running", "instance", instance.Name)

		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = pendingReason
		if runningCondition != nil && runningCondition.Reason != pendingReason {
			readyCondition.Reason = runningCondition.Reason
		}

		readyCondition.Message = "Instance is not running"
		if runningCondition != nil && runningCondition.Status != metav1.ConditionUnknown {
			readyCondition.Message = runningCondition.Message
		}

		return apimeta.SetStatusCondition(&instance.Status.Conditions, *readyCondition), nil
	}

	readyCondition.Status = metav1.ConditionTrue
	readyCondition.Reason = computev1alpha.InstanceReadyReasonRunning
	readyCondition.Message = msgInstanceReady

	return apimeta.SetStatusCondition(&instance.Status.Conditions, *readyCondition), nil
}

// Rough way to propagate creation errors up to the instance as soon as possible.
// Lots of room for improvement here.
func (r *InstanceReconciler) checkForNetworkCreationFailure(ctx context.Context, upstreamClient client.Client, instance *computev1alpha.Instance) (failed bool, message string, err error) {
	workloadDeploymentRef := metav1.GetControllerOf(instance)
	if workloadDeploymentRef == nil {
		return false, "", fmt.Errorf("instance is not owned by a workload deployment")
	}

	var workloadDeployment computev1alpha.WorkloadDeployment
	workloadDeploymentObjectKey := client.ObjectKey{
		Namespace: instance.Namespace,
		Name:      workloadDeploymentRef.Name,
	}
	if err := upstreamClient.Get(ctx, workloadDeploymentObjectKey, &workloadDeployment); err != nil {
		return false, "", fmt.Errorf("failed fetching workload deployment: %w", err)
	}

	for i := range instance.Spec.NetworkInterfaces {
		var networkBinding networkingv1alpha.NetworkBinding
		networkBindingObjectKey := client.ObjectKey{
			Namespace: workloadDeployment.Namespace,
			Name:      fmt.Sprintf("%s-net-%d", workloadDeployment.Name, i),
		}

		if err := upstreamClient.Get(ctx, networkBindingObjectKey, &networkBinding); client.IgnoreNotFound(err) != nil {
			return false, "", fmt.Errorf("failed checking for existing network binding: %w", err)
		}

		condition := apimeta.FindStatusCondition(networkBinding.Status.Conditions, networkingv1alpha.NetworkBindingReady)
		if condition != nil && condition.Status == metav1.ConditionFalse && condition.Reason == "NetworkFailedToCreate" {
			return true, condition.Message, nil
		}
	}

	return false, "", nil
}

// resolveProjectID returns the Milo project ID to use for quota calls.
// When projectIDForInstance is set it delegates to that function; otherwise it
// falls back to string(clusterName), which is correct for Milo-mode deployments
// where the cluster name IS the project name.
// Returns ("", nil) to signal "no project, skip quota". Returns ("", err) for
// transient failures that should cause a reconcile requeue.
func (r *InstanceReconciler) resolveProjectID(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) (string, error) {
	if r.projectIDForInstance != nil {
		return r.projectIDForInstance(ctx, clusterName, instance)
	}
	return string(clusterName), nil
}

// resolveProjectNamespace returns the namespace within the Milo project control
// plane where ResourceClaims for this instance should be created.
// When projectNamespaceForInstance is set it delegates to that function;
// otherwise it falls back to instance.Namespace, which is correct for
// Milo-mode deployments where the project-side namespace already matches the
// instance namespace.
// Returns ("", nil) to signal "no project, skip quota". Returns ("", err) for
// transient failures that should cause a reconcile requeue.
func (r *InstanceReconciler) resolveProjectNamespace(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) (string, error) {
	if r.projectNamespaceForInstance != nil {
		return r.projectNamespaceForInstance(ctx, clusterName, instance)
	}
	return instance.Namespace, nil
}

// resolveClusterNameForProject returns the multicluster ClusterName for the
// given project ID. When clusterNameForProject is set it delegates to that
// function; otherwise it falls back to multicluster.ClusterName(projectID),
// which is correct for Milo-mode deployments where the cluster name IS the
// project name.
func (r *InstanceReconciler) resolveClusterNameForProject(projectID string) multicluster.ClusterName {
	if r.clusterNameForProject != nil {
		return r.clusterNameForProject(projectID)
	}
	return multicluster.ClusterName(projectID)
}

// SetupWithManager sets up the controller with the Manager.
//
// quotaRestConfig is the REST config used to reach Milo project control planes
// for ResourceClaim management. Pass nil to disable quota accounting.
//
// projectIDForInstance derives the Milo project ID for each reconcile request.
// In Milo mode pass nil (falls back to using ClusterName). In single-cell mode
// pass a function that returns instance.Namespace.
//
// clusterNameForProject maps a project ID back to the multicluster ClusterName.
// In Milo mode pass nil (falls back to ClusterName(projectID)). In single-cell
// mode pass a function that always returns "single".
func (r *InstanceReconciler) SetupWithManager(
	mgr mcmanager.Manager,
	quotaRestConfig *rest.Config,
	projectIDForInstance InstanceProjectIDFunc,
	projectNamespaceForInstance InstanceProjectNamespaceFunc,
	edgeClusterName string,
	clusterNameForProject func(projectID string) multicluster.ClusterName,
) error {
	r.mgr = mgr
	r.scheme = mgr.GetLocalManager().GetScheme()
	//nolint:staticcheck // GetEventRecorder (new events API) has an incompatible Eventf
	// signature (requires related object + action args) that would require migrating
	// all emit sites. GetEventRecorderFor remains correct; migration is deferred.
	r.recorder = mgr.GetLocalManager().GetEventRecorderFor("instance-controller")
	r.edgeClusterName = edgeClusterName
	r.projectIDForInstance = projectIDForInstance
	r.projectNamespaceForInstance = projectNamespaceForInstance
	r.clusterNameForProject = clusterNameForProject
	if quotaRestConfig != nil {
		if edgeClusterName == "" {
			return fmt.Errorf("edgeClusterName must be set when quota enforcement is enabled; set discovery.clusterName in the server config")
		}
		r.quotaClientManager = quotametrics.New(quotaRestConfig)
	}

	r.finalizers = finalizer.NewFinalizers()
	if err := r.finalizers.Register(instanceControllerFinalizer, r); err != nil {
		return fmt.Errorf("failed to register finalizer: %w", err)
	}

	edgeClusterNameVal := r.edgeClusterName

	return mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.Instance{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Watches(
			&quotav1alpha1.ResourceClaim{},
			func(_ multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
				return handler.TypedEnqueueRequestsFromMapFunc(
					func(ctx context.Context, obj client.Object) []mcreconcile.Request {
						claim := obj.(*quotav1alpha1.ResourceClaim)
						// Map the claim back to its owning Instance. The Instance
						// namespace is carried on a label (the claim itself lives in
						// the project's quota namespace) and the Instance name is the
						// claim name with the resource-kind prefix stripped.
						instanceNamespace := claim.GetLabels()[instanceQuotaClaimNamespaceLabel]
						if instanceNamespace == "" {
							return nil
						}
						return []mcreconcile.Request{
							{
								Request: reconcile.Request{
									NamespacedName: types.NamespacedName{
										Namespace: instanceNamespace,
										Name:      strings.TrimPrefix(claim.Name, instanceQuotaClaimNamePrefix),
									},
								},
								ClusterName: r.resolveClusterNameForProject(claim.Spec.ConsumerRef.Name),
							},
						}
					},
				)
			},
			mcbuilder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetLabels()[instanceQuotaClaimSourceLabel] == edgeClusterNameVal
			})),
		).
		Complete(r)
}
