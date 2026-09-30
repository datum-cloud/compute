// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"context"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	claimRetryInterval   = 5 * time.Second
	cleanupRetryInterval = 5 * time.Second
)

// SetupWithManager reconciles session copies delivered to the cell. The agent
// writes status only on these copies; Karmada carries it back to the hub.
func (a *Agent) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("shell-agent").
		For(&computev1alpha.InstanceConsoleSession{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 16}).
		Complete(a)
}

// Reconcile drives one session: claiming it, ending it at its deadlines or
// when it is revoked, taking it over from a lost agent, and stopping its
// processes before its finalizer is released.
func (a *Agent) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var session computev1alpha.InstanceConsoleSession
	if err := a.sessions.Get(ctx, req.NamespacedName, &session); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("session", sessionUID(&session)))
	return a.reconcileSession(ctx, &session)
}

func (a *Agent) reconcileSession(ctx context.Context, session *computev1alpha.InstanceConsoleSession) (ctrl.Result, error) {
	uid := sessionUID(session)
	if !session.DeletionTimestamp.IsZero() {
		return a.cleanUp(ctx, session)
	}
	if uid == "" {
		return ctrl.Result{}, a.rejectUnclaimed(ctx, session, reject(computev1alpha.InstanceConsoleSessionReasonInvalid,
			"The session was delivered without its session UID."))
	}
	if revoked(session) && !isTerminal(session) {
		return a.revoke(ctx, session)
	}
	if isTerminal(session) {
		a.forget(uid)
		return a.settle(ctx, session)
	}

	owner := endpointOf(session)
	switch {
	case owner == "":
		return a.claim(ctx, session)
	case owner != a.EndpointID():
		return a.watchOwner(ctx, session, owner)
	}

	a.track(uid, client.ObjectKeyFromObject(session))
	now := a.now()
	switch readyReason(session) {
	case computev1alpha.InstanceConsoleSessionReasonSessionReady:
		if a.draining.Load() {
			return ctrl.Result{}, a.endUnconnected(ctx, session, computev1alpha.InstanceConsoleSessionReasonAgentShutdown)
		}
		if session.Status.ConnectBefore != nil && now.Before(session.Status.ConnectBefore.Time) {
			return ctrl.Result{RequeueAfter: session.Status.ConnectBefore.Sub(now)}, nil
		}
		log.FromContext(ctx).Info("session was not connected in time")
		return ctrl.Result{}, a.endUnconnected(ctx, session, computev1alpha.InstanceConsoleSessionReasonNotConnected)
	case computev1alpha.InstanceConsoleSessionReasonConnected:
		if !a.isLive(uid) {
			log.FromContext(ctx).Info("ending a session this agent's previous run served")
			return ctrl.Result{}, a.abandon(ctx, session, owner, computev1alpha.InstanceConsoleSessionReasonAgentLost)
		}
		if session.Status.ExpiresAt != nil && now.Before(session.Status.ExpiresAt.Time) {
			return ctrl.Result{RequeueAfter: session.Status.ExpiresAt.Sub(now)}, nil
		}
		log.FromContext(ctx).Info("session expired")
		a.stop(uid, computev1alpha.InstanceConsoleSessionReasonExpired)
	}
	return ctrl.Result{}, nil
}

