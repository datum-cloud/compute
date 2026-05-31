// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

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
	"go.datum.net/compute/internal/referenceddata"
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

	// msgInstanceAvailable is the human-readable message for the available state.
	msgInstanceAvailable = "Instance is available"

	// reasonNetworkFailedToCreate is the reason code for network creation failure.
	reasonNetworkFailedToCreate = "NetworkFailedToCreate"
)

// instanceTypeD1Standard2 is the platform instance type name for the
// 1 vCPU / 2 GiB size used as the catalog baseline for quota accounting.
const instanceTypeD1Standard2 = "datumcloud/d1-standard-2"

// instanceTypeResources holds the vCPU and memory for a named instance type.
type instanceTypeResources struct {
	// CPUMillicores is the number of CPU millicores (1000 = 1 vCPU).
	CPUMillicores int64
	// MemoryMiB is the amount of RAM in mebibytes.
	MemoryMiB int64
}

// instanceTypeCatalog maps platform instance type names to their resource
// dimensions used for quota accounting when the instance spec carries only an
// instanceType and no explicit container Limits or instance-level Requests.
//
// These are the platform-declared quota sizes for the instance type, not a
// derivation of any infra provider's machine type. (infra-provider-gcp separately
// maps datumcloud/d1-standard-2 to the GCP n2-standard-2 machine type for VM
// provisioning; that mapping does not define the quota size here.) When new
// instance types are added, add them here with their vCPU/memory values.
var instanceTypeCatalog = map[string]instanceTypeResources{
	instanceTypeD1Standard2: {
		CPUMillicores: 1000, // 1 vCPU
		MemoryMiB:     2048, // 2 GiB
	},
}

