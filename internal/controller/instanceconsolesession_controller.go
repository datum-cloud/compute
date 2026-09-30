// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
	milosource "go.miloapis.com/milo/pkg/multicluster-runtime/source"
)

const (
	defaultSessionCleanupTimeout = 5 * time.Minute

	// DefaultSessionClaimTimeout is how long after its creation a session may
	// wait for a cell to claim it before it ends as Unavailable.
	DefaultSessionClaimTimeout = 30 * time.Second
)

// InstanceConsoleSessionReconciler delivers shell sessions created in a project
// to the cell that runs their instance, mirrors what the cell reports back, and
// removes each session as soon as it ends.
//
// A session is delivered as a copy in the federation hub namespace of the
// instance's WorkloadDeployment. The copy carries the hub WorkloadDeployment's
// placement labels, so the WorkloadDeployment's PropagationPolicy delivers it to
// the same cells. The shell agent there writes status to the hub copy, and this
// controller copies that status onto the project session.
type InstanceConsoleSessionReconciler struct {
	mgr mcmanager.Manager

	// FederationClient reads and writes hub copies on the federation hub.
	FederationClient client.Client

	// FederationCluster watches hub copies, so status the agent writes reaches
	// the project session without waiting for a resync. It is nil in unit tests.
	FederationCluster cluster.Cluster

	// ReportingInstance identifies this replica on the events it records.
	ReportingInstance string

	// CleanupTimeout is how long a deleted session waits for the cell to
	// confirm cleanup. Zero means five minutes.
	CleanupTimeout time.Duration

	// ClaimTimeout is how long after its creation a session waits for a cell to
	// claim it before it ends as Unavailable. Zero means
	// DefaultSessionClaimTimeout.
	ClaimTimeout time.Duration

	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instanceconsolesessions,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instanceconsolesessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instanceconsolesessions/finalizers,verbs=update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create

func (r *InstanceConsoleSessionReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	if req.ClusterName == "" {
		return ctrl.Result{}, nil
	}

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	projectClient := cl.GetClient()

	var session computev1alpha.InstanceConsoleSession
	if err := projectClient.Get(ctx, req.NamespacedName, &session); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !session.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, projectClient, &session)
	}

	if !controllerutil.ContainsFinalizer(&session, computev1alpha.InstanceConsoleSessionFinalizer) {
		patch := client.MergeFrom(session.DeepCopy())
		controllerutil.AddFinalizer(&session, computev1alpha.InstanceConsoleSessionFinalizer)
		if err := projectClient.Patch(ctx, &session, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed adding finalizer: %w", err)
		}
	}

	var result ctrl.Result
	if !sessionEnded(&session) {
		var err error
		if result, err = r.reconcileDelivery(ctx, req.ClusterName, projectClient, &session); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.recordLifecycleEvents(ctx, projectClient, &session, ""); err != nil {
		return ctrl.Result{}, err
	}

	if sessionEnded(&session) {
		// The end is on record, so nothing needs the session any longer, and
		// deleting it releases its quota.
		if err := projectClient.Delete(ctx, &session, client.Preconditions{UID: &session.UID}); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("failed deleting ended session: %w", err)
		}
		log.FromContext(ctx).Info("deleted ended session", "reason", sessionReadyReason(&session))
		return ctrl.Result{}, nil
	}

	return result, nil
}

