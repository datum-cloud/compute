// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"context"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

type sessionStateKind int

const (
	sessionActive sessionStateKind = iota
	sessionEnded
)

// sessionState reports whether a session, by project UID, is still open. A
// session whose cell copy is gone has ended.
func (a *Agent) sessionState(ctx context.Context, uid string) (sessionStateKind, error) {
	cached, err := a.findSession(ctx, uid)
	if err != nil {
		return sessionActive, err
	}
	if cached == nil {
		return sessionEnded, nil
	}
	var session computev1alpha.InstanceConsoleSession
	err = a.cell.Get(ctx, client.ObjectKeyFromObject(cached), &session)
	if apierrors.IsNotFound(err) {
		return sessionEnded, nil
	}
	if err != nil {
		return sessionActive, err
	}
	if sessionUID(&session) != uid || isTerminal(&session) {
		return sessionEnded, nil
	}
	return sessionActive, nil
}

// RunSweeper sweeps for orphaned processes at start and at every sweep
// interval until ctx ends.
func (a *Agent) RunSweeper(ctx context.Context) error {
	ticker := time.NewTicker(a.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		if err := a.sweep(ctx); err != nil && ctx.Err() == nil {
			log.FromContext(ctx).Error(err, "sweep for orphaned session processes")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

type markerPlace struct {
	pod       types.NamespacedName
	podUID    types.UID
	container string
	dir       string
}

// sweep stops processes left behind by sessions that no longer exist or have
// ended, and frees their slots. Slots name the containers sessions ran in, and
// each of those containers is searched for marker files of other sessions.
func (a *Agent) sweep(ctx context.Context) error {
	var leases coordinationv1.LeaseList
	if err := a.cell.List(ctx, &leases, client.InNamespace(a.cfg.Namespace),
		client.MatchingLabels{componentLabel: slotComponent}); err != nil {
		return err
	}
	places := map[markerPlace]struct{}{}
	for i := range leases.Items {
		s := slotFromLease(&leases.Items[i])
		if s.markerDir != "" && s.pod.Name != "" {
			places[markerPlace{pod: s.pod, podUID: s.podUID, container: s.container, dir: s.markerDir}] = struct{}{}
		}
		if a.isLive(s.sessionUID) {
			continue
		}
		state, err := a.sessionState(ctx, s.sessionUID)
		if err != nil || state != sessionEnded {
			continue
		}
		reaped, err := a.reap(ctx, s)
		if err != nil {
			log.FromContext(ctx).Error(err, "reap orphaned session", "session", s.sessionUID)
			continue
		}
		if reaped {
			if err := a.markStopped(ctx, s.session, s.sessionUID); err != nil {
				log.FromContext(ctx).Error(err, "record stopped session", "session", s.sessionUID)
			}
		}
	}
	for place := range places {
		a.sweepContainer(ctx, place)
	}
	return nil
}

func (a *Agent) sweepContainer(ctx context.Context, place markerPlace) {
	logger := log.FromContext(ctx).WithValues("pod", place.pod.String(), "container", place.container)
	var pod corev1.Pod
	if err := a.cell.Get(ctx, place.pod, &pod); err != nil || pod.UID != place.podUID {
		return
	}
	if status := containerStatus(&pod, place.container); status == nil || status.State.Running == nil {
		return
	}
	out, code, err := a.run(ctx, place.pod, place.container, listMarkersCommand(place.dir))
	if err != nil || code != 0 {
		logger.Info("could not list session markers", "exitCode", code, "error", err)
		return
	}
	for _, uid := range strings.Fields(out) {
		if a.isLive(uid) {
			continue
		}
		state, err := a.sessionState(ctx, uid)
		if err != nil || state != sessionEnded {
			continue
		}
		grace := int(a.cfg.KillGrace.Seconds())
		if _, code, err := a.run(ctx, place.pod, place.container, killCommand(place.dir, uid, grace)); err != nil || code != 0 {
			logger.Info("could not stop orphaned session processes", "session", uid, "exitCode", code, "error", err)
			continue
		}
		logger.Info("stopped orphaned session processes", "session", uid)
	}
}
