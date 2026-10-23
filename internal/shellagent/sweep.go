// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"context"
	"slices"
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

// maxMarkerChecks bounds the session UIDs one marker check passes, since each
// becomes a query parameter of the exec request.
const maxMarkerChecks = 64

type markerPlace struct {
	pod       types.NamespacedName
	podUID    types.UID
	container string
	dir       string
}

// sweep stops processes left behind by sessions that no longer exist or have
// ended, and frees their slots. Slots name the containers sessions ran in, and
// each of those containers is checked for marker files of the instance's other
// sessions. The instance's user can write marker files too, so the sweep acts
// only on markers of sessions the cell knows, never on every file it finds.
func (a *Agent) sweep(ctx context.Context) error {
	start := time.Now()
	defer func() { agentSweepSeconds.WithLabelValues(a.cfg.Cell).Observe(time.Since(start).Seconds()) }()
	var leases coordinationv1.LeaseList
	if err := a.cell.List(ctx, &leases, client.InNamespace(a.cfg.Namespace),
		client.MatchingLabels{componentLabel: slotComponent}); err != nil {
		agentSweepErrors.WithLabelValues(a.cfg.Cell).Inc()
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
			a.recordOrphanReaped(orphanSourceSlot)
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
	candidates, err := a.markerCandidates(ctx, place.pod)
	if err != nil {
		logger.Error(err, "list sessions of the instance")
		return
	}
	if len(candidates) == 0 {
		return
	}
	found := map[string]struct{}{}
	for batch := range slices.Chunk(candidates, maxMarkerChecks) {
		out, code, err := a.run(ctx, execPurposeMarkerCheck, place.pod, place.container, findMarkersCommand(place.dir, batch))
		if err != nil || code != 0 {
			logger.Info("could not look for session markers", "exitCode", code, "error", err)
			return
		}
		for _, uid := range strings.Fields(out) {
			if slices.Contains(batch, uid) {
				found[uid] = struct{}{}
			}
		}
	}
	for uid := range found {
		state, err := a.sessionState(ctx, uid)
		if err != nil || state != sessionEnded {
			continue
		}
		grace := int(a.cfg.KillGrace.Seconds())
		if _, code, err := a.run(ctx, execPurposeStop, place.pod, place.container, killCommand(place.dir, uid, grace)); err != nil || code != 0 {
			logger.Info("could not stop orphaned session processes", "session", uid, "exitCode", code, "error", err)
			continue
		}
		a.recordOrphanReaped(orphanSourceMarker)
		logger.Info("stopped orphaned session processes", "session", uid)
	}
}

// markerCandidates returns the UIDs of an instance's sessions that this agent
// is not serving. Their number is bounded by the sessions the instance's
// project may hold, not by what the instance writes.
func (a *Agent) markerCandidates(ctx context.Context, pod types.NamespacedName) ([]string, error) {
	var sessions computev1alpha.InstanceConsoleSessionList
	if err := a.sessions.List(ctx, &sessions, client.InNamespace(pod.Namespace),
		client.MatchingLabels{computev1alpha.InstanceConsoleSessionInstanceNameLabel: pod.Name}); err != nil {
		return nil, err
	}
	var uids []string
	for i := range sessions.Items {
		uid := sessionUID(&sessions.Items[i])
		if uid == "" || a.isLive(uid) || slices.Contains(uids, uid) {
			continue
		}
		uids = append(uids, uid)
	}
	return uids, nil
}
