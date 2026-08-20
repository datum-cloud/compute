// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
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

	"go.datum.net/compute/internal/controller/instancecontrol"
	instancecontrolstateful "go.datum.net/compute/internal/controller/instancecontrol/stateful"
)

const (
	// reasonReplicasAvailable is used in the ReplicasReady condition when replicas
	// are either available or pending; it has no equivalent in the API constants
	// package because it is an internal detail of this controller.
	reasonReplicasAvailable = "ReplicasAvailable"
)

// WorkloadDeploymentReconciler reconciles a WorkloadDeployment object
type WorkloadDeploymentReconciler struct {
	mgr        mcmanager.Manager
	finalizers finalizer.Finalizers

	// NetworkingEnabled controls whether the networking integration with
	// network-services-operator is active. When false, interface claim creation
	// is skipped, the Network scheduling gate is never added to Instances (and is
	// actively removed if present), and the networking step is treated as
	// immediately ready. Defaults to false.
	NetworkingEnabled bool

	// enableReferencedDataGate mirrors FeatureFlagsConfig.EnableReferencedDataGate.
	// When true, new Instances whose template references ConfigMaps or Secrets
	// receive the ReferencedData scheduling gate at creation time.
	enableReferencedDataGate bool
}

func effectiveDesiredReplicas(deployment *computev1alpha.WorkloadDeployment) int32 {
	if !deployment.DeletionTimestamp.IsZero() {
		return 0
	}
	if deployment.Spec.Replicas != nil {
		return *deployment.Spec.Replicas
	}
	return deployment.Spec.ScaleSettings.MinReplicas
}

