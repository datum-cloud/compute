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
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	claimRetryInterval   = 5 * time.Second
	cleanupRetryInterval = 5 * time.Second
)

// SetupWithManager reconciles session copies delivered to the cell, and their
// hub copies, which share a namespace and name with them.
func (a *Agent) SetupWithManager(mgr ctrl.Manager, hub cluster.Cluster) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("shell-agent").
		For(&computev1alpha.InstanceConsoleSession{}).
		WatchesRawSource(source.Kind(hub.GetCache(), &computev1alpha.InstanceConsoleSession{},
			&handler.TypedEnqueueRequestForObject[*computev1alpha.InstanceConsoleSession]{})).
		WithOptions(controller.Options{MaxConcurrentReconciles: 16}).
		Complete(a)
}

// Reconcile drives one session: claiming it, ending it at its deadlines,
// taking it over from a lost agent, and stopping its processes before its
// finalizers are released.
func (a *Agent) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cell computev1alpha.InstanceConsoleSession
	if err := a.sessions.Get(ctx, req.NamespacedName, &cell); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	uid := sessionUID(&cell)
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("session", uid))

	var hub computev1alpha.InstanceConsoleSession
	err := a.hub.Get(ctx, req.NamespacedName, &hub)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if apierrors.IsNotFound(err) || sessionUID(&hub) != uid {
		return a.cleanUp(ctx, &cell, nil)
	}
	if !hub.DeletionTimestamp.IsZero() || !cell.DeletionTimestamp.IsZero() {
		return a.cleanUp(ctx, &cell, &hub)
	}
	if uid == "" {
		return ctrl.Result{}, a.rejectUnclaimed(ctx, &hub, reject(computev1alpha.InstanceConsoleSessionReasonInvalid,
			"The session was delivered without its session UID."))
	}
	if isTerminal(&hub) {
		a.forget(uid)
		return a.settle(ctx, &hub)
	}

	owner := endpointOf(&hub)
	switch {
	case owner == "":
		return a.claim(ctx, &cell, &hub)
	case owner != a.EndpointID():
		return a.watchOwner(ctx, &hub, owner)
	}

	a.track(uid, req.NamespacedName)
	now := a.now()
	switch readyReason(&hub) {
	case computev1alpha.InstanceConsoleSessionReasonSessionReady:
		if a.draining.Load() {
			return ctrl.Result{}, a.endUnconnected(ctx, &hub, computev1alpha.InstanceConsoleSessionReasonAgentShutdown)
		}
		if hub.Status.ConnectBefore != nil && now.Before(hub.Status.ConnectBefore.Time) {
			return ctrl.Result{RequeueAfter: hub.Status.ConnectBefore.Sub(now)}, nil
		}
		log.FromContext(ctx).Info("session was not connected in time")
		return ctrl.Result{}, a.endUnconnected(ctx, &hub, computev1alpha.InstanceConsoleSessionReasonNotConnected)
	case computev1alpha.InstanceConsoleSessionReasonConnected:
		if !a.isLive(uid) {
			log.FromContext(ctx).Info("ending a session this agent's previous run served")
			return ctrl.Result{}, a.abandon(ctx, &hub, owner, computev1alpha.InstanceConsoleSessionReasonAgentLost)
		}
		if hub.Status.ExpiresAt != nil && now.Before(hub.Status.ExpiresAt.Time) {
			return ctrl.Result{RequeueAfter: hub.Status.ExpiresAt.Sub(now)}, nil
		}
		log.FromContext(ctx).Info("session expired")
		a.stop(uid, computev1alpha.InstanceConsoleSessionReasonExpired)
	}
	return ctrl.Result{}, nil
}