// reconcileDelivery delivers a session that has not ended and mirrors its hub
// copy's status onto it. It ends the session when its instance is gone, when
// no cell claims it by its claim deadline, and when its hub copy goes away
// before the session ends.
func (r *InstanceConsoleSessionReconciler) reconcileDelivery(
	ctx context.Context,
	clusterName multicluster.ClusterName,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
) (ctrl.Result, error) {
	claimDeadline := session.CreationTimestamp.Add(r.claimTimeout())

	hubCopy, err := r.recordedHubCopy(ctx, session)
	if err != nil {
		return ctrl.Result{}, err
	}
	if hubCopy != nil && sessionEnded(hubCopy) {
		return ctrl.Result{}, r.copyStatus(ctx, projectClient, session, hubCopy)
	}

	var instance computev1alpha.Instance
	instanceKey := types.NamespacedName{Namespace: session.Namespace, Name: session.Spec.InstanceRef.Name}
	if err := projectClient.Get(ctx, instanceKey, &instance); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("failed getting instance %s: %w", instanceKey, err)
	}
	if instance.UID != session.Spec.InstanceRef.UID || !instance.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.endSession(ctx, projectClient, session,
			computev1alpha.InstanceConsoleSessionReasonInstanceNotFound,
			fmt.Sprintf("Instance %q was deleted or replaced.", session.Spec.InstanceRef.Name))
	}

	if hubCopy == nil {
		if sessionClaimed(session) {
			return ctrl.Result{}, r.endSession(ctx, projectClient, session,
				computev1alpha.InstanceConsoleSessionReasonAgentLost,
				"The session's copy on the cell was removed before the session ended.")
		}
		if !r.now().Before(claimDeadline) {
			log.FromContext(ctx).Info("ending session no cell could receive in time")
			return ctrl.Result{}, r.endSession(ctx, projectClient, session,
				computev1alpha.InstanceConsoleSessionReasonUnavailable, r.unavailableMessage())
		}
		if hubCopy, err = r.deliver(ctx, clusterName, projectClient, session, &instance); err != nil {
			return ctrl.Result{}, err
		}
	}

	if !hubCopy.DeletionTimestamp.IsZero() {
		return r.reconcileTerminatingHubCopy(ctx, projectClient, session, hubCopy, claimDeadline)
	}

	wait, err := r.endUnclaimed(ctx, hubCopy, claimDeadline)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.copyStatus(ctx, projectClient, session, hubCopy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: wait}, nil
}

// reconcileTerminatingHubCopy handles a hub copy deleted while its session is
// still open, as happens when the hub deployment that owns it is replaced. The
// session waits as long as a deleted session waits for the cell to confirm
// cleanup, or only until its claim deadline if no cell claimed it, and then
// releases the copy and ends.
func (r *InstanceConsoleSessionReconciler) reconcileTerminatingHubCopy(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
	hubCopy *computev1alpha.InstanceConsoleSession,
	claimDeadline time.Time,
) (ctrl.Result, error) {
	if err := r.copyStatus(ctx, projectClient, session, hubCopy); err != nil {
		return ctrl.Result{}, err
	}

	claimed := sessionClaimed(session)
	deadline := hubCopy.DeletionTimestamp.Add(r.cleanupTimeout())
	if !claimed && claimDeadline.Before(deadline) {
		deadline = claimDeadline
	}
	if wait := deadline.Sub(r.now()); wait > 0 {
		return ctrl.Result{RequeueAfter: wait}, nil
	}

	if controllerutil.ContainsFinalizer(hubCopy, computev1alpha.InstanceConsoleSessionAgentFinalizer) {
		if err := r.recordCleanupUnconfirmed(ctx, projectClient, session); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.releaseHubCopy(ctx, hubCopy); err != nil {
			return ctrl.Result{}, err
		}
	}

	if !claimed {
		return ctrl.Result{}, r.endSession(ctx, projectClient, session,
			computev1alpha.InstanceConsoleSessionReasonUnavailable, r.unavailableMessage())
	}
	return ctrl.Result{}, r.endSession(ctx, projectClient, session,
		computev1alpha.InstanceConsoleSessionReasonAgentLost,
		"The session's copy on the cell was removed and the cell did not report the session's end.")
}