// Quota-pending requeue backoff. The instance controller is normally re-queued by
// the ResourceClaim watch when a claim is granted, but that grant event lives on
// the project control plane and can be missed (informer engagement races, watch
// relist gaps), wedging the instance at QuotaGranted!=True indefinitely. While
// quota is pending we requeue on a backing-off schedule as a safety net so a
// missed grant self-heals. The interval lengthens the longer the instance waits:
//
//	elapsed < 60s : every 1s   (catch a grant landing almost immediately)
//	60s – 5m      : every 15s
//	5m – 10m      : every 60s
//	>= 10m        : every 300s
const (
	quotaPendingRequeueFast   = 1 * time.Second
	quotaPendingRequeueMedium = 15 * time.Second
	quotaPendingRequeueSlow   = 60 * time.Second
	quotaPendingRequeueIdle   = 300 * time.Second

	quotaPendingFastWindow   = 60 * time.Second
	quotaPendingMediumWindow = 5 * time.Minute
	quotaPendingSlowWindow   = 10 * time.Minute
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
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

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

	// Safety-net requeue while quota is not yet granted, computed up front so
	// every return path below honors it. A conflict during the pending window
	// must not drop the instance onto controller-runtime's exponential
	// error-backoff (which can stretch to minutes), which would defeat recovery
	// from a missed ResourceClaim grant event. Logged so the requeue is
	// observable: a re-firing requeue prints this every pass while pending.
	quotaReq := quotaPendingRequeueAfter(&instance, time.Now())
	if quotaReq > 0 {
		logger.Info("quota pending; scheduling safety-net requeue",
			"after", quotaReq.String(), "cluster", req.ClusterName.String(), "instance", instance.Name)
	}

	// Even when reconcileQuotaCondition returns a transient error, persist any
	// condition change first so the failure reason is visible on the Instance.
	// We return the error afterwards so controller-runtime requeues with backoff.

	// Reconcile the ReferencedData condition: diff expected companions against
	// those present on the cell. The result tells us whether the gate may be
	// cleared in the spec-patch pass below.
	refDataResult, requeueAfter, err := r.reconcileReferencedDataCondition(ctx, cl.GetClient(), &instance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed reconciling referenced data condition: %w", err)
	}
	statusChanged = refDataResult.conditionChanged || statusChanged

	readyChanged, err := r.reconcileInstanceReadyCondition(ctx, cl.GetClient(), &instance, r.checkForNetworkCreationFailure)
	if err != nil {
		return ctrl.Result{}, err
	}

	if statusChanged || readyChanged {
		if err := cl.GetClient().Status().Update(ctx, &instance); err != nil {
			if quotaReq > 0 && apierrors.IsConflict(err) {
				logger.Info("status update conflicted while quota pending; requeuing instead of error-backoff",
					"after", quotaReq.String(), "instance", instance.Name)
				return ctrl.Result{RequeueAfter: quotaReq}, nil
			}
			return ctrl.Result{}, err
		}
		// Return with the quota error (nil or transient) so controller-runtime
		// requeues with backoff on failures. On the success path (quotaErr==nil)
		// we fall through to reconcileSchedulingGates below instead of returning
		// early, so gates are cleared in the same reconcile pass rather than
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

	// Spec-patch pass: remove scheduling gates for conditions that are now
	// persisted as True. Handles both the Quota gate and the ReferencedData gate
	// in a single patch to avoid duplicate API calls.
	if err := r.reconcileSchedulingGates(ctx, cl.GetClient(), &instance); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.writeBackToUpstream(ctx, req.ClusterName, &instance); err != nil {
		if quotaReq > 0 && apierrors.IsConflict(err) {
			logger.Info("upstream writeback conflicted while quota pending; requeuing instead of error-backoff",
				"after", quotaReq.String(), "instance", instance.Name)
			return ctrl.Result{RequeueAfter: quotaReq}, nil
		}
		return ctrl.Result{}, err
	}

	// Honor both the quota safety-net requeue and the referenced-data resolver
	// requeue: pick the soonest non-zero deadline so neither pending wait is
	// dropped.
	effectiveRequeue := quotaReq
	if requeueAfter > 0 && (effectiveRequeue == 0 || requeueAfter < effectiveRequeue) {
		effectiveRequeue = requeueAfter
	}
	if effectiveRequeue > 0 {
		logger.Info("requeuing instance", "after", effectiveRequeue.String(),
			"cluster", req.ClusterName.String(), "instance", instance.Name)
	}

	return ctrl.Result{RequeueAfter: effectiveRequeue}, nil
}

// reconcileSchedulingGates removes scheduling gates whose corresponding
// conditions have been persisted as True. It handles both the Quota gate and
// the ReferencedData gate in a single patch to avoid duplicate API calls.
//
// Both gates are guarded by ObservedGeneration == instance.Generation to
// prevent a stale True condition from generation N unblocking a generation N+1
// instance before quota/referenced-data for the new spec has been re-evaluated.
func (r *InstanceReconciler) reconcileSchedulingGates(
	ctx context.Context,
	cl client.Client,
	instance *computev1alpha.Instance,
) error {
	if instance.Spec.Controller == nil || len(instance.Spec.Controller.SchedulingGates) == 0 {
		return nil
	}

	quotaGrantedCond := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceQuotaGranted)
	refDataCond := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.ReferencedDataReady)

	newGates := make([]computev1alpha.SchedulingGate, 0, len(instance.Spec.Controller.SchedulingGates))
	gatesRemoved := false
	for _, gate := range instance.Spec.Controller.SchedulingGates {
		// Guard on ObservedGeneration to prevent a stale True condition from a
		// prior generation clearing a freshly-stamped gate before the condition
		// has been re-evaluated at the current generation.
		removeQuota := gate.Name == instancecontrol.QuotaSchedulingGate.String() &&
			quotaGrantedCond != nil && quotaGrantedCond.Status == metav1.ConditionTrue &&
			quotaGrantedCond.ObservedGeneration == instance.Generation
		removeRefData := gate.Name == instancecontrol.ReferencedDataSchedulingGate.String() &&
			refDataCond != nil && refDataCond.Status == metav1.ConditionTrue &&
			refDataCond.ObservedGeneration == instance.Generation
		if removeQuota || removeRefData {
			gatesRemoved = true
			// Observe gate-wait duration and emit event when the ReferencedData
			// gate clears.
			if removeRefData {
				r.observeGateWaitDuration(instance)
				r.emitReferencedDataClearedEvent(instance)
			}
			continue
		}
		newGates = append(newGates, gate)
	}
	if gatesRemoved {
		patch := client.MergeFrom(instance.DeepCopy())
		instance.Spec.Controller.SchedulingGates = newGates
		if err := cl.Patch(ctx, instance, patch); err != nil {
			return fmt.Errorf("failed patching scheduling gates: %w", err)
		}
	}
	return nil
}