func workloadDeploymentPodSelector(deployment *computev1alpha.WorkloadDeployment) string {
	set := labels.Set{computev1alpha.WorkloadDeploymentUIDLabel: string(deployment.GetUID())}
	return set.AsSelector().String()
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=servinglocations,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=networkinterfaceclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=networkinterfaces,verbs=get;list;watch
// The management-mode WorkloadReconciler watches Networks. Declare the grant as
// a marker so regenerating the role keeps it.
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=networks,verbs=get;list;watch

func (r *WorkloadDeploymentReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	ctx = mccontext.WithCluster(ctx, req.ClusterName)

	var deployment computev1alpha.WorkloadDeployment
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	finalizationResult, err := r.finalizers.Finalize(ctx, &deployment)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to finalize: %w", err)
	}
	if finalizationResult.Updated {
		if err = cl.GetClient().Update(ctx, &deployment); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update based on finalization result: %w", err)
		}
		// The finalizer-add Update is metadata-only and may be filtered by event
		// predicates or handlers, so requeue explicitly to guarantee the
		// deployment is reconciled past this point.
		return ctrl.Result{Requeue: true}, nil
	}

	if !deployment.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if deployment.Spec.Replicas == nil {
		base := deployment.DeepCopy()
		deployment.Spec.Replicas = new(deployment.Spec.ScaleSettings.MinReplicas)
		if err := cl.GetClient().Patch(ctx, &deployment, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed initializing deployment replicas: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	logger.Info("reconciling deployment")
	defer logger.Info("reconcile complete")

	// Snapshot the existing status before any modifications so we can skip the
	// Status().Update call when nothing changed (see loop-prevention comment below).
	existingStatus := *deployment.Status.DeepCopy()

	// Resolve the cell's location before instances are built: the instance
	// control strategy stamps Instance.Spec.Location from Status.Location as it
	// creates them, so resolving afterwards left the first generation of
	// instances permanently without one.
	var location servingLocationResult
	if r.NetworkingEnabled {
		location, err = r.resolveLocation(ctx, cl.GetClient())
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed resolving location: %w", err)
		}
		location.evaluate(&deployment)

		// A location the cell contradicts is never written to status: an Instance
		// carrying the wrong location is worse than one carrying none.
		if location.reference != nil {
			deployment.Status.Location = location.reference
		}
	}

	// Collect all instances for this deployment
	listOpts := client.MatchingLabels{
		computev1alpha.WorkloadDeploymentUIDLabel: string(deployment.GetUID()),
	}
	desiredReplicas := effectiveDesiredReplicas(&deployment)

	var instances computev1alpha.InstanceList
	if err := cl.GetClient().List(ctx, &instances, listOpts); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed listing instances: %w", err)
	}

	instanceControl := instancecontrolstateful.NewWithOptions(instancecontrolstateful.Options{
		NetworkingEnabled:        r.NetworkingEnabled,
		EnableReferencedDataGate: r.enableReferencedDataGate,
	})

	actions, err := instanceControl.GetActions(ctx, cl.GetScheme(), &deployment, desiredReplicas, instances.Items)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed getting instance control actions: %w", err)
	}

	logger.Info("collected instance control actions", "count", len(actions))

	for _, action := range actions {
		// We don't need to actually check this, but it'll reduce log noise.
		if action.IsSkipped() {
			continue
		}

		logger.Info("instance control action", "instance", action.Object.GetName(), "action", action.ActionType())

		if err := action.Execute(ctx, cl.GetClient()); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed executing instance control action: %w", err)
		}
	}

	// When networking is disabled, bypass the entire network provisioning path.
	// The Network scheduling gate is treated as cleared for every instance and no
	// interface claims are created. This lets Instances reach the runtime on cells
	// where network-services-operator (VPC) is not yet available.
	networkReadyByInstance := make(map[string]bool, len(instances.Items))
	switch {
	case !r.NetworkingEnabled:
		for _, instance := range instances.Items {
			networkReadyByInstance[instance.Name] = true
		}

	case location.blocked:
		// The cell has stated an identity that contradicts where this deployment
		// was asked to run. Addresses are allocated out of the cell's own
		// location, so claiming them here would place the workload in a city the
		// user did not ask for. Leaving every instance's Network gate held keeps
		// the deployment out of service until the placement fault is corrected,
		// and releases it again with no further action once it is. The Available
		// condition names the fault.
		logger.Info("holding instances: cell location contradicts deployment placement",
			"reason", location.reason, "message", location.message)

	default:
		networkReadyByInstance, err = r.reconcileNetworkInterfaceClaims(ctx, cl.GetClient(), &deployment, instances.Items)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed reconciling network interface claims: %w", err)
		}
	}

	// Each instance's Network scheduling gate is released as soon as its own
	// claims hold their addresses. Instances created by the actions above are not
	// in this pass's list; the Instance watch re-runs this reconcile for them.

	// Translate the suspend/resume request carried on SuspendedAnnotation
	// (written hub-side by the ComputeSuspend/ComputeResume consumer hooks and
	// propagated here by WorkloadDeploymentFederator) into Status.Suspended.
	// This cell is the authoritative writer of Status.Suspended: Karmada only
	// aggregates status cell->hub, so a hub-side write to this field is
	// silently reverted by the next aggregation. Writing it here, before
	// reconcileInstanceGates propagates it to owned Instances below, is what
	// lets the request actually take effect and lets the resulting status
	// reach the hub.
	deployment.Status.Suspended = deployment.Annotations[computev1alpha.SuspendedAnnotation] == suspendedAnnotationTrue

	replicas := len(instances.Items)

	currentReplicas, updatedReplicas, readyReplicas, quotaBlockedReplicas, referencedDataBlockedReplicas, err := r.reconcileInstanceGates(ctx, cl.GetClient(), &deployment, instances.Items, networkReadyByInstance)
	if err != nil {
		return ctrl.Result{}, err
	}

	// The deployment reports one networking state to users even though the gate
	// is now per instance: it is still waiting on the network while any instance
	// is.
	networkReady := true
	for _, instance := range instances.Items {
		if !networkReadyByInstance[instance.Name] {
			networkReady = false
			break
		}
	}

	deployment.Status.Replicas = int32(replicas)
	deployment.Status.CurrentReplicas = int32(currentReplicas)
	deployment.Status.UpdatedReplicas = int32(updatedReplicas)
	deployment.Status.DesiredReplicas = desiredReplicas
	deployment.Status.ReadyReplicas = int32(readyReplicas)
	deployment.Status.Selector = workloadDeploymentPodSelector(&deployment)
	deployment.Status.ObservedGeneration = deployment.Generation

	switch {
	case quotaBlockedReplicas > 0:
		apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:    computev1alpha.WorkloadDeploymentReplicasReady,
			Status:  metav1.ConditionFalse,
			Reason:  computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
			Message: fmt.Sprintf("%d of %d desired replicas are pending quota", quotaBlockedReplicas, desiredReplicas),
		})
	case referencedDataBlockedReplicas > 0:
		apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:    computev1alpha.WorkloadDeploymentReplicasReady,
			Status:  metav1.ConditionFalse,
			Reason:  computev1alpha.ReferencedDataReasonAwaitingPropagation,
			Message: fmt.Sprintf("%d of %d desired replicas are waiting for referenced data companions", referencedDataBlockedReplicas, desiredReplicas),
		})
	default:
		apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:    computev1alpha.WorkloadDeploymentReplicasReady,
			Status:  metav1.ConditionTrue,
			Reason:  reasonReplicasAvailable,
			Message: fmt.Sprintf("%d/%d replicas available", readyReplicas, desiredReplicas),
		})
	}

	if readyReplicas > 0 {
		apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.WorkloadDeploymentAvailable,
			Status:             metav1.ConditionTrue,
			Reason:             computev1alpha.WorkloadDeploymentReasonStableInstanceFound,
			Message:            fmt.Sprintf("%d/%d instances are ready", readyReplicas, replicas),
			ObservedGeneration: deployment.Generation,
		})
	} else {
		availCond := selectWDBlockingCondition(&deployment, networkReady, location, quotaBlockedReplicas, referencedDataBlockedReplicas, replicas, desiredReplicas)
		apimeta.SetStatusCondition(&deployment.Status.Conditions, availCond)
	}

	// Skip the write when the status is unchanged. Without this guard the
	// reconciler's own Status().Update would always produce a new resourceVersion,
	// firing another Update event on the WD and creating an infinite reconcile loop
	// before the predicate on For() was added. The guard is a belt-and-suspenders
	// complement to the predicate: the predicate prevents re-queuing on own writes,
	// and this guard avoids the superfluous API call entirely.
	if !equality.Semantic.DeepEqual(existingStatus, deployment.Status) {
		if err := cl.GetClient().Status().Update(ctx, &deployment); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed updating deployment status: %w", err)
		}
		logger.Info("deployment status updated")
	}

	return ctrl.Result{}, nil
}