// endUnclaimed ends a hub copy no cell has claimed by the deadline, and
// otherwise returns how long remains until the deadline. The status update
// carries the copy's resourceVersion, and so does the shell agent's claim, so
// exactly one of them succeeds: a conflict here means the copy changed, most
// likely because a cell claimed it, and the retry re-reads it.
func (r *InstanceConsoleSessionReconciler) endUnclaimed(
	ctx context.Context,
	hubCopy *computev1alpha.InstanceConsoleSession,
	deadline time.Time,
) (time.Duration, error) {
	if hubCopy.Status.Connection != nil || sessionEnded(hubCopy) || !hubCopy.DeletionTimestamp.IsZero() {
		return 0, nil
	}
	if wait := deadline.Sub(r.now()); wait > 0 {
		return wait, nil
	}

	now := metav1.NewTime(r.now())
	hubCopy.Status.EndedAt = &now
	apimeta.SetStatusCondition(&hubCopy.Status.Conditions, metav1.Condition{
		Type:               computev1alpha.InstanceConsoleSessionReady,
		Status:             metav1.ConditionFalse,
		Reason:             computev1alpha.InstanceConsoleSessionReasonUnavailable,
		Message:            r.unavailableMessage(),
		ObservedGeneration: hubCopy.Generation,
		LastTransitionTime: now,
	})
	if err := r.FederationClient.Status().Update(ctx, hubCopy); err != nil {
		return 0, fmt.Errorf("failed ending unclaimed hub session %s/%s: %w", hubCopy.Namespace, hubCopy.Name, err)
	}
	log.FromContext(ctx).Info("ended session no cell claimed in time", "hubNamespace", hubCopy.Namespace)
	return 0, nil
}

// recordedHubCopy returns the hub copy in the namespace recorded on the
// session, or nil when none is recorded or the copy does not exist.
func (r *InstanceConsoleSessionReconciler) recordedHubCopy(
	ctx context.Context,
	session *computev1alpha.InstanceConsoleSession,
) (*computev1alpha.InstanceConsoleSession, error) {
	hubNS := session.Annotations[computev1alpha.FederationNamespaceAnnotation]
	if hubNS == "" {
		return nil, nil
	}
	return r.getHubCopy(ctx, hubNS, session)
}

func (r *InstanceConsoleSessionReconciler) unavailableMessage() string {
	return fmt.Sprintf("No cell took the session within %s. Shell sessions are not available for this instance right now.", r.claimTimeout())
}

// deliver creates the session's hub copy and returns it.
func (r *InstanceConsoleSessionReconciler) deliver(
	ctx context.Context,
	clusterName multicluster.ClusterName,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
	instance *computev1alpha.Instance,
) (*computev1alpha.InstanceConsoleSession, error) {
	hubCopy, hubDeployment, err := r.buildHubCopy(ctx, clusterName, projectClient, session, instance)
	if err != nil {
		return nil, err
	}

	// Record the hub namespace before writing into it, so finalization can
	// always find the copy even once the instance and its deployment are gone.
	if session.Annotations[computev1alpha.FederationNamespaceAnnotation] != hubCopy.Namespace {
		patch := client.MergeFrom(session.DeepCopy())
		if session.Annotations == nil {
			session.Annotations = map[string]string{}
		}
		session.Annotations[computev1alpha.FederationNamespaceAnnotation] = hubCopy.Namespace
		if err := projectClient.Patch(ctx, session, patch); err != nil {
			return nil, fmt.Errorf("failed recording federation namespace on session: %w", err)
		}
	}

	if err := controllerutil.SetControllerReference(hubDeployment, hubCopy, federationScheme(r.FederationClient.Scheme())); err != nil {
		return nil, fmt.Errorf("failed setting hub session owner: %w", err)
	}
	if err := r.FederationClient.Create(ctx, hubCopy); err != nil {
		return nil, fmt.Errorf("failed creating hub session %s/%s: %w", hubCopy.Namespace, hubCopy.Name, err)
	}
	log.FromContext(ctx).Info("delivered session to federation hub", "hubNamespace", hubCopy.Namespace)
	return hubCopy, nil
}