// referencedDataResult carries outputs of reconcileReferencedDataCondition.
type referencedDataResult struct {
	// conditionChanged is true when the ReferencedDataReady condition was updated.
	conditionChanged bool
}

// reconcileReferencedDataCondition checks whether all expected companion
// ConfigMaps/Secrets are present on the cell for the given instance.
//
// It reads the expected-set annotation from the owning WorkloadDeployment,
// lists labeled companions in the namespace, and computes the diff.
//
//   - Annotation absent (resolver not yet finished): Resolving / Unknown, requeueAfter.
//   - Expected set present but some companions missing: AwaitingPropagation / False.
//   - All companions present: Ready / True (gate cleared by caller).
//
// Returns a requeueAfter duration when the annotation is not yet stamped on the
// WD, so the instance reconciler retries even without a watch event.
func (r *InstanceReconciler) reconcileReferencedDataCondition(
	ctx context.Context,
	cl client.Client,
	instance *computev1alpha.Instance,
) (referencedDataResult, time.Duration, error) {
	// If the instance does not carry the ReferencedData gate there is nothing to
	// reconcile here — skip silently.
	hasGate := false
	if instance.Spec.Controller != nil {
		for _, g := range instance.Spec.Controller.SchedulingGates {
			if g.Name == instancecontrol.ReferencedDataSchedulingGate.String() {
				hasGate = true
				break
			}
		}
	}
	if !hasGate {
		// Gate already gone — ensure condition is Ready if the gate was just
		// cleared on a previous pass (idempotent).
		existing := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.ReferencedDataReady)
		if existing == nil {
			// Gate never present; nothing to do.
			return referencedDataResult{}, 0, nil
		}
		if existing.Status == metav1.ConditionTrue {
			// Already marked ready — nothing to do.
			return referencedDataResult{}, 0, nil
		}
		// Gate is absent but condition is not True. Only self-heal to True when we
		// can confirm that all companions are actually present — fetching the WD
		// annotation to validate. If the annotation is absent (resolver hasn't
		// finished yet) or companions are still missing, leave the condition alone
		// rather than falsely reporting Ready.
		wd, err := r.fetchOwnerWorkloadDeployment(ctx, cl, instance)
		if err != nil {
			return referencedDataResult{}, 0, err
		}
		annoRaw, hasAnno := wd.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]
		if !hasAnno || annoRaw == "" {
			// Resolver hasn't stamped the annotation yet — cannot confirm readiness.
			return referencedDataResult{}, 0, nil
		}
		var expectedNames []string
		if err := json.Unmarshal([]byte(annoRaw), &expectedNames); err != nil {
			// Malformed annotation — cannot confirm readiness.
			return referencedDataResult{}, 0, nil
		}
		if len(expectedNames) > 0 {
			presentNames, err := r.listPresentCompanionNames(ctx, cl, instance.Namespace)
			if err != nil {
				return referencedDataResult{}, 0, fmt.Errorf("failed listing companion objects during self-heal check: %w", err)
			}
			for _, name := range expectedNames {
				if _, ok := presentNames[name]; !ok {
					// At least one companion is missing — do not self-heal to True.
					return referencedDataResult{}, 0, nil
				}
			}
		}
		// All companions confirmed present (or none expected) — mark ready.
		changed := apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.ReferencedDataReady,
			Status:             metav1.ConditionTrue,
			Reason:             computev1alpha.ReferencedDataReasonReady,
			Message:            "All referenced companions are available",
			ObservedGeneration: instance.Generation,
		})
		return referencedDataResult{conditionChanged: changed}, 0, nil
	}

	// Stamp the gate-start annotation once so we can measure duration later.
	r.stampGateStartAnnotation(ctx, cl, instance)

	// Fetch the owning WorkloadDeployment to read the expected-set annotation.
	wd, err := r.fetchOwnerWorkloadDeployment(ctx, cl, instance)
	if err != nil {
		return referencedDataResult{}, 0, err
	}

	// Annotation not yet present — the resolver hasn't finished. Signal Resolving
	// and requeue so we re-check even without a new watch event.
	annoRaw, hasAnno := wd.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]
	if !hasAnno || annoRaw == "" {
		changed := r.setReferencedDataCondition(instance, metav1.ConditionUnknown,
			computev1alpha.ReferencedDataReasonResolving,
			"Waiting for the resolver to finish reading source objects")
		return referencedDataResult{conditionChanged: changed}, 5 * time.Second, nil
	}

	// Decode the expected companion names.
	var expectedNames []string
	if err := json.Unmarshal([]byte(annoRaw), &expectedNames); err != nil {
		// Malformed annotation — treat like absent; the resolver will fix it.
		changed := r.setReferencedDataCondition(instance, metav1.ConditionUnknown,
			computev1alpha.ReferencedDataReasonResolving,
			"expected-referenced-data annotation is malformed; waiting for resolver")
		return referencedDataResult{conditionChanged: changed}, 5 * time.Second, nil
	}

	// No companions expected (WD template has no references) — clear the gate.
	if len(expectedNames) == 0 {
		changed := r.setReferencedDataCondition(instance, metav1.ConditionTrue,
			computev1alpha.ReferencedDataReasonReady,
			"No referenced companions required")
		return referencedDataResult{conditionChanged: changed}, 0, nil
	}

	// List labeled companions in the instance's namespace.
	presentNames, err := r.listPresentCompanionNames(ctx, cl, instance.Namespace)
	if err != nil {
		return referencedDataResult{}, 0, fmt.Errorf("failed listing companion objects: %w", err)
	}

	// Diff: find which expected companions are missing.
	var missing []string
	for _, name := range expectedNames {
		if _, ok := presentNames[name]; !ok {
			missing = append(missing, name)
		}
	}

	// Update metrics (aggregated per namespace to avoid high-cardinality per-instance series).
	present := len(expectedNames) - len(missing)
	referenceddata.CompanionsExpected.WithLabelValues(instance.Namespace).Set(float64(len(expectedNames)))
	referenceddata.CompanionsPresent.WithLabelValues(instance.Namespace).Set(float64(present))

	if len(missing) > 0 {
		msg := fmt.Sprintf("Waiting for %d companion(s) to arrive on cell: %s",
			len(missing), strings.Join(missing, ", "))
		// Capture the previous reason before the condition is updated, so we can
		// determine whether the reason has transitioned. We emit a Warning event
		// only on a reason transition (not on every reconcile where the missing-set
		// message changes) to avoid event floods.
		prevCond := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.ReferencedDataReady)
		prevReason := ""
		if prevCond != nil {
			prevReason = prevCond.Reason
		}
		changed := r.setReferencedDataConditionWithTransition(instance, metav1.ConditionFalse,
			computev1alpha.ReferencedDataReasonAwaitingPropagation, msg)
		if prevReason != computev1alpha.ReferencedDataReasonAwaitingPropagation {
			r.emitEvent(instance, corev1.EventTypeWarning,
				computev1alpha.ReferencedDataReasonAwaitingPropagation, msg)
		}
		return referencedDataResult{conditionChanged: changed}, 0, nil
	}

	// All companions present.
	changed := r.setReferencedDataConditionWithTransition(instance, metav1.ConditionTrue,
		computev1alpha.ReferencedDataReasonReady,
		fmt.Sprintf("All %d referenced companion(s) are present on cell", len(expectedNames)))
	return referencedDataResult{conditionChanged: changed}, 0, nil
}