// claim checks a session and, if it can run, takes a slot and publishes where
// to connect. The cell's agents race on the cell copy with optimistic
// concurrency, so exactly one claim lands. A cell that does not run the
// session's instance leaves it to the cell that does, or to the control plane.
func (a *Agent) claim(ctx context.Context, session *computev1alpha.InstanceConsoleSession) (ctrl.Result, error) {
	if !a.claiming() || a.openCount() >= a.cfg.MaxOpenSessions {
		return ctrl.Result{RequeueAfter: claimRetryInterval}, nil
	}
	ready, err := a.endpointReady(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: claimRetryInterval}, nil
	}
	target, rej, err := a.check(ctx, session)
	if errors.Is(err, errNotInCell) {
		return ctrl.Result{RequeueAfter: claimRetryInterval}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if rej != nil {
		log.FromContext(ctx).Info("refusing session", "reason", rej.reason, "message", rej.message)
		return ctrl.Result{}, a.rejectUnclaimed(ctx, session, rej)
	}

	uid := sessionUID(session)
	held, err := a.acquireSlot(ctx, target.instance.UID, &slot{
		sessionUID:  uid,
		session:     client.ObjectKeyFromObject(session),
		pod:         client.ObjectKeyFromObject(target.pod),
		podUID:      target.pod.UID,
		container:   target.container,
		containerID: target.containerID,
		markerDir:   target.markerDir,
		endpointID:  a.EndpointID(),
	})
	if errors.Is(err, errClaimInProgress) {
		return ctrl.Result{RequeueAfter: claimRetryInterval}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if held == nil {
		return ctrl.Result{}, a.rejectUnclaimed(ctx, session, reject(
			computev1alpha.InstanceConsoleSessionReasonTooManySessions,
			"The instance already has %d open sessions, the most it allows.", a.cfg.SlotsPerInstance))
	}

	claimed := session.DeepCopy()
	if err := addFinalizer(ctx, a.cell, claimed); err != nil {
		return a.abortClaim(ctx, client.ObjectKeyFromObject(session), held, err)
	}
	now := a.now()
	connectBefore := metav1.NewTime(now.Add(a.cfg.ConnectTimeout))
	claimed.Status.Connection = &computev1alpha.InstanceConsoleSessionConnection{
		EndpointID: a.EndpointID(),
		RelayURLs:  a.cfg.RelayURLs,
		Target:     a.cfg.Target,
	}
	claimed.Status.ConnectBefore = &connectBefore
	setReady(claimed, metav1.ConditionTrue, computev1alpha.InstanceConsoleSessionReasonSessionReady,
		"Connect before the connection deadline.")
	if err := a.cell.Status().Update(ctx, claimed); err != nil {
		return a.abortClaim(ctx, client.ObjectKeyFromObject(session), held, err)
	}
	a.track(uid, client.ObjectKeyFromObject(session))
	log.FromContext(ctx).Info("claimed session", "pod", target.pod.Name, "container", target.container)
	return ctrl.Result{RequeueAfter: a.cfg.ConnectTimeout}, nil
}

// abortClaim frees the slot of a claim that did not land. A conflict means
// the session changed, most likely because another agent claimed it, and the
// retry sees the change. The cache can also lag this agent's own claim, and
// the slot of a claim that already stands is kept.
func (a *Agent) abortClaim(ctx context.Context, key types.NamespacedName, held *slot, err error) (ctrl.Result, error) {
	if apierrors.IsConflict(err) {
		var current computev1alpha.InstanceConsoleSession
		if getErr := a.cell.Get(ctx, key, &current); getErr != nil {
			return ctrl.Result{}, client.IgnoreNotFound(getErr)
		}
		if endpointOf(&current) == a.EndpointID() && !isTerminal(&current) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
	}
	if relErr := a.releaseSlot(ctx, held); relErr != nil {
		return ctrl.Result{}, relErr
	}
	if apierrors.IsConflict(err) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	return ctrl.Result{}, err
}

// watchOwner takes over a session whose agent stopped renewing its liveness
// Lease. A session with no slot has nothing left to take over.
func (a *Agent) watchOwner(ctx context.Context, session *computev1alpha.InstanceConsoleSession, owner string) (ctrl.Result, error) {
	held, err := a.slotFor(ctx, sessionUID(session))
	if err != nil || held == nil {
		return ctrl.Result{}, err
	}
	alive, err := a.agentAlive(ctx, owner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if alive {
		return ctrl.Result{RequeueAfter: LeaseDuration}, nil
	}
	log.FromContext(ctx).Info("taking over a session from a lost agent", "agent", owner)
	return ctrl.Result{}, a.abandon(ctx, session, owner, computev1alpha.InstanceConsoleSessionReasonAgentLost)
}

// abandon ends a session no agent is serving: it stops the session's process,
// which a dropped stream leaves running, and records why. The slot is kept if
// the process could not be confirmed stopped, for the sweep to retry.
func (a *Agent) abandon(ctx context.Context, session *computev1alpha.InstanceConsoleSession, owner, reason string) error {
	uid := sessionUID(session)
	held, err := a.slotFor(ctx, uid)
	if err != nil {
		return err
	}
	stopped := true
	if held != nil {
		if stopped, err = a.stopProcesses(ctx, held); err != nil {
			return err
		}
	}
	if _, err := a.end(ctx, client.ObjectKeyFromObject(session), reason, endMessage(reason), nil,
		func(s *computev1alpha.InstanceConsoleSession) bool { return endpointOf(s) == owner }); err != nil {
		return err
	}
	a.forget(uid)
	if stopped {
		return a.releaseSlot(ctx, held)
	}
	return nil
}

// endUnconnected ends a session this agent claimed but no client connected
// to. No process was started, so its slot is freed at once.
func (a *Agent) endUnconnected(ctx context.Context, session *computev1alpha.InstanceConsoleSession, reason string) error {
	uid := sessionUID(session)
	me := a.EndpointID()
	ended, err := a.end(ctx, client.ObjectKeyFromObject(session), reason, endMessage(reason), nil,
		func(s *computev1alpha.InstanceConsoleSession) bool {
			return endpointOf(s) == me && readyReason(s) == computev1alpha.InstanceConsoleSessionReasonSessionReady
		})
	if err != nil || !ended {
		return err
	}
	a.forget(uid)
	held, err := a.slotFor(ctx, uid)
	if err != nil {
		return err
	}
	return a.releaseSlot(ctx, held)
}

func (a *Agent) rejectUnclaimed(ctx context.Context, session *computev1alpha.InstanceConsoleSession, rej *rejection) error {
	_, err := a.end(ctx, client.ObjectKeyFromObject(session), rej.reason, rej.message, nil,
		func(s *computev1alpha.InstanceConsoleSession) bool { return s.Status.Connection == nil })
	return err
}

// settle frees the slot of an ended session once its process is stopped,
// unless a live agent is still ending it.
func (a *Agent) settle(ctx context.Context, session *computev1alpha.InstanceConsoleSession) (ctrl.Result, error) {
	uid := sessionUID(session)
	if a.isLive(uid) {
		return ctrl.Result{RequeueAfter: cleanupRetryInterval}, nil
	}
	held, err := a.slotFor(ctx, uid)
	if err != nil || held == nil {
		return ctrl.Result{}, err
	}
	if owner := endpointOf(session); owner != "" && owner != a.EndpointID() {
		if alive, err := a.agentAlive(ctx, owner); err != nil || alive {
			return ctrl.Result{RequeueAfter: LeaseDuration}, err
		}
	}
	reaped, err := a.reap(ctx, held)
	if err != nil || !reaped {
		return ctrl.Result{RequeueAfter: cleanupRetryInterval}, err
	}
	return ctrl.Result{}, nil
}

// revoke ends a session that is still open because the control plane revoked
// it or its copy is being deleted. The end is recorded only once the
// session's processes have been stopped, so the control plane can take it as
// confirmation that the command is gone.
func (a *Agent) revoke(ctx context.Context, session *computev1alpha.InstanceConsoleSession) (ctrl.Result, error) {
	uid := sessionUID(session)
	reason := computev1alpha.InstanceConsoleSessionReasonRevoked
	switch owner := endpointOf(session); {
	case owner == a.EndpointID() && a.isLive(uid):
		log.FromContext(ctx).Info("revoking session")
		a.stop(uid, reason)
	case owner == a.EndpointID():
		if err := a.endUnconnected(ctx, session, reason); err != nil {
			return ctrl.Result{}, err
		}
	case owner == "":
		if err := a.rejectUnclaimed(ctx, session, reject(reason, "%s", endMessage(reason))); err != nil {
			return ctrl.Result{}, err
		}
	default:
		held, err := a.slotFor(ctx, uid)
		if err != nil || held == nil {
			return ctrl.Result{}, err
		}
		alive, err := a.agentAlive(ctx, owner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if alive {
			return ctrl.Result{RequeueAfter: cleanupRetryInterval}, nil
		}
		if err := a.abandon(ctx, session, owner, reason); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// cleanUp handles a session whose cell copy is being deleted. The session is
// revoked if it is still open, and the finalizer is released only once its
// process is stopped.
func (a *Agent) cleanUp(ctx context.Context, session *computev1alpha.InstanceConsoleSession) (ctrl.Result, error) {
	uid := sessionUID(session)
	if !isTerminal(session) {
		if res, err := a.revoke(ctx, session); err != nil || res.RequeueAfter > 0 {
			return res, err
		}
	}
	if a.isLive(uid) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	held, err := a.slotFor(ctx, uid)
	if err != nil {
		return ctrl.Result{}, err
	}
	if held != nil {
		reaped, err := a.reap(ctx, held)
		if err != nil || !reaped {
			return ctrl.Result{RequeueAfter: cleanupRetryInterval}, err
		}
	}
	a.forget(uid)
	return ctrl.Result{}, removeFinalizer(ctx, a.cell, client.ObjectKeyFromObject(session))
}

// end records a terminal reason on the cell copy if the session has not ended
// yet and guard still holds for its latest state. It reports whether this
// call ended the session.
func (a *Agent) end(ctx context.Context, key types.NamespacedName, reason, message string, exitCode *int32,
	guard func(*computev1alpha.InstanceConsoleSession) bool) (bool, error) {
	ended := false
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var s computev1alpha.InstanceConsoleSession
		if err := a.cell.Get(ctx, key, &s); err != nil {
			return err
		}
		if isTerminal(&s) || guard != nil && !guard(&s) {
			return nil
		}
		now := metav1.NewTime(a.now())
		s.Status.EndedAt = &now
		s.Status.ExitCode = exitCode
		setReady(&s, metav1.ConditionFalse, reason, message)
		if err := a.cell.Status().Update(ctx, &s); err != nil {
			return err
		}
		ended = true
		return nil
	})
	return ended, client.IgnoreNotFound(err)
}

// revoked reports whether the control plane asked the cell to end the
// session.
func revoked(s *computev1alpha.InstanceConsoleSession) bool {
	_, ok := s.Annotations[computev1alpha.InstanceConsoleSessionRevokeAnnotation]
	return ok
}

func addFinalizer(ctx context.Context, c client.Client, s *computev1alpha.InstanceConsoleSession) error {
	if controllerutil.ContainsFinalizer(s, computev1alpha.InstanceConsoleSessionAgentFinalizer) {
		return nil
	}
	base := s.DeepCopy()
	controllerutil.AddFinalizer(s, computev1alpha.InstanceConsoleSessionAgentFinalizer)
	return c.Patch(ctx, s, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func removeFinalizer(ctx context.Context, c client.Client, key types.NamespacedName) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var s computev1alpha.InstanceConsoleSession
		if err := c.Get(ctx, key, &s); err != nil {
			return err
		}
		if !controllerutil.ContainsFinalizer(&s, computev1alpha.InstanceConsoleSessionAgentFinalizer) {
			return nil
		}
		base := s.DeepCopy()
		controllerutil.RemoveFinalizer(&s, computev1alpha.InstanceConsoleSessionAgentFinalizer)
		return c.Patch(ctx, &s, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	})
	return client.IgnoreNotFound(err)
}