// buildHubCopy builds the hub copy of a session and returns it with the hub
// WorkloadDeployment that owns it.
func (r *InstanceConsoleSessionReconciler) buildHubCopy(
	ctx context.Context,
	clusterName multicluster.ClusterName,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
	instance *computev1alpha.Instance,
) (*computev1alpha.InstanceConsoleSession, *computev1alpha.WorkloadDeployment, error) {
	deploymentName := instance.Labels[computev1alpha.WorkloadDeploymentNameLabel]
	if deploymentName == "" {
		if owner := metav1.GetControllerOf(instance); owner != nil && owner.Kind == kindWorkloadDeployment {
			deploymentName = owner.Name
		}
	}
	if deploymentName == "" {
		return nil, nil, fmt.Errorf("instance %s/%s names no WorkloadDeployment", instance.Namespace, instance.Name)
	}

	cellDeploymentUID := instance.Labels[computev1alpha.WorkloadDeploymentUIDLabel]
	if cellDeploymentUID == "" {
		return nil, nil, fmt.Errorf("instance %s/%s is missing the %s label", instance.Namespace, instance.Name, computev1alpha.WorkloadDeploymentUIDLabel)
	}

	var deployment computev1alpha.WorkloadDeployment
	if err := projectClient.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: deploymentName}, &deployment); err != nil {
		return nil, nil, fmt.Errorf("failed getting WorkloadDeployment %s/%s: %w", instance.Namespace, deploymentName, err)
	}
	hubNS := deployment.Annotations[computev1alpha.FederationNamespaceAnnotation]
	if hubNS == "" {
		return nil, nil, fmt.Errorf("WorkloadDeployment %s/%s has not been federated yet", deployment.Namespace, deployment.Name)
	}

	var hubDeployment computev1alpha.WorkloadDeployment
	if err := r.FederationClient.Get(ctx, types.NamespacedName{Namespace: hubNS, Name: deploymentName}, &hubDeployment); err != nil {
		return nil, nil, fmt.Errorf("failed getting hub WorkloadDeployment %s/%s: %w", hubNS, deploymentName, err)
	}
	location := hubDeployment.Labels[locationLabel]
	if location == "" {
		return nil, nil, fmt.Errorf("hub WorkloadDeployment %s/%s is missing the %s label", hubNS, deploymentName, locationLabel)
	}

	labels := map[string]string{
		locationLabel: location,
		computev1alpha.InstanceConsoleSessionUIDLabel:          string(session.UID),
		computev1alpha.InstanceConsoleSessionInstanceNameLabel: instance.Name,
		computev1alpha.WorkloadDeploymentUIDLabel:              cellDeploymentUID,
		downstreamclient.UpstreamOwnerClusterNameLabel:         EncodeClusterName(string(clusterName)),
		downstreamclient.UpstreamOwnerNamespaceLabel:           session.Namespace,
	}
	if class, ok := hubDeployment.Labels[computev1alpha.RuntimeClassLabel]; ok {
		labels[computev1alpha.RuntimeClassLabel] = class
	}

	return &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:      session.Name,
			Namespace: hubNS,
			Labels:    labels,
		},
		Spec: *session.Spec.DeepCopy(),
	}, &hubDeployment, nil
}

// getHubCopy returns the session's hub copy, or nil when there is none. A copy
// left behind by an earlier session with the same name is not this session's.
func (r *InstanceConsoleSessionReconciler) getHubCopy(
	ctx context.Context,
	hubNS string,
	session *computev1alpha.InstanceConsoleSession,
) (*computev1alpha.InstanceConsoleSession, error) {
	var hubCopy computev1alpha.InstanceConsoleSession
	if err := r.FederationClient.Get(ctx, types.NamespacedName{Namespace: hubNS, Name: session.Name}, &hubCopy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed getting hub session %s/%s: %w", hubNS, session.Name, err)
	}
	if hubCopy.Labels[computev1alpha.InstanceConsoleSessionUIDLabel] != string(session.UID) {
		return nil, nil
	}
	return &hubCopy, nil
}