// setReferencedDataCondition sets the ReferencedDataReady condition and returns
// whether it changed.
func (r *InstanceReconciler) setReferencedDataCondition(
	instance *computev1alpha.Instance,
	status metav1.ConditionStatus,
	reason, message string,
) bool {
	return apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
		Type:               computev1alpha.ReferencedDataReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: instance.Generation,
	})
}

// setReferencedDataConditionWithTransition sets the condition and, when it
// changed, increments the condition-transition metric.
func (r *InstanceReconciler) setReferencedDataConditionWithTransition(
	instance *computev1alpha.Instance,
	status metav1.ConditionStatus,
	reason, message string,
) bool {
	prev := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.ReferencedDataReady)
	fromReason := "none"
	if prev != nil {
		fromReason = prev.Reason
	}
	changed := r.setReferencedDataCondition(instance, status, reason, message)
	if changed && fromReason != reason {
		referenceddata.ConditionTransitions.WithLabelValues(instance.Namespace, fromReason, reason).Inc()
	}
	return changed
}

// listPresentCompanionNames returns a set (map[name]struct{}) of companion
// ConfigMap and Secret names present in the given namespace (matched by the
// ReferencedDataLabel).
func (r *InstanceReconciler) listPresentCompanionNames(
	ctx context.Context,
	cl client.Client,
	namespace string,
) (map[string]struct{}, error) {
	labelSel := client.MatchingLabels{computev1alpha.ReferencedDataLabel: "true"}
	inNs := client.InNamespace(namespace)

	names := make(map[string]struct{})

	var cmList corev1.ConfigMapList
	if err := cl.List(ctx, &cmList, inNs, labelSel); err != nil {
		return nil, fmt.Errorf("list companion ConfigMaps: %w", err)
	}
	for _, cm := range cmList.Items {
		names[cm.Name] = struct{}{}
	}

	var secretList corev1.SecretList
	if err := cl.List(ctx, &secretList, inNs, labelSel); err != nil {
		return nil, fmt.Errorf("list companion Secrets: %w", err)
	}
	for _, s := range secretList.Items {
		names[s.Name] = struct{}{}
	}

	return names, nil
}