// claim checks a session and, if it can run, takes a slot and publishes where
// to connect. Agents and the control plane, which ends sessions no cell takes
// in time, race on the hub copy with optimistic concurrency, so a claim never
// lands on a session that has already ended. A cell that does not run the
// session's instance leaves it to the cell that does, or to the control plane.
func (a *Agent) claim(ctx context.Context, cell, hub *computev1alpha.InstanceConsoleSession) (ctrl.Result, error) {
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
	target, rej, err := a.check(ctx, cell)
	if errors.Is(err, errNotInCell) {
		return ctrl.Result{RequeueAfter: claimRetryInterval}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if rej != nil {
		log.FromContext(ctx).Info("refusing session", "reason", rej.reason, "message", rej.message)
		return ctrl.Result{}, a.rejectUnclaimed(ctx, hub, rej)
	}

	uid := sessionUID(hub)
	held, err := a.acquireSlot(ctx, target.instance.UID, &slot{
		sessionUID:  uid,
		session:     client.ObjectKeyFromObject(cell),
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
		return ctrl.Result{}, a.rejectUnclaimed(ctx, hub, reject(
			computev1alpha.InstanceConsoleSessionReasonTooManySessions,
			"The instance already has %d open sessions, the most it allows.", a.cfg.SlotsPerInstance))
	}

	for _, f := range []struct {
		c client.Client
		s *computev1alpha.InstanceConsoleSession
	}{{a.hub, hub}, {a.cell, cell}} {
		if err := addFinalizer(ctx, f.c, f.s); err != nil {
			if relErr := a.releaseSlot(ctx, held); relErr != nil {
				return ctrl.Result{}, relErr
			}
			if apierrors.IsConflict(err) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, err
		}
	}

	now := a.now()
	connectBefore := metav1.NewTime(now.Add(a.cfg.ConnectTimeout))
	hub.Status.Connection = &computev1alpha.InstanceConsoleSessionConnection{
		EndpointID: a.EndpointID(),
		RelayURLs:  a.cfg.RelayURLs,
		Target:     a.cfg.Target,
	}
	hub.Status.ConnectBefore = &connectBefore
	setReady(hub, metav1.ConditionTrue, computev1alpha.InstanceConsoleSessionReasonSessionReady,
		"Connect before the connection deadline.")
	if err := a.hub.Status().Update(ctx, hub); err != nil {
		if relErr := a.releaseSlot(ctx, held); relErr != nil {
			return ctrl.Result{}, relErr
		}
		if apierrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	a.track(uid, client.ObjectKeyFromObject(cell))
	log.FromContext(ctx).Info("claimed session", "pod", target.pod.Name, "container", target.container)
	return ctrl.Result{RequeueAfter: a.cfg.ConnectTimeout}, nil
}

// watchOwner takes over a session in this cell whose agent stopped renewing
// its liveness Lease. A session claimed in another cell has no slot here and
// is left alone.
func (a *Agent) watchOwner(ctx context.Context, hub *computev1alpha.InstanceConsoleSession, owner string) (ctrl.Result, error) {
	held, err := a.slotFor(ctx, sessionUID(hub))
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
	return ctrl.Result{}, a.abandon(ctx, hub, owner, computev1alpha.InstanceConsoleSessionReasonAgentLost)
}

// abandon ends a session no agent is serving: it stops the session's process,
// which a dropped stream leaves running, and records why. The slot is kept if
// the process could not be confirmed stopped, for the sweep to retry.
func (a *Agent) abandon(ctx context.Context, hub *computev1alpha.InstanceConsoleSession, owner, reason string) error {
	uid := sessionUID(hub)
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
	if _, err := a.end(ctx, client.ObjectKeyFromObject(hub), reason, endMessage(reason), nil,
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
func (a *Agent) endUnconnected(ctx context.Context, hub *computev1alpha.InstanceConsoleSession, reason string) error {
	uid := sessionUID(hub)
	me := a.EndpointID()
	ended, err := a.end(ctx, client.ObjectKeyFromObject(hub), reason, endMessage(reason), nil,
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

func (a *Agent) rejectUnclaimed(ctx context.Context, hub *computev1alpha.InstanceConsoleSession, rej *rejection) error {
	_, err := a.end(ctx, client.ObjectKeyFromObject(hub), rej.reason, rej.message, nil,
		func(s *computev1alpha.InstanceConsoleSession) bool { return s.Status.Connection == nil })
	return err
}

// settle frees the slot of an ended session once its process is stopped,
// unless a live agent is still ending it.
func (a *Agent) settle(ctx context.Context, hub *computev1alpha.InstanceConsoleSession) (ctrl.Result, error) {
	uid := sessionUID(hub)
	if a.isLive(uid) {
		return ctrl.Result{RequeueAfter: cleanupRetryInterval}, nil
	}
	held, err := a.slotFor(ctx, uid)
	if err != nil || held == nil {
		return ctrl.Result{}, err
	}
	if owner := endpointOf(hub); owner != "" && owner != a.EndpointID() {
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

// cleanUp handles a session whose hub or cell copy is being deleted, or whose
// hub copy is gone. The session is revoked if it is still open, and the
// finalizers are released only once its process is stopped.
func (a *Agent) cleanUp(ctx context.Context, cell, hub *computev1alpha.InstanceConsoleSession) (ctrl.Result, error) {
	uid := sessionUID(cell)
	me := a.EndpointID()
	responsible := true
	if hub == nil && a.isLive(uid) {
		a.stop(uid, computev1alpha.InstanceConsoleSessionReasonRevoked)
	}
	if hub != nil && !isTerminal(hub) {
		switch owner := endpointOf(hub); {
		case owner == me && a.isLive(uid):
			log.FromContext(ctx).Info("revoking session")
			a.stop(uid, computev1alpha.InstanceConsoleSessionReasonRevoked)
		case owner == me:
			if err := a.endUnconnected(ctx, hub, computev1alpha.InstanceConsoleSessionReasonRevoked); err != nil {
				return ctrl.Result{}, err
			}
		case owner == "":
			if err := a.rejectUnclaimed(ctx, hub, reject(computev1alpha.InstanceConsoleSessionReasonRevoked,
				"%s", endMessage(computev1alpha.InstanceConsoleSessionReasonRevoked))); err != nil {
				return ctrl.Result{}, err
			}
		default:
			held, err := a.slotFor(ctx, uid)
			if err != nil {
				return ctrl.Result{}, err
			}
			if held == nil {
				responsible = false
				break
			}
			alive, err := a.agentAlive(ctx, owner)
			if err != nil {
				return ctrl.Result{}, err
			}
			if alive {
				return ctrl.Result{RequeueAfter: cleanupRetryInterval}, nil
			}
			if err := a.abandon(ctx, hub, owner, computev1alpha.InstanceConsoleSessionReasonRevoked); err != nil {
				return ctrl.Result{}, err
			}
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

	if hub != nil && responsible {
		if err := removeFinalizer(ctx, a.hub, client.ObjectKeyFromObject(hub)); err != nil {
			return ctrl.Result{}, err
		}
	}
	if !cell.DeletionTimestamp.IsZero() || hub == nil {
		return ctrl.Result{}, removeFinalizer(ctx, a.cell, client.ObjectKeyFromObject(cell))
	}
	return ctrl.Result{}, nil
}

// end records a terminal reason on the hub copy if the session has not ended
// yet and guard still holds for its latest state. It reports whether this
// call ended the session.
func (a *Agent) end(ctx context.Context, key types.NamespacedName, reason, message string, exitCode *int32,
	guard func(*computev1alpha.InstanceConsoleSession) bool) (bool, error) {
	ended := false
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var s computev1alpha.InstanceConsoleSession
		if err := a.hub.Get(ctx, key, &s); err != nil {
			return err
		}
		if isTerminal(&s) || guard != nil && !guard(&s) {
			return nil
		}
		now := metav1.NewTime(a.now())
		s.Status.EndedAt = &now
		s.Status.ExitCode = exitCode
		setReady(&s, metav1.ConditionFalse, reason, message)
		if err := a.hub.Status().Update(ctx, &s); err != nil {
			return err
		}
		ended = true
		return nil
	})
	return ended, client.IgnoreNotFound(err)
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