func (r *WorkloadDeploymentReconciler) reconcileInstanceGates(
	ctx context.Context,
	c client.Client,
	deployment *computev1alpha.WorkloadDeployment,
	instances []computev1alpha.Instance,
	networkReadyByInstance map[string]bool,
) (currentReplicas, updatedReplicas, readyReplicas, quotaBlockedReplicas, referencedDataBlockedReplicas int, err error) {
	templateHash := instancecontrol.ComputeHash(deployment.Spec.Template)
	for _, instance := range instances {
		// Instances are stamped with the deployment's location as they are
		// created, which leaves any instance that predates the cell learning its
		// own location without one, and nothing else ever revisits it. Backfill
		// it here. Best-effort by design: a failure is logged and the instance
		// keeps running, because location has never gated scheduling.
		if deployment.Status.Location != nil && instance.Spec.Location == nil {
			base := instance.DeepCopy()
			instance.Spec.Location = deployment.Status.Location
			if patchErr := c.Patch(ctx, &instance, client.MergeFrom(base)); patchErr != nil {
				log.FromContext(ctx).Error(patchErr, "failed backfilling instance location", "instance", instance.Name)
			}
		}

		// Propagate suspension state from deployment to instance.
		if instance.Status.Suspended != deployment.Status.Suspended {
			base := instance.DeepCopy()
			instance.Status.Suspended = deployment.Status.Suspended
			if err := c.Status().Patch(ctx, &instance, client.MergeFrom(base)); err != nil {
				return 0, 0, 0, 0, 0, fmt.Errorf("failed propagating suspension state to instance %s: %w", instance.Name, err)
			}
		}

		if apimeta.IsStatusConditionPresentAndEqual(instance.Status.Conditions, computev1alpha.InstanceQuotaGranted, metav1.ConditionFalse) {
			quotaBlockedReplicas++
		}

		if apimeta.IsStatusConditionPresentAndEqual(instance.Status.Conditions, computev1alpha.ReferencedDataReady, metav1.ConditionFalse) {
			referencedDataBlockedReplicas++
		}

		// The gate is released per instance: an instance whose own interface claims
		// hold their addresses boots even while a sibling's are still pending.
		//
		// Spec.Controller is a nilable pointer; guard it before dereferencing the
		// scheduling gates so an instance without controller state cannot panic
		// the reconcile (mirrors the Status.Controller guard below).
		if networkReadyByInstance[instance.Name] && instance.Spec.Controller != nil && len(instance.Spec.Controller.SchedulingGates) > 0 {
			newGates := slices.DeleteFunc(instance.Spec.Controller.SchedulingGates, func(gate computev1alpha.SchedulingGate) bool {
				return gate.Name == instancecontrol.NetworkSchedulingGate.String()
			})
			if len(newGates) != len(instance.Spec.Controller.SchedulingGates) {
				if _, patchErr := controllerutil.CreateOrPatch(ctx, c, &instance, func() error {
					instance.Spec.Controller.SchedulingGates = newGates
					return nil
				}); patchErr != nil {
					return 0, 0, 0, 0, 0, fmt.Errorf("failed updating instance: %w", patchErr)
				}
			}
		}

		// An instance is "updated" once it has observed the desired template
		// revision, regardless of readiness. Counting these (even before they are
		// Programmed) makes a rolling update / restart observable: UpdatedReplicas
		// dips below Replicas while the recreated instance comes up, then recovers.
		// Status.Controller is a pointer the infra provider may not have populated
		// yet; guard the deref to avoid a panic that would abort the reconcile.
		onLatestRevision := instance.Status.Controller != nil &&
			instance.Status.Controller.ObservedTemplateHash == templateHash
		if onLatestRevision {
			updatedReplicas++
		}

		// CurrentReplicas is the Programmed subset of UpdatedReplicas — updated
		// instances that are ready to serve.
		if onLatestRevision && apimeta.IsStatusConditionTrue(instance.Status.Conditions, computev1alpha.InstanceProgrammed) {
			currentReplicas++
		}

		if apimeta.IsStatusConditionTrue(instance.Status.Conditions, computev1alpha.InstanceReady) {
			readyReplicas++
		}
	}
	return currentReplicas, updatedReplicas, readyReplicas, quotaBlockedReplicas, referencedDataBlockedReplicas, nil
}