// fetchOwnerWorkloadDeployment retrieves the WorkloadDeployment that owns the
// given instance. It is shared by the network-failure check and the
// referenced-data condition reconciler to avoid duplicate fetches; callers
// that already hold the WD should not call this a second time.
func (r *InstanceReconciler) fetchOwnerWorkloadDeployment(
	ctx context.Context,
	cl client.Client,
	instance *computev1alpha.Instance,
) (*computev1alpha.WorkloadDeployment, error) {
	ownerRef := metav1.GetControllerOf(instance)
	if ownerRef == nil {
		return nil, fmt.Errorf("instance %s/%s has no controller owner reference", instance.Namespace, instance.Name)
	}
	var wd computev1alpha.WorkloadDeployment
	if err := cl.Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: ownerRef.Name}, &wd); err != nil {
		return nil, fmt.Errorf("failed fetching owning WorkloadDeployment %q: %w", ownerRef.Name, err)
	}
	return &wd, nil
}

// stampGateStartAnnotation records the RFC3339 time at which the ReferencedData
// gate was first observed. It is a best-effort, no-op if the annotation is
// already present or the patch fails.
//
// The status conditions on instance are preserved across the patch: the fake
// client (and real API server) does not touch status in a metadata-only Patch,
// but the controller-runtime fake client zeroes and re-unmarshals instance from
// the server response, which would lose any in-memory status changes not yet
// persisted via Status().Update(). We save and restore them explicitly.
func (r *InstanceReconciler) stampGateStartAnnotation(
	ctx context.Context,
	cl client.Client,
	instance *computev1alpha.Instance,
) {
	if instance.Annotations != nil {
		if _, ok := instance.Annotations[computev1alpha.ReferencedDataGateStartAnnotation]; ok {
			return
		}
	}
	// Preserve in-memory status so the Patch does not discard conditions that
	// have been set by earlier reconcile steps but not yet persisted to the
	// API server via Status().Update().
	savedStatus := instance.Status.DeepCopy()

	patch := client.MergeFrom(instance.DeepCopy())
	if instance.Annotations == nil {
		instance.Annotations = make(map[string]string)
	}
	instance.Annotations[computev1alpha.ReferencedDataGateStartAnnotation] = time.Now().UTC().Format(time.RFC3339)
	if err := cl.Patch(ctx, instance, patch); err != nil {
		log.FromContext(ctx).V(1).Info("could not stamp gate-start annotation (non-fatal)", "error", err)
	}

	// Restore the in-memory status after the patch overwrites it with the
	// server-side state.
	instance.Status = *savedStatus
}

// observeGateWaitDuration records the gate-wait histogram when the
// ReferencedData gate is being cleared. It reads the start annotation stamped
// when the gate was first observed.
func (r *InstanceReconciler) observeGateWaitDuration(instance *computev1alpha.Instance) {
	if instance.Annotations == nil {
		return
	}
	startStr, ok := instance.Annotations[computev1alpha.ReferencedDataGateStartAnnotation]
	if !ok {
		return
	}
	startTime, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		return
	}
	elapsed := time.Since(startTime).Seconds()
	referenceddata.GateWaitDuration.WithLabelValues(instance.Namespace).Observe(elapsed)
}

