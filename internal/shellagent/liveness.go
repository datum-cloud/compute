// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// LeaseDuration is how long an agent's liveness Lease holds without a
	// renewal before other agents take over its sessions.
	LeaseDuration = 15 * time.Second
	// LeaseRenewInterval is how often an agent renews its liveness Lease.
	LeaseRenewInterval = 5 * time.Second

	livenessComponent = "shell-agent-liveness"
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
	now := metav1.NewMicroTime(a.now())
	key := client.ObjectKey{Namespace: a.cfg.Namespace, Name: agentLeaseName(id)}
	var lease coordinationv1.Lease
	err := a.cell.Get(ctx, key, &lease)
	if apierrors.IsNotFound(err) {
		return a.cell.Create(ctx, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
				Labels:    map[string]string{componentLabel: livenessComponent},
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
	lease.Spec.HolderIdentity = ptr.To(a.incarnation)
	lease.Spec.LeaseDurationSeconds = ptr.To(int32(LeaseDuration.Seconds()))
	lease.Spec.RenewTime = &now
	return a.cell.Update(ctx, &lease)
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
	if lease.Spec.RenewTime == nil || lease.Spec.LeaseDurationSeconds == nil {
		return false, nil
	}
	expires := lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
	return a.now().Before(expires), nil
}