// wdReferencedDataChangedPredicate returns a predicate for the WorkloadDeployment
// For() watch that fires on:
//   - Any Create, Delete, or Generic event (always enqueue).
//   - An Update event where metadata.generation changed (spec updated), OR where
//     the ReferencedDataReady condition's Status, Reason, or Message changed, OR
//     where Status.Suspended changed.
//
// The predicate intentionally does NOT fire when only the Available or
// ReplicasReady conditions change, because those are written by this reconciler
// itself. Without this guard the reconciler's own Status().Update would re-enqueue
// itself on every run, creating a tight reconcile loop. The equality check before
// Status().Update is a complementary guard, but the predicate is the primary
// protection: it prevents re-enqueuing entirely so the workqueue stays quiet between
// meaningful state transitions.
//
// Loop prevention: the ReferencedDataController (the only other writer of the
// ReferencedDataReady condition) is the intended trigger. When it sets
// ReferencedDataReady=False/SourceNotFound the predicate passes and this
// reconciler re-runs, sees the resolver verdict in deployment.Status.Conditions, and
// promotes Available to ReferencedDataNotReady. Subsequent runs by this reconciler
// (which write Available but not ReferencedDataReady) are filtered out.
//
// SuspendedAnnotation is written out-of-band: hub-side by ComputeSuspend/
// ComputeResume (see suspend_hooks.go), then propagated onto this cell's copy
// by WorkloadDeploymentFederator via Karmada. That's a metadata-only change —
// same Generation, and Status.Suspended hasn't been derived from it yet — so
// without this explicit check the annotation change would be invisible to
// this predicate, and Reconcile would never run to translate it into
// Status.Suspended in the first place. The Status.Suspended check below
// additionally catches this reconciler's own follow-up write of that
// translation, so a slow-to-engage cell still converges.
func wdReferencedDataChangedPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldWD, ok1 := e.ObjectOld.(*computev1alpha.WorkloadDeployment)
			newWD, ok2 := e.ObjectNew.(*computev1alpha.WorkloadDeployment)
			if !ok1 || !ok2 {
				return true // be conservative when type assertion fails
			}
			// Spec change: always reconcile.
			if oldWD.Generation != newWD.Generation {
				return true
			}
			// Suspend/resume request changed: reconcile so it's translated into
			// Status.Suspended and propagated down to the owned Instances.
			if oldWD.Annotations[computev1alpha.SuspendedAnnotation] != newWD.Annotations[computev1alpha.SuspendedAnnotation] {
				return true
			}
			// Suspension state changed: reconcile so the suspend/resume is
			// propagated down to the owned Instances.
			if oldWD.Status.Suspended != newWD.Status.Suspended {
				return true
			}
			// ReferencedDataReady condition changed: reconcile so Available is
			// updated to reflect the resolver's verdict.
			return wdRefDataCondChanged(
				apimeta.FindStatusCondition(oldWD.Status.Conditions, computev1alpha.ReferencedDataReady),
				apimeta.FindStatusCondition(newWD.Status.Conditions, computev1alpha.ReferencedDataReady),
			)
		},
		CreateFunc:  func(_ event.CreateEvent) bool { return true },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return true },
		GenericFunc: func(_ event.GenericEvent) bool { return true },
	}
}

// wdRefDataCondChanged returns true when the ReferencedDataReady condition's
// observable fields (Status, Reason, Message) differ between old and new. Presence
// changes (nil → non-nil or vice versa) are also treated as a change. The
// LastTransitionTime field is excluded because it changes on every status flip and
// would defeat the loop-prevention intent of wdReferencedDataChangedPredicate.
func wdRefDataCondChanged(old, new *metav1.Condition) bool {
	if (old == nil) != (new == nil) {
		return true // condition was added or removed
	}
	if old == nil {
		return false // both nil — no change
	}
	return old.Status != new.Status ||
		old.Reason != new.Reason ||
		old.Message != new.Message
}