// emitReferencedDataClearedEvent records a Normal event on the instance when
// the ReferencedData gate is cleared.
func (r *InstanceReconciler) emitReferencedDataClearedEvent(instance *computev1alpha.Instance) {
	r.emitEvent(instance, corev1.EventTypeNormal,
		computev1alpha.ReferencedDataReasonReady,
		"All referenced companion ConfigMaps/Secrets are present; ReferencedData gate cleared")
}

// emitEvent emits a Kubernetes event if a recorder is available. Guard against
// a nil recorder so that unit tests that don't wire up a recorder don't panic.
func (r *InstanceReconciler) emitEvent(obj *computev1alpha.Instance, eventType, reason, message string) {
	if r.recorder == nil {
		return
	}
	r.recorder.Event(obj, eventType, reason, message)
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

// quotaPendingRequeueAfter returns a safety-net requeue interval while the
// instance's quota is not yet granted, backing off the longer it has waited (see
// the quotaPendingRequeue* constants). It returns 0 when quota is already granted
// (QuotaGranted=True) or the condition is absent, so a granted/normal instance is
// not needlessly requeued.
//
// Elapsed time is anchored on the instance's creation timestamp, NOT the
// QuotaGranted condition's LastTransitionTime: while quota is pending the
// condition stays Unknown (PendingEvaluation and NoBudget are both Unknown), so
// SetStatusCondition never bumps LastTransitionTime off its 1970-01-01 CRD
// default — which would peg every pending instance to the slowest tier.
func quotaPendingRequeueAfter(instance *computev1alpha.Instance, now time.Time) time.Duration {
	cond := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceQuotaGranted)
	if cond == nil || cond.Status == metav1.ConditionTrue {
		return 0
	}
	elapsed := now.Sub(instance.CreationTimestamp.Time)
	switch {
	case elapsed < quotaPendingFastWindow:
		return quotaPendingRequeueFast
	case elapsed < quotaPendingMediumWindow:
		return quotaPendingRequeueMedium
	case elapsed < quotaPendingSlowWindow:
		return quotaPendingRequeueSlow
	default:
		return quotaPendingRequeueIdle
	}
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

// resolveInstanceResources determines the vCPU and memory amounts to claim
// for an instance. Explicit sizing always takes precedence over the instance
// type catalog, so a workload that overrides container limits is accounted at
// its actual resource footprint rather than the catalog baseline.
//
// Precedence order:
//  1. Sandbox container Limits (sum across all containers) — all containers
//     must have both cpu and memory Limits for this path to succeed.
//  2. Instance-level Resources.Requests — both cpu and memory must be present.
//  3. instanceTypeCatalog lookup by instanceType — used for the common case
//     where a workload is sized only by instanceType with no explicit limits.
//
// Returns (0, 0, false) when none of the above yield a complete sizing, so
// the caller falls back to claiming only the instance count.
func resolveInstanceResources(instance *computev1alpha.Instance) (cpuMillicores int64, memMiB int64, resolved bool) {
	rt := instance.Spec.Runtime

	// Path 1: explicit per-container Limits — most specific, wins if fully set.
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
		if allSet && len(rt.Sandbox.Containers) > 0 {
			return totalCPU.MilliValue(), totalMem.Value() / (1024 * 1024), true
		}
		// Containers exist but limits are incomplete — fall through to catalog
		// rather than returning false, because instanceType is still set.
	}

	// Path 2: instance-level resource requests.
	cpu, hasCPU := rt.Resources.Requests[corev1.ResourceCPU]
	mem, hasMem := rt.Resources.Requests[corev1.ResourceMemory]
	if hasCPU && hasMem {
		return cpu.MilliValue(), mem.Value() / (1024 * 1024), true
	}

	// Path 3: instanceType catalog — handles the typical production case where
	// instanceType is the only sizing signal and no explicit limits are set.
	if rt.Resources.InstanceType != "" {
		if spec, ok := instanceTypeCatalog[rt.Resources.InstanceType]; ok {
			return spec.CPUMillicores, spec.MemoryMiB, true
		}
	}

	return 0, 0, false
}