// copyStatus mirrors the status the cell wrote on the hub copy. An ended
// session keeps its status: a terminal reason never changes.
func (r *InstanceConsoleSessionReconciler) copyStatus(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
	hubCopy *computev1alpha.InstanceConsoleSession,
) error {
	if sessionEnded(session) || equality.Semantic.DeepEqual(session.Status, hubCopy.Status) {
		return nil
	}
	session.Status = *hubCopy.Status.DeepCopy()
	if err := projectClient.Status().Update(ctx, session); err != nil {
		return fmt.Errorf("failed copying hub status to session: %w", err)
	}
	return nil
}

// endSession ends a session the cell never received.
func (r *InstanceConsoleSessionReconciler) endSession(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
	reason, message string,
) error {
	now := metav1.NewTime(r.now())
	session.Status.EndedAt = &now
	apimeta.SetStatusCondition(&session.Status.Conditions, metav1.Condition{
		Type:               computev1alpha.InstanceConsoleSessionReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: session.Generation,
		LastTransitionTime: now,
	})
	if err := projectClient.Status().Update(ctx, session); err != nil {
		return fmt.Errorf("failed ending session: %w", err)
	}
	return nil
}

// finalize removes the hub copy and releases the project session once the cell
// confirms cleanup by removing its finalizer from the copy, or once the cleanup
// timeout passes without that confirmation.
func (r *InstanceConsoleSessionReconciler) finalize(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(session, computev1alpha.InstanceConsoleSessionFinalizer) {
		return ctrl.Result{}, nil
	}
	logger := log.FromContext(ctx)

	if hubNS := session.Annotations[computev1alpha.FederationNamespaceAnnotation]; hubNS != "" {
		hubCopy, err := r.getHubCopy(ctx, hubNS, session)
		if err != nil {
			return ctrl.Result{}, err
		}
		if hubCopy != nil && hubCopy.DeletionTimestamp.IsZero() {
			if err := r.FederationClient.Delete(ctx, hubCopy, client.Preconditions{UID: &hubCopy.UID}); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, fmt.Errorf("failed deleting hub session %s/%s: %w", hubNS, session.Name, err)
			}
			if hubCopy, err = r.getHubCopy(ctx, hubNS, session); err != nil {
				return ctrl.Result{}, err
			}
		}
		if hubCopy != nil {
			if err := r.copyStatus(ctx, projectClient, session, hubCopy); err != nil {
				return ctrl.Result{}, err
			}
			deadline := session.DeletionTimestamp.Add(r.cleanupTimeout())
			if wait := deadline.Sub(r.now()); wait > 0 {
				return ctrl.Result{RequeueAfter: wait}, nil
			}
			if err := r.recordCleanupUnconfirmed(ctx, projectClient, session); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.releaseHubCopy(ctx, hubCopy); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("cell did not confirm session cleanup", "hubNamespace", hubNS, "timeout", r.cleanupTimeout())
		}
	}

	endReason := ""
	if !sessionEnded(session) {
		endReason = computev1alpha.InstanceConsoleSessionReasonRevoked
	}
	if err := r.recordLifecycleEvents(ctx, projectClient, session, endReason); err != nil {
		return ctrl.Result{}, err
	}

	patch := client.MergeFrom(session.DeepCopy())
	controllerutil.RemoveFinalizer(session, computev1alpha.InstanceConsoleSessionFinalizer)
	if err := projectClient.Patch(ctx, session, patch); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("failed removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// releaseHubCopy removes the agent's finalizer from a hub copy the cell never
// confirmed, so the copy does not outlive the session. Its cell copy goes with
// it, and the agent's orphan sweep stops any command still running for it.
func (r *InstanceConsoleSessionReconciler) releaseHubCopy(ctx context.Context, hubCopy *computev1alpha.InstanceConsoleSession) error {
	patch := client.MergeFromWithOptions(hubCopy.DeepCopy(), client.MergeFromWithOptimisticLock{})
	if !controllerutil.RemoveFinalizer(hubCopy, computev1alpha.InstanceConsoleSessionAgentFinalizer) {
		return nil
	}
	if err := r.FederationClient.Patch(ctx, hubCopy, patch); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed releasing hub session %s/%s: %w", hubCopy.Namespace, hubCopy.Name, err)
	}
	return nil
}

