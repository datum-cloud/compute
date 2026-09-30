// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

	karmadaworkv1alpha2 "github.com/karmada-io/api/work/v1alpha2"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
	milosource "go.miloapis.com/milo/pkg/multicluster-runtime/source"
)

const (
	defaultSessionCleanupTimeout = 5 * time.Minute

	// sessionMemberClusterLabel names, on a hub copy, the Karmada member
	// cluster the session's hub WorkloadDeployment is scheduled to, which is
	// the only cell that runs the instance. Status aggregation takes a claim or
	// an end only from that cluster, and the controller watches that cluster
	// keeps reporting while the session is open.
	sessionMemberClusterLabel = "compute.datumapis.com/member-cluster"

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
// the same cells. The shell agent there writes status only on its cell copy;
// Karmada reflects it back onto the hub copy, and this controller copies it onto
// the project session. The controller never writes hub status, which
// aggregation would overwrite: what it decides itself goes on the project
// session, and it ends a session on the cell by revoking or deleting the hub
// copy.
type InstanceConsoleSessionReconciler struct {
	mgr mcmanager.Manager

	// FederationClient creates, revokes and deletes hub copies on the
	// federation hub.
	FederationClient client.Client

	// FederationCluster watches hub copies, so status Karmada reflects from the
	// cell reaches the project session without waiting for a resync. It is nil in unit tests.
	FederationCluster cluster.Cluster

	// ReportingInstance identifies this replica on the events it records.
	ReportingInstance string

	// CleanupTimeout is how long a deleted session that is open on the cell
	// waits for the cell to report its end. Zero means five minutes.
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

// reconcileDelivery delivers a session that has not ended and mirrors the
// status Karmada reflects onto its hub copy. It ends the session when its
// instance is gone, when no cell claims it by its claim deadline, and when its
// hub copy goes away after a cell claimed it.
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
	if hubCopy != nil {
		if err := r.copyStatus(ctx, projectClient, session, hubCopy); err != nil {
			return ctrl.Result{}, err
		}
		if sessionEnded(session) {
			return ctrl.Result{}, nil
		}
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

	if hubCopy == nil || !hubCopy.DeletionTimestamp.IsZero() {
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
		if hubCopy != nil {
			// The copy's name is taken until it is gone, and its removal
			// requeues the session through the hub watch.
			return ctrl.Result{RequeueAfter: claimDeadline.Sub(r.now())}, nil
		}
		if hubCopy, err = r.deliver(ctx, clusterName, projectClient, session, &instance); err != nil {
			return ctrl.Result{}, err
		}
	}

	if sessionClaimed(session) {
		reporting, err := r.memberReports(ctx, hubCopy)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !reporting {
			log.FromContext(ctx).Info("the cell that claimed the session stopped reporting it",
				"memberCluster", hubCopy.Labels[sessionMemberClusterLabel])
			return ctrl.Result{}, r.endSession(ctx, projectClient, session,
				computev1alpha.InstanceConsoleSessionReasonAgentLost,
				"The cell running the session stopped reporting it.")
		}
		return ctrl.Result{}, nil
	}
	if wait := claimDeadline.Sub(r.now()); wait > 0 {
		return ctrl.Result{RequeueAfter: wait}, nil
	}
	// A cell may have claimed the session in the moment before this decision,
	// but no client can have connected: clients learn where to connect only
	// from the claim on the project session. Cleanup revokes the cell's claim.
	log.FromContext(ctx).Info("ending session no cell claimed in time")
	return ctrl.Result{}, r.endSession(ctx, projectClient, session,
		computev1alpha.InstanceConsoleSessionReasonUnavailable, r.unavailableMessage())
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

	memberCluster, err := r.scheduledMember(ctx, &hubDeployment)
	if err != nil {
		return nil, nil, err
	}

	labels := map[string]string{
		locationLabel:                                          location,
		sessionMemberClusterLabel:                              memberCluster,
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

// copyStatus mirrors the status Karmada reflected onto the hub copy. An ended
// session keeps its status, because a terminal reason never changes, and a
// status that says less than the session's, such as a fresh copy's empty one,
// never replaces it. The weighing matches the hub copy's status aggregation.
func (r *InstanceConsoleSessionReconciler) copyStatus(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
	hubCopy *computev1alpha.InstanceConsoleSession,
) error {
	if equality.Semantic.DeepEqual(session.Status, hubCopy.Status) {
		return nil
	}
	if sessionEnded(session) {
		return r.copyCleanupTime(ctx, projectClient, session, hubCopy)
	}
	if claimed := session.Status.Connection; claimed != nil &&
		(hubCopy.Status.Connection == nil || hubCopy.Status.Connection.EndpointID != claimed.EndpointID) {
		return nil
	}
	hubWeight := statusWeight(&hubCopy.Status)
	if hubWeight == 0 || hubWeight < statusWeight(&session.Status) {
		return nil
	}
	session.Status = *hubCopy.Status.DeepCopy()
	if err := projectClient.Status().Update(ctx, session); err != nil {
		return fmt.Errorf("failed copying hub status to session: %w", err)
	}
	return nil
}

// copyCleanupTime records on an ended session when the cell confirmed its
// processes stopped, once the hub copy reports the same end with that time.
func (r *InstanceConsoleSessionReconciler) copyCleanupTime(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
	hubCopy *computev1alpha.InstanceConsoleSession,
) error {
	if session.Status.EndedAt != nil || !cleanupConfirmed(hubCopy) ||
		sessionReadyReason(hubCopy) != sessionReadyReason(session) {
		return nil
	}
	session.Status.EndedAt = hubCopy.Status.EndedAt.DeepCopy()
	if err := projectClient.Status().Update(ctx, session); err != nil {
		return fmt.Errorf("failed recording session cleanup time: %w", err)
	}
	return nil
}

// endSession records an end the control plane decided on the project session.
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

// finalize removes the hub copy and releases the project session. A session
// still open on the cell is revoked first, and the copy is removed once the
// cell reports the session's end, or once the cleanup timeout passes without
// that report. Karmada removes a cell copy without waiting for it to go, so a
// removed hub copy can no longer report anything.
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
			if err := r.copyStatus(ctx, projectClient, session, hubCopy); err != nil {
				return ctrl.Result{}, err
			}
			if openOnCell(hubCopy) {
				reporting, err := r.memberReports(ctx, hubCopy)
				if err != nil {
					return ctrl.Result{}, err
				}
				deadline := session.DeletionTimestamp.Add(r.cleanupTimeout())
				wait := deadline.Sub(r.now())
				if reporting {
					if err := r.revokeHubCopy(ctx, hubCopy); err != nil {
						return ctrl.Result{}, err
					}
				}
				if reporting && wait > 0 {
					return ctrl.Result{RequeueAfter: wait}, nil
				}
				if err := r.recordCleanupUnconfirmed(ctx, projectClient, session); err != nil {
					return ctrl.Result{}, err
				}
				logger.Info("cell did not confirm session cleanup", "hubNamespace", hubNS,
					"memberReporting", reporting, "timeout", r.cleanupTimeout())
			}
			if err := r.FederationClient.Delete(ctx, hubCopy, client.Preconditions{UID: &hubCopy.UID}); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, fmt.Errorf("failed deleting hub session %s/%s: %w", hubNS, session.Name, err)
			}
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

// revokeHubCopy asks the cell to end the session. The annotation reaches the
// cell copy through propagation, and the agent reports the end through status
// only after it has stopped the session's processes.
func (r *InstanceConsoleSessionReconciler) revokeHubCopy(ctx context.Context, hubCopy *computev1alpha.InstanceConsoleSession) error {
	if _, ok := hubCopy.Annotations[computev1alpha.InstanceConsoleSessionRevokeAnnotation]; ok {
		return nil
	}
	patch := client.MergeFrom(hubCopy.DeepCopy())
	if hubCopy.Annotations == nil {
		hubCopy.Annotations = map[string]string{}
	}
	hubCopy.Annotations[computev1alpha.InstanceConsoleSessionRevokeAnnotation] = r.now().UTC().Format(time.RFC3339)
	if err := r.FederationClient.Patch(ctx, hubCopy, patch); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed revoking hub session %s/%s: %w", hubCopy.Namespace, hubCopy.Name, err)
	}
	return nil
}

// openOnCell reports whether a cell claimed the session and has not confirmed
// that its processes are stopped. Only such a session can have processes to
// stop. One no cell claimed has none, because a client learns where to
// connect only from the claim.
func openOnCell(hubCopy *computev1alpha.InstanceConsoleSession) bool {
	return sessionClaimed(hubCopy) && !cleanupConfirmed(hubCopy)
}

// cleanupConfirmed reports whether the cell recorded the session's end together
// with the time its processes were confirmed stopped. The agent records the
// end at once so the client learns of it, but sets endedAt only once nothing
// the session started is left running.
func cleanupConfirmed(s *computev1alpha.InstanceConsoleSession) bool {
	return sessionEnded(s) && s.Status.EndedAt != nil
}

// errNoSingleMember means the hub WorkloadDeployment is not scheduled to
// exactly one member cluster, so no single cell can be trusted with the
// session.
var errNoSingleMember = errors.New("not scheduled to exactly one member cluster")

// scheduledMember returns the one member cluster Karmada scheduled the hub
// WorkloadDeployment to. The scheduler writes this, not a cell, so it names
// the cell that runs the deployment's instances whatever any cell reports.
func (r *InstanceConsoleSessionReconciler) scheduledMember(
	ctx context.Context,
	hubDeployment *computev1alpha.WorkloadDeployment,
) (string, error) {
	var binding karmadaworkv1alpha2.ResourceBinding
	key := types.NamespacedName{Namespace: hubDeployment.Namespace, Name: bindingName(kindWorkloadDeployment, hubDeployment.Name)}
	if err := r.FederationClient.Get(ctx, key, &binding); err != nil {
		return "", fmt.Errorf("failed getting the placement of hub WorkloadDeployment %s/%s: %w",
			hubDeployment.Namespace, hubDeployment.Name, err)
	}
	if len(binding.Spec.Clusters) != 1 {
		return "", fmt.Errorf("hub WorkloadDeployment %s/%s is %w (%d)",
			hubDeployment.Namespace, hubDeployment.Name, errNoSingleMember, len(binding.Spec.Clusters))
	}
	return binding.Spec.Clusters[0].Name, nil
}

// memberReports reports whether the member cluster a hub copy is bound to
// still reports the session's status to Karmada. A cell that was deregistered,
// lost the deployment to another cell, or had its copy removed stops
// reporting, and Karmada then keeps the hub copy's last status forever.
func (r *InstanceConsoleSessionReconciler) memberReports(
	ctx context.Context,
	hubCopy *computev1alpha.InstanceConsoleSession,
) (bool, error) {
	member := hubCopy.Labels[sessionMemberClusterLabel]
	if member == "" {
		return false, nil
	}
	var binding karmadaworkv1alpha2.ResourceBinding
	key := types.NamespacedName{Namespace: hubCopy.Namespace, Name: bindingName(kindInstanceConsoleSession, hubCopy.Name)}
	if err := r.FederationClient.Get(ctx, key, &binding); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed getting the binding of hub session %s/%s: %w", hubCopy.Namespace, hubCopy.Name, err)
	}
	for _, item := range binding.Status.AggregatedStatus {
		if item.ClusterName == member && item.Status != nil {
			return true, nil
		}
	}
	return false, nil
}

// bindingName is the name Karmada gives the ResourceBinding of a namespaced
// resource template.
func bindingName(kind, name string) string {
	return strings.ToLower(strings.ReplaceAll(name, ":", ".") + "-" + kind)
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

// statusWeight ranks how much a session status says: a claim, then an end,
// then any condition, then nothing.
func statusWeight(status *computev1alpha.InstanceConsoleSessionStatus) int {
	switch {
	case status.Connection != nil:
		return 3
	case apimeta.IsStatusConditionFalse(status.Conditions, computev1alpha.InstanceConsoleSessionReady):
		return 2
	case len(status.Conditions) > 0:
		return 1
	}
	return 0
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