// selectWDBlockingCondition evaluates all blocking causes for a WorkloadDeployment
// that has no ready replicas and returns the Available condition reflecting the
// highest-priority blocker. All causes are evaluated before selecting the winner
// so that a transient cause (e.g. network provisioning) cannot mask a more
// actionable one (e.g. missing referenced data).
func selectWDBlockingCondition(
	deployment *computev1alpha.WorkloadDeployment,
	networkReady bool,
	location servingLocationResult,
	quotaBlockedReplicas, referencedDataBlockedReplicas, replicas int,
	desiredReplicas int32,
) metav1.Condition {
	type candidate struct {
		reason   string
		message  string
		priority int
	}

	var best candidate

	// Try each blocking cause and track the highest-priority one.
	consider := func(reason, message string) {
		p := wdBlockingReasonPriority(reason)
		if p > best.priority {
			best = candidate{reason: reason, message: message, priority: p}
		}
	}

	// An unusable cell location is the only user-visible signal that instances
	// are running without the location their placement asked for, and it is
	// considered first so it wins over the generic provisioning reason it shares
	// a priority with.
	if location.reason != "" {
		consider(location.reason, location.message)
	}

	if !networkReady {
		consider(computev1alpha.WorkloadDeploymentReasonNetworkProvisioning, "Network is being provisioned")
	}

	// WD-level ReferencedDataReady condition reflects the resolver verdict; when
	// it is False, the source object is missing/unauthorized/too-large and the
	// companion will never arrive. Treat this as a hard, high-priority blocker.
	if wdRefDataCond := apimeta.FindStatusCondition(deployment.Status.Conditions, computev1alpha.ReferencedDataReady); wdRefDataCond != nil && wdRefDataCond.Status == metav1.ConditionFalse {
		consider(computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, wdRefDataCond.Message)
	}

	// In federated topology the Karmada status-aggregation pathway carries status
	// cell→hub only, so the cell WD never receives the ReferencedDataReady condition
	// written by the hub-side resolver. The ReferencedDataErrorAnnotation bridges
	// terminal errors hub→cell alongside ObjectMeta (Karmada propagates annotations).
	// Read it here as a parallel path so the cell WD Available condition reflects the
	// terminal error even without the status condition. The annotation takes priority
	// over the propagation-lag check below when both could apply to the same bucket.
	if raw, ok := deployment.Annotations[computev1alpha.ReferencedDataErrorAnnotation]; ok && raw != "" {
		if reason, message, err := decodeTerminalError(raw); err == nil && reason != "" {
			consider(reason, message)
		}
		// Malformed annotation values are silently ignored: a parse failure here
		// should not block the WD from reporting whatever state it does know.
	}

	if quotaBlockedReplicas > 0 {
		consider(computev1alpha.WorkloadDeploymentReasonQuotaNotGranted,
			fmt.Sprintf("%d of %d desired instances pending quota", quotaBlockedReplicas, desiredReplicas))
	}

	// referencedDataBlockedReplicas > 0 with no WD-level resolver error means
	// companions are still propagating to the cell (propagation lag, not a hard error).
	if referencedDataBlockedReplicas > 0 {
		wdRefDataTrue := apimeta.IsStatusConditionTrue(deployment.Status.Conditions, computev1alpha.ReferencedDataReady)
		wdRefDataAbsent := apimeta.FindStatusCondition(deployment.Status.Conditions, computev1alpha.ReferencedDataReady) == nil
		if wdRefDataTrue || wdRefDataAbsent {
			consider(computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady,
				fmt.Sprintf("%d of %d desired instances waiting for companion propagation", referencedDataBlockedReplicas, desiredReplicas))
		}
	}

	if replicas > 0 {
		consider(computev1alpha.WorkloadDeploymentReasonInstancesProvisioning, "Instances are being provisioned")
	}

	// Fallback: nothing more specific is known yet.
	if best.priority == 0 {
		best.reason = computev1alpha.WorkloadDeploymentReasonInstancesProvisioning
		best.message = "No instances available yet"
	}

	return metav1.Condition{
		Type:               computev1alpha.WorkloadDeploymentAvailable,
		Status:             metav1.ConditionFalse,
		Reason:             best.reason,
		Message:            best.message,
		ObservedGeneration: deployment.Generation,
	}
}