func (r *InstanceConsoleSessionReconciler) cleanupTimeout() time.Duration {
	if r.CleanupTimeout > 0 {
		return r.CleanupTimeout
	}
	return defaultSessionCleanupTimeout
}

func (r *InstanceConsoleSessionReconciler) claimTimeout() time.Duration {
	if r.ClaimTimeout > 0 {
		return r.ClaimTimeout
	}
	return DefaultSessionClaimTimeout
}

func (r *InstanceConsoleSessionReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// sessionEnded reports whether the session has reached its terminal state.
func sessionEnded(session *computev1alpha.InstanceConsoleSession) bool {
	return apimeta.IsStatusConditionFalse(session.Status.Conditions, computev1alpha.InstanceConsoleSessionReady)
}

// sessionClaimed reports whether a cell has claimed the session.
func sessionClaimed(session *computev1alpha.InstanceConsoleSession) bool {
	return session.Status.Connection != nil || session.Status.StartedAt != nil
}

// sessionStarted reports whether a client connected and the command started.
func sessionStarted(session *computev1alpha.InstanceConsoleSession) bool {
	return session.Status.StartedAt != nil ||
		sessionReadyReason(session) == computev1alpha.InstanceConsoleSessionReasonConnected
}

func sessionReadyReason(session *computev1alpha.InstanceConsoleSession) string {
	if cond := apimeta.FindStatusCondition(session.Status.Conditions, computev1alpha.InstanceConsoleSessionReady); cond != nil {
		return cond.Reason
	}
	return ""
}

// SetupWithManager registers the controller with the multicluster manager.
func (r *InstanceConsoleSessionReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr

	// The finalizer is released only after the hub copy is removed, so a
	// controller without a hub client could never release it.
	if r.FederationClient == nil {
		return fmt.Errorf("instance console session controller requires a federation client")
	}

	b := mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.InstanceConsoleSession{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Named("instance-console-session")

	// The handler keeps the project cluster name the map function resolves; see
	// WorkloadDeploymentFederator.SetupWithManager for why the default handler
	// would lose it.
	if r.FederationCluster != nil {
		preserveClusterName := func(_ multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[*computev1alpha.InstanceConsoleSession, mcreconcile.Request] {
			return mchandler.TypedEnqueueRequestsFromMapFuncWithClusterPreservation(r.mapHubCopyToRequest)
		}
		b = b.WatchesRawSource(milosource.MustNewClusterSource(
			r.FederationCluster,
			&computev1alpha.InstanceConsoleSession{},
			preserveClusterName,
		))
	}

	return b.Complete(r)
}

// mapHubCopyToRequest maps a hub copy to its project session through the
// upstream-owner labels stamped on the copy at delivery.
func (r *InstanceConsoleSessionReconciler) mapHubCopyToRequest(
	ctx context.Context,
	hubCopy *computev1alpha.InstanceConsoleSession,
) []mcreconcile.Request {
	projectNamespace := hubCopy.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	clusterName := projectClusterNameFromLabel(hubCopy.Labels[downstreamclient.UpstreamOwnerClusterNameLabel])
	if projectNamespace == "" || clusterName == "" {
		log.FromContext(ctx).Error(nil, "hub session is missing its upstream-owner labels; dropping event",
			"namespace", hubCopy.Namespace, "name", hubCopy.Name)
		return nil
	}

	if _, err := r.mgr.GetCluster(ctx, multicluster.ClusterName(clusterName)); err != nil {
		log.FromContext(ctx).V(1).Info("project cluster not engaged for hub session event; dropping event",
			"clusterName", clusterName, "namespace", hubCopy.Namespace, "name", hubCopy.Name, "error", err)
		return nil
	}

	return []mcreconcile.Request{{
		ClusterName: multicluster.ClusterName(clusterName),
		Request: ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: projectNamespace, Name: hubCopy.Name},
		},
	}}
}