// instanceBlockingReasonPriority ranks Instance blocking reasons so the most
// specific, user-actionable cause wins when several conditions are unsatisfied.
// Higher numbers are more specific. Reasons absent from the table rank 0.
//
//	0 - unknown/default
//	1 - Provisioning              (transient runtime startup)
//	3 - PendingQuota              (operator action may be needed)
//	5 - ImageUnavailable / InstanceCrashing / ConfigurationError
//	    (hard runtime error, user-actionable)
//	7 - NetworkFailedToCreate     (hard infra error)
func instanceBlockingReasonPriority(reason string) int {
	switch reason {
	case computev1alpha.InstanceReadyReasonProvisioning:
		return 1
	case computev1alpha.InstanceProgrammedReasonPendingQuota:
		return 3
	case computev1alpha.InstanceReadyReasonImageUnavailable,
		computev1alpha.InstanceReadyReasonInstanceCrashing,
		computev1alpha.InstanceReadyReasonConfigurationError:
		// Hard runtime errors are user-actionable (wrong image, crashing app, bad
		// config) and rank highest among non-infra reasons so they are not buried
		// under transient startup/quota reasons.
		return 5
	case reasonNetworkFailedToCreate:
		return 7
	default:
		return 0
	}
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
			Type:               computev1alpha.InstanceAvailable,
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

		// Surface the most specific provider sub-condition rather than a generic
		// "Instance has not been programmed". A provider reason like
		// ImageUnavailable (set on the Available condition while Programmed is
		// still Unknown) must surface on Ready with its actionable message.
		//
		// Two tiers are tracked:
		//   - bestKnown: the best candidate from the priority table (ranked 1-7).
		//   - fallback:  the Programmed condition's own reason/message when it has
		//                one but it is not in the priority table (e.g. a provider
		//                writes a custom Programmed reason otherwise unknown to
		//                this controller). Preserves Programmed.Reason → Ready.Reason
		//                pass-through behavior.
		type candidate struct {
			status   metav1.ConditionStatus
			reason   string
			message  string
			priority int
		}

		// Generic default — used only when nothing better is found.
		fallbackCandidate := candidate{
			status:   metav1.ConditionFalse,
			reason:   computev1alpha.InstanceProgrammedReasonPendingProgramming,
			message:  msgNotProgrammed,
			priority: -1,
		}
		// Promote the Programmed condition's own reason as a fallback when it is
		// more specific than PendingProgramming/Pending but not in the priority
		// table. Preserves pass-through for provider-written Programmed reasons.
		if programmedCondition != nil && programmedCondition.Reason != pendingReason &&
			programmedCondition.Reason != computev1alpha.InstanceProgrammedReasonPendingProgramming {
			fallbackCandidate = candidate{
				status:   programmedCondition.Status,
				reason:   programmedCondition.Reason,
				message:  programmedCondition.Message,
				priority: 0,
			}
		}

		best := fallbackCandidate
		consider := func(status metav1.ConditionStatus, reason, message string) {
			// A generic "Pending" reason carries no actionable signal; skip it so
			// it cannot displace an already-set specific reason from the provider.
			if reason == pendingReason {
				return
			}
			p := instanceBlockingReasonPriority(reason)
			if p > best.priority {
				best = candidate{status: status, reason: reason, message: message, priority: p}
			}
		}

		// Sub-conditions set by the provider (e.g. Available=Unknown/ImageUnavailable)
		// may be more specific than the Programmed condition. Consult each one so
		// the highest-priority reason wins, regardless of which condition carries it.
		for _, cond := range instance.Status.Conditions {
			if cond.Status == metav1.ConditionTrue {
				// Satisfied conditions are not blocking; skip them.
				continue
			}
			switch cond.Type {
			case computev1alpha.InstanceProgrammed,
				computev1alpha.InstanceReady,
				computev1alpha.InstanceQuotaGranted:
				// InstanceProgrammed is handled below; InstanceReady is being set
				// now. InstanceQuotaGranted is a gate-level signal evaluated before
				// this branch is reached — including it here would let a transient
				// PendingEvaluation reason displace the generic not-programmed
				// fallback when no provider sub-condition is set yet.
				continue
			}
			consider(cond.Status, cond.Reason, cond.Message)
		}
		// Also let the Programmed condition itself compete through the priority table
		// in case it carries a known reason (e.g. PendingQuota).
		if programmedCondition != nil {
			consider(programmedCondition.Status, programmedCondition.Reason, programmedCondition.Message)
		}

		readyCondition.Status = best.status
		readyCondition.Reason = best.reason
		readyCondition.Message = best.message

		return apimeta.SetStatusCondition(&instance.Status.Conditions, *readyCondition), nil
	}

	logger.Info("instance is programmed", "instance", instance.Name)

	availableCondition := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceAvailable)
	if availableCondition == nil || availableCondition.Status != metav1.ConditionTrue {
		logger.Info("instance is not available", "instance", instance.Name)

		// Propagate the Available condition's reason and message directly —
		// including when the status is Unknown — so provider-set reasons like
		// ImageUnavailable surface on Ready rather than a generic message.
		readyStatus := metav1.ConditionFalse
		readyReason := pendingReason
		readyMessage := "Instance is not available"
		if availableCondition != nil && availableCondition.Reason != pendingReason {
			readyStatus = availableCondition.Status
			readyReason = availableCondition.Reason
			readyMessage = availableCondition.Message
		}

		readyCondition.Status = readyStatus
		readyCondition.Reason = readyReason
		readyCondition.Message = readyMessage

		return apimeta.SetStatusCondition(&instance.Status.Conditions, *readyCondition), nil
	}

	readyCondition.Status = metav1.ConditionTrue
	readyCondition.Reason = computev1alpha.InstanceReadyReasonAvailable
	readyCondition.Message = msgInstanceReady

	return apimeta.SetStatusCondition(&instance.Status.Conditions, *readyCondition), nil
}