// wdBlockingReasonPriority returns the relative priority of a blocking reason on
// WorkloadDeployment.Available. Higher numbers indicate causes that are more
// actionable and should be surfaced over lower-priority transient states.
// This is a server-internal ranking; clients observe only the winning condition.
//
// Priority table (matches RFC §5.4):
//
//	0 - unknown/default
//	1 - InstancesProvisioning  (transient startup)
//	2 - NetworkProvisioning / NoMatchingLocation (infra provisioning; when both
//	    apply the unresolved city wins, because it names something an operator
//	    can act on)
//	3 - QuotaNotGranted        (operator action may be needed)
//	4 - ReferencedDataNotReady (AwaitingPropagation / Resolving — expected to clear)
//	5 - SourceNotFound / SourceTooLarge / SourceUnauthorized (hard spec error)
//	6 - NetworkNotFound        (hard error; user action required)
//	7 - NetworkFailedToCreate  (hard infra error)
//	8 - CityCodeMismatch / AmbiguousServingLocation (the deployment is on a cell
//	    that cannot serve it; nothing the user does clears it, and no other
//	    blocker is worth reporting until it is fixed)
func wdBlockingReasonPriority(reason string) int {
	switch reason {
	case computev1alpha.WorkloadDeploymentReasonInstancesProvisioning:
		return 1
	case computev1alpha.WorkloadDeploymentReasonNetworkProvisioning,
		computev1alpha.WorkloadDeploymentReasonNoMatchingLocation:
		return 2
	case computev1alpha.WorkloadDeploymentReasonQuotaNotGranted,
		computev1alpha.InstanceProgrammedReasonPendingQuota:
		return 3
	case computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady,
		computev1alpha.ReferencedDataReasonAwaitingPropagation,
		computev1alpha.ReferencedDataReasonResolving:
		return 4
	case computev1alpha.ReferencedDataReasonSourceNotFound,
		computev1alpha.ReferencedDataReasonSourceTooLarge,
		computev1alpha.ReferencedDataReasonSourceUnauthorized:
		return 5
	case computev1alpha.WorkloadReasonNetworkNotFound:
		return 6
	case reasonNetworkFailedToCreate:
		return 7
	case computev1alpha.WorkloadDeploymentReasonCityCodeMismatch,
		computev1alpha.WorkloadDeploymentReasonAmbiguousServingLocation:
		return 8
	default:
		return 0
	}
}

// servingLocationResult is what the cell was able to say about the location it
// serves, expressed the way the deployment needs it.
type servingLocationResult struct {
	// reference is the location to stamp on the deployment and its instances. It
	// is nil whenever the cell's answer is missing or unusable.
	reference *networkingv1alpha.LocationReference

	// servingLocation is the single ServingLocation the cell was delivered, or
	// nil when it was delivered none or more than one.
	servingLocation *networkingv1alpha.ServingLocation

	// reason and message are the Available condition the deployment should
	// report while the location is unusable. Both are empty once it is usable.
	reason  string
	message string

	// blocked distinguishes a cell that contradicts the deployment's placement
	// from one that simply has not been identified yet. Only the former holds
	// instances back; an unidentified cell must never stop a workload from
	// running.
	blocked bool
}

// resolveLocation reads the location the cell serves.
//
// A cell cannot tell where it is on its own; the platform delivers it exactly
// one ServingLocation naming the place it sits in. Anything other than exactly
// one is reported rather than guessed at, per the ServingLocation contract.
func (r *WorkloadDeploymentReconciler) resolveLocation(
	ctx context.Context,
	c client.Client,
) (servingLocationResult, error) {
	var servingLocations networkingv1alpha.ServingLocationList
	if err := c.List(ctx, &servingLocations); err != nil {
		return servingLocationResult{}, fmt.Errorf("failed to list serving locations: %w", err)
	}

	if len(servingLocations.Items) > 1 {
		names := make([]string, 0, len(servingLocations.Items))
		for _, servingLocation := range servingLocations.Items {
			names = append(names, servingLocation.Name)
		}
		slices.Sort(names)

		return servingLocationResult{
			reason: computev1alpha.WorkloadDeploymentReasonAmbiguousServingLocation,
			message: fmt.Sprintf("This cell has been given %d locations to serve (%s) and will not guess between them",
				len(names), strings.Join(names, ", ")),
			blocked: true,
		}, nil
	}

	if len(servingLocations.Items) == 0 {
		// Not an error: a cell that has not been identified yet still runs
		// workloads, it just cannot tell them where they are.
		log.FromContext(ctx).V(1).Info("cell has no serving location, waiting")

		return servingLocationResult{
			reason: computev1alpha.WorkloadDeploymentReasonNoMatchingLocation,
			message: fmt.Sprintf("This cell has not been told which location it serves; it needs the %s cluster label, or its location has not reached it yet",
				networkingv1alpha.ServingLocationTopologyLabel),
		}, nil
	}

	servingLocation := &servingLocations.Items[0]

	// A ServingLocation takes the name of the Location it was copied from, and
	// Location is cluster scoped, so the reference carries a name and no
	// namespace.
	return servingLocationResult{
		reference:       &networkingv1alpha.LocationReference{Name: servingLocation.Name},
		servingLocation: servingLocation,
	}, nil
}

