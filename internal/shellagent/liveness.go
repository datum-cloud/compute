// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	// LeaseDuration is how long an agent's liveness Lease holds without a
	// renewal before other agents take over its sessions.
	LeaseDuration = 15 * time.Second
	// LeaseRenewInterval is how often an agent renews its liveness Lease.
	LeaseRenewInterval = 5 * time.Second

	livenessComponent = "shell-agent-liveness"

	// annotationClaiming on an agent's liveness Lease says whether the agent
	// takes new sessions, so the cell's agents spread claims among only those
	// that do.
	annotationClaiming = annotationPrefix + "claiming"
)

// agentLeaseName names an agent's liveness Lease by the full SHA-256 of its
// endpoint ID. Lease names are DNS subdomains, which fit all 64 hex digits, so
// two agents never share a Lease.
func agentLeaseName(endpointID string) string {
	sum := sha256.Sum256([]byte(endpointID))
	return "agent-" + hex.EncodeToString(sum[:])
}

// RunLiveness renews the agent's liveness Lease until ctx ends.
func (a *Agent) RunLiveness(ctx context.Context) error {
	ticker := time.NewTicker(LeaseRenewInterval)
	defer ticker.Stop()
	for {
		if err := a.renewLease(ctx); err != nil && ctx.Err() == nil {
			log.FromContext(ctx).Error(err, "renew liveness lease")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (a *Agent) renewLease(ctx context.Context) error {
	id := a.EndpointID()
	if id == "" {
		return nil
	}
	claiming := strconv.FormatBool(a.acceptsClaims(ctx))
	now := metav1.NewMicroTime(a.now())
	key := client.ObjectKey{Namespace: a.cfg.Namespace, Name: agentLeaseName(id)}
	var lease coordinationv1.Lease
	err := a.cell.Get(ctx, key, &lease)
	if apierrors.IsNotFound(err) {
		return a.cell.Create(ctx, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:        key.Name,
				Namespace:   key.Namespace,
				Labels:      map[string]string{componentLabel: livenessComponent},
				Annotations: map[string]string{annotationClaiming: claiming},
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       ptr.To(a.incarnation),
				LeaseDurationSeconds: ptr.To(int32(LeaseDuration.Seconds())),
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		})
	}
	if err != nil {
		return err
	}
	metav1.SetMetaDataAnnotation(&lease.ObjectMeta, annotationClaiming, claiming)
	lease.Spec.HolderIdentity = ptr.To(a.incarnation)
	lease.Spec.LeaseDurationSeconds = ptr.To(int32(LeaseDuration.Seconds()))
	lease.Spec.RenewTime = &now
	return a.cell.Update(ctx, &lease)
}

// acceptsClaims reports whether the agent would claim a new session now.
func (a *Agent) acceptsClaims(ctx context.Context) bool {
	if !a.claiming() || a.openCount() >= a.cfg.MaxOpenSessions {
		return false
	}
	ready, err := a.endpointReady(ctx)
	return err == nil && ready
}

// releaseLease deletes a liveness Lease once its agent no longer serves
// sessions under that endpoint ID.
func (a *Agent) releaseLease(ctx context.Context, endpointID string) {
	err := a.cell.Delete(ctx, &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Name:      agentLeaseName(endpointID),
		Namespace: a.cfg.Namespace,
	}})
	if client.IgnoreNotFound(err) != nil {
		log.FromContext(ctx).Error(err, "release liveness lease")
	}
}

// agentAlive reports whether the agent serving endpointID renewed its
// liveness Lease within the Lease's duration.
func (a *Agent) agentAlive(ctx context.Context, endpointID string) (bool, error) {
	if endpointID == a.EndpointID() {
		return true, nil
	}
	var lease coordinationv1.Lease
	err := a.cell.Get(ctx, client.ObjectKey{Namespace: a.cfg.Namespace, Name: agentLeaseName(endpointID)}, &lease)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return leaseCurrent(&lease, a.now()), nil
}

func leaseCurrent(lease *coordinationv1.Lease, now time.Time) bool {
	if lease.Spec.RenewTime == nil || lease.Spec.LeaseDurationSeconds == nil {
		return false
	}
	return now.Before(lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second))
}

// claimDelay is how long the agent leaves a new session to the agents ahead
// of it before claiming it itself.
//
// Every agent that takes claims ranks the cell's claiming agents for each
// session by a hash of the session UID and the agent, so the cell's agents
// agree on an order that differs from session to session and spreads sessions
// evenly across them. The first agent claims at once, and each one after it
// waits one more ClaimStagger from the session's creation, which lets a later
// agent take a session its predecessors cannot serve. Claims still race with
// optimistic concurrency, so an order two agents see differently costs only a
// lost claim.
func (a *Agent) claimDelay(ctx context.Context, session *computev1alpha.InstanceConsoleSession) (time.Duration, error) {
	if a.cfg.ClaimStagger <= 0 {
		return 0, nil
	}
	var leases coordinationv1.LeaseList
	if err := a.cell.List(ctx, &leases, client.InNamespace(a.cfg.Namespace),
		client.MatchingLabels{componentLabel: livenessComponent}); err != nil {
		return 0, err
	}
	uid := sessionUID(session)
	mine := agentLeaseName(a.EndpointID())
	myRank := claimRank(uid, mine)
	now := a.now()
	ahead := 0
	for i := range leases.Items {
		lease := &leases.Items[i]
		if claiming, _ := strconv.ParseBool(lease.Annotations[annotationClaiming]); lease.Name == mine || !claiming || !leaseCurrent(lease, now) {
			continue
		}
		if rank := claimRank(uid, lease.Name); rank < myRank || rank == myRank && lease.Name < mine {
			ahead++
		}
	}
	wait := time.Duration(ahead)*a.cfg.ClaimStagger - now.Sub(session.CreationTimestamp.Time)
	return max(wait, 0), nil
}

func claimRank(sessionUID, leaseName string) uint64 {
	sum := sha256.Sum256([]byte(sessionUID + "/" + leaseName))
	return binary.BigEndian.Uint64(sum[:8])
}