// Rough way to propagate creation errors up to the instance as soon as possible.
// Lots of room for improvement here.
func (r *InstanceReconciler) checkForNetworkCreationFailure(ctx context.Context, upstreamClient client.Client, instance *computev1alpha.Instance) (failed bool, message string, err error) {
	workloadDeployment, err := r.fetchOwnerWorkloadDeployment(ctx, upstreamClient, instance)
	if err != nil {
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
		// Watch companion ConfigMaps: when one arrives (or is updated) re-queue all
		// Instances in the namespace so they can attempt gate-clearing.
		Watches(&corev1.ConfigMap{}, func(clusterName multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
			return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []mcreconcile.Request {
				if obj.GetLabels()[computev1alpha.ReferencedDataLabel] != "true" {
					return nil
				}
				return enqueueInstancesInNamespace(ctx, cl.GetClient(), string(clusterName), obj.GetNamespace())
			})
		}).
		// Watch companion Secrets for the same reason.
		Watches(&corev1.Secret{}, func(clusterName multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
			return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []mcreconcile.Request {
				if obj.GetLabels()[computev1alpha.ReferencedDataLabel] != "true" {
					return nil
				}
				return enqueueInstancesInNamespace(ctx, cl.GetClient(), string(clusterName), obj.GetNamespace())
			})
		}).
		Complete(r)
}

// enqueueInstancesInNamespace returns reconcile requests for every Instance
// in the given namespace. Companions are shared across all Instances in a
// namespace, so any gated instance in the namespace should re-check when a
// companion ConfigMap or Secret arrives.
func enqueueInstancesInNamespace(
	ctx context.Context,
	cl client.Client,
	clusterName, namespace string,
) []mcreconcile.Request {
	logger := log.FromContext(ctx)

	var instanceList computev1alpha.InstanceList
	if err := cl.List(ctx, &instanceList, client.InNamespace(namespace)); err != nil {
		logger.Error(err, "failed listing instances for companion watch", "namespace", namespace)
		return nil
	}

	requests := make([]mcreconcile.Request, 0, len(instanceList.Items))
	for _, inst := range instanceList.Items {
		requests = append(requests, mcreconcile.Request{
			Request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: inst.Namespace,
					Name:      inst.Name,
				},
			},
			ClusterName: multicluster.ClusterName(clusterName),
		})
	}
	return requests
}