// evaluate checks the resolved location against where the deployment asked to
// run. A deployment that reaches a cell serving another city was misplaced by
// the propagation layer, so it is reported as a fault rather than silently run
// in the wrong city.
func (s *servingLocationResult) evaluate(deployment *computev1alpha.WorkloadDeployment) {
	if s.servingLocation == nil {
		return
	}

	cityCode := s.servingLocation.CityCode()
	if cityCode == deployment.Spec.CityCode {
		return
	}

	s.reference = nil
	s.reason = computev1alpha.WorkloadDeploymentReasonCityCodeMismatch
	s.message = fmt.Sprintf("Deployment asked for city %q but this cell serves %q; it was delivered to the wrong cell",
		deployment.Spec.CityCode, cityCode)
	s.blocked = true
}

// reconcileNetworkInterfaceClaims ensures one NetworkInterfaceClaim exists per
// instance interface, and reports which instances hold every address they asked
// for. The returned map is keyed by instance name; an instance missing from it,
// or mapped to false, is still waiting on its addresses.
//
// A claim names the instance slot rather than the instance object, and is owned
// by the instance filling that slot. Deleting the instance therefore releases
// the claim, at which point the interface's reclaim policy decides whether the
// addresses are returned to IPAM or held for the next instance in the slot.
func (r *WorkloadDeploymentReconciler) reconcileNetworkInterfaceClaims(
	ctx context.Context,
	c client.Client,
	deployment *computev1alpha.WorkloadDeployment,
	instances []computev1alpha.Instance,
) (map[string]bool, error) {
	logger := log.FromContext(ctx)

	readyByInstance := make(map[string]bool, len(instances))
	for i := range instances {
		instance := &instances[i]
		if !instance.DeletionTimestamp.IsZero() {
			continue
		}

		ready := true
		for _, networkInterface := range instance.Spec.NetworkInterfaces {
			claim, err := r.ensureNetworkInterfaceClaim(ctx, c, deployment, instance, networkInterface)
			if err != nil {
				return nil, err
			}

			if !networkInterfaceClaimSatisfied(claim) {
				ready = false
				if reason, message := networkInterfaceClaimRejection(claim); reason != "" {
					// The rejection is surfaced to users on the Instance by the
					// instance reconciler; log it here so an operator watching the
					// deployment sees why it is stuck.
					logger.Info("network interface claim cannot be fulfilled",
						"claim", claim.Name, "reason", reason, "message", message)
				}
			}
		}
		readyByInstance[instance.Name] = ready
	}

	return readyByInstance, nil
}

// ensureNetworkInterfaceClaim creates the claim for one instance interface if it
// is absent, and returns the claim either way.
//
// An existing claim is never updated: almost every field of a claim spec is
// immutable, because the addresses were allocated against it. A changed
// interface request is expressed by replacing the instance, which replaces the
// claim with it.
func (r *WorkloadDeploymentReconciler) ensureNetworkInterfaceClaim(
	ctx context.Context,
	c client.Client,
	deployment *computev1alpha.WorkloadDeployment,
	instance *computev1alpha.Instance,
	networkInterface computev1alpha.InstanceNetworkInterface,
) (*networkingv1alpha.NetworkInterfaceClaim, error) {
	claim := &networkingv1alpha.NetworkInterfaceClaim{}
	key := client.ObjectKey{
		Namespace: deployment.Namespace,
		Name:      networkInterfaceClaimName(instance.Name, instanceInterfaceName(networkInterface)),
	}

	err := c.Get(ctx, key, claim)
	if err == nil {
		return claim, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed checking for existing network interface claim: %w", err)
	}

	claim = &networkingv1alpha.NetworkInterfaceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: key.Namespace,
			Name:      key.Name,
		},
		Spec: desiredNetworkInterfaceClaimSpec(networkInterface),
	}

	// The claim belongs to the instance, not the deployment: the instance going
	// away is what ends the slot's hold on the interface.
	if err := controllerutil.SetControllerReference(instance, claim, c.Scheme()); err != nil {
		return nil, fmt.Errorf("failed to set controller on network interface claim: %w", err)
	}

	if err := c.Create(ctx, claim); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Lost a race with another writer, or with this controller's own stale
			// cache. Read what is there rather than reporting a failure.
			if getErr := c.Get(ctx, key, claim); getErr != nil {
				return nil, fmt.Errorf("failed fetching network interface claim: %w", getErr)
			}
			return claim, nil
		}
		return nil, fmt.Errorf("failed creating network interface claim: %w", err)
	}

	log.FromContext(ctx).Info("created network interface claim", "claim", claim.Name, "instance", instance.Name)

	return claim, nil
}

func (r *WorkloadDeploymentReconciler) Finalize(_ context.Context, _ client.Object) (finalizer.Result, error) {
	// Instance cascade is handled by Kubernetes GC via owner references set at
	// Instance creation time. No explicit deletion is needed here.
	return finalizer.Result{}, nil
}

// WorkloadDeploymentReconcilerOptions configures the WorkloadDeploymentReconciler.
type WorkloadDeploymentReconcilerOptions struct {
	// EnableReferencedDataGate mirrors FeatureFlagsConfig.EnableReferencedDataGate.
	EnableReferencedDataGate bool
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadDeploymentReconciler) SetupWithManager(mgr mcmanager.Manager, opts ...WorkloadDeploymentReconcilerOptions) error {
	r.mgr = mgr
	for _, o := range opts {
		r.enableReferencedDataGate = o.EnableReferencedDataGate
	}
	r.finalizers = finalizer.NewFinalizers()
	if err := r.finalizers.Register(workloadControllerFinalizer, r); err != nil {
		return fmt.Errorf("failed to register finalizer: %w", err)
	}

	b := mcbuilder.ControllerManagedBy(mgr).
		// The predicate gates re-enqueuing on meaningful WD changes: spec updates
		// (generation bump) or a ReferencedDataReady condition change written by
		// ReferencedDataController. Without it, each Status().Update by this
		// reconciler (writing Available/ReplicasReady) would re-enqueue itself,
		// creating a tight loop and delaying the ReferencedDataReady signal from
		// the resolver.
		For(&computev1alpha.WorkloadDeployment{},
			mcbuilder.WithEngageWithLocalCluster(false),
			mcbuilder.WithPredicates(wdReferencedDataChangedPredicate()),
		).
		Owns(&computev1alpha.Instance{})

	// Only watch networking resources when the networking integration is enabled.
	// On cells without network-services-operator these watches would log spurious
	// errors for missing CRDs.
	if r.NetworkingEnabled {
		b = b.
			// A claim becoming bound and allocated is what releases an instance's
			// Network gate, and nothing else wakes this reconciler for it. The claim
			// is owned by its Instance, which in turn is owned by the deployment, so
			// the event is mapped up two levels.
			Watches(&networkingv1alpha.NetworkInterfaceClaim{}, func(clusterName multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
				return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []mcreconcile.Request {
					return enqueueWorkloadDeploymentForClaim(ctx, cl.GetClient(), clusterName, o)
				})
			}).
			// A deployment on a cell that does not yet know its own location waits
			// without any other wake-up event, and the reconciler does not poll.
			// Watching ServingLocations re-reconciles those deployments as soon as
			// the cell learns where it is, so Status.Location is filled in.
			Watches(&networkingv1alpha.ServingLocation{}, func(clusterName multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
				return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []mcreconcile.Request {
					return enqueueWorkloadDeploymentsForServingLocation(ctx, cl.GetClient(), clusterName)
				})
			})
	}

	return b.Complete(r)
}

// enqueueWorkloadDeploymentForClaim maps a NetworkInterfaceClaim to the
// WorkloadDeployment that owns the Instance the claim was created for. Claims
// created by anything else carry no Instance owner and map to nothing.
func enqueueWorkloadDeploymentForClaim(ctx context.Context, c client.Client, clusterName multicluster.ClusterName, claim client.Object) []mcreconcile.Request {
	logger := log.FromContext(ctx)

	owner := metav1.GetControllerOf(claim)
	if owner == nil || owner.Kind != "Instance" {
		return nil
	}

	var instance computev1alpha.Instance
	if err := c.Get(ctx, types.NamespacedName{Namespace: claim.GetNamespace(), Name: owner.Name}, &instance); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to get instance for network interface claim", "claim", claim.GetName())
		}
		return nil
	}

	instanceOwner := metav1.GetControllerOf(&instance)
	if instanceOwner == nil || instanceOwner.Kind != kindWorkloadDeployment {
		return nil
	}

	return []mcreconcile.Request{
		{
			Request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: instance.Namespace,
					Name:      instanceOwner.Name,
				},
			},
			ClusterName: clusterName,
		},
	}
}

// enqueueWorkloadDeploymentsForServingLocation maps a ServingLocation change to
// every WorkloadDeployment on the cell. A cell's identity applies to all of
// them: it decides the location they are stamped with, and whether they are on
// the right cell at all. There is at most one ServingLocation per cell, so this
// fans out once per delivery, not per object.
func enqueueWorkloadDeploymentsForServingLocation(ctx context.Context, c client.Client, clusterName multicluster.ClusterName) []mcreconcile.Request {
	logger := log.FromContext(ctx)

	var workloadDeployments computev1alpha.WorkloadDeploymentList
	if err := c.List(ctx, &workloadDeployments); err != nil {
		logger.Error(err, "failed to list workload deployments")
		return nil
	}

	requests := make([]mcreconcile.Request, 0, len(workloadDeployments.Items))
	for _, workload := range workloadDeployments.Items {
		requests = append(requests, mcreconcile.Request{
			Request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: workload.Namespace,
					Name:      workload.Name,
				},
			},
			ClusterName: clusterName,
		})
	}

	return requests
}
