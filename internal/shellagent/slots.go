// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	componentLabel        = "app.kubernetes.io/component"
	slotComponent         = "shell-session-slot"
	annotationPrefix      = "shell-agent.compute.datumapis.com/"
	annotationSession     = annotationPrefix + "session"
	annotationPod         = annotationPrefix + "pod"
	annotationPodUID      = annotationPrefix + "pod-uid"
	annotationContainer   = annotationPrefix + "container"
	annotationContainerID = annotationPrefix + "container-id"
	annotationMarkerDir   = annotationPrefix + "marker-dir"
	annotationEndpoint    = annotationPrefix + "endpoint"
)

// A slot is a Lease held by one session of one Instance. Creating a Lease is
// atomic, so agents claiming at once cannot take more slots than exist. A
// slot also records where the session's command runs, and outlives the
// session until that command is known to be stopped: a held slot is the
// cell's record that a process may still need stopping.
type slot struct {
	lease       *coordinationv1.Lease
	sessionUID  string
	session     types.NamespacedName
	pod         types.NamespacedName
	podUID      types.UID
	container   string
	containerID string
	markerDir   string
	endpointID  string
}

func slotName(instanceUID types.UID, n int) string {
	sum := sha256.Sum256([]byte(instanceUID))
	return fmt.Sprintf("slot-%s-%d", hex.EncodeToString(sum[:8]), n)
}

func slotFromLease(l *coordinationv1.Lease) *slot {
	s := &slot{
		lease:       l,
		sessionUID:  ptr.Deref(l.Spec.HolderIdentity, ""),
		podUID:      types.UID(l.Annotations[annotationPodUID]),
		container:   l.Annotations[annotationContainer],
		containerID: l.Annotations[annotationContainerID],
		markerDir:   l.Annotations[annotationMarkerDir],
		endpointID:  l.Annotations[annotationEndpoint],
	}
	s.session = splitKey(l.Annotations[annotationSession])
	s.pod = splitKey(l.Annotations[annotationPod])
	return s
}

func splitKey(key string) types.NamespacedName {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return types.NamespacedName{Namespace: key[:i], Name: key[i+1:]}
		}
	}
	return types.NamespacedName{Name: key}
}

func (s *slot) leaseFor(namespace, name string) *coordinationv1.Lease {
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				componentLabel: slotComponent,
				computev1alpha.InstanceConsoleSessionUIDLabel: s.sessionUID,
			},
			Annotations: map[string]string{
				annotationSession:     s.session.String(),
				annotationPod:         s.pod.String(),
				annotationPodUID:      string(s.podUID),
				annotationContainer:   s.container,
				annotationContainerID: s.containerID,
				annotationMarkerDir:   s.markerDir,
				annotationEndpoint:    s.endpointID,
			},
		},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(s.sessionUID)},
	}
}

// errClaimInProgress means another agent in the cell is claiming the same
// session.
var errClaimInProgress = errors.New("another agent is claiming the session")

// acquireSlot takes one of the Instance's slots for want. It returns nil when
// every slot is held by a session that may still be running. A slot already
// held by the same session is returned as is, so a retried claim is
// idempotent.
func (a *Agent) acquireSlot(ctx context.Context, instanceUID types.UID, want *slot) (*slot, error) {
	for n := range a.cfg.SlotsPerInstance {
		name := slotName(instanceUID, n)
		for range 2 {
			lease := want.leaseFor(a.cfg.Namespace, name)
			err := a.cell.Create(ctx, lease)
			if err == nil {
				return slotFromLease(lease), nil
			}
			if !apierrors.IsAlreadyExists(err) {
				return nil, err
			}
			var held coordinationv1.Lease
			err = a.cell.Get(ctx, client.ObjectKey{Namespace: a.cfg.Namespace, Name: name}, &held)
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			holder := slotFromLease(&held)
			if holder.sessionUID == want.sessionUID {
				if holder.endpointID == want.endpointID {
					return holder, nil
				}
				alive, err := a.agentAlive(ctx, holder.endpointID)
				if err != nil {
					return nil, err
				}
				if alive {
					return nil, errClaimInProgress
				}
				if err := a.releaseSlot(ctx, holder); err != nil {
					return nil, err
				}
				continue
			}
			if a.isLive(holder.sessionUID) {
				break
			}
			state, err := a.sessionState(ctx, holder.sessionUID)
			if err != nil {
				return nil, err
			}
			if state != sessionEnded {
				break
			}
			if reaped, err := a.reap(ctx, holder); err != nil || !reaped {
				break
			}
		}
	}
	return nil, nil
}

// slotFor returns the slot a session holds in this cell, if any.
func (a *Agent) slotFor(ctx context.Context, sessionUID string) (*slot, error) {
	var leases coordinationv1.LeaseList
	if err := a.cell.List(ctx, &leases, client.InNamespace(a.cfg.Namespace), client.MatchingLabels{
		componentLabel: slotComponent,
		computev1alpha.InstanceConsoleSessionUIDLabel: sessionUID,
	}); err != nil {
		return nil, err
	}
	for i := range leases.Items {
		if ptr.Deref(leases.Items[i].Spec.HolderIdentity, "") == sessionUID {
			return slotFromLease(&leases.Items[i]), nil
		}
	}
	return nil, nil
}

// releaseSlot frees a slot whose session never started a process or whose
// process is known to be stopped.
func (a *Agent) releaseSlot(ctx context.Context, s *slot) error {
	if s == nil {
		return nil
	}
	var opts []client.DeleteOption
	if s.lease.UID != "" {
		opts = append(opts, client.Preconditions{UID: &s.lease.UID})
	}
	err := a.cell.Delete(ctx, s.lease, opts...)
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	return err
}

// reap stops the process a slot records, then frees the slot. It reports
// false, keeping the slot, when the process could not be confirmed stopped.
func (a *Agent) reap(ctx context.Context, s *slot) (bool, error) {
	stopped, err := a.stopProcesses(ctx, s)
	if err != nil || !stopped {
		return false, err
	}
	return true, a.releaseSlot(ctx, s)
}

// stopProcesses stops a session's process group in the pod its slot records.
// A pod or container that stopped, or was replaced, took the process with it.
func (a *Agent) stopProcesses(ctx context.Context, s *slot) (bool, error) {
	if s.markerDir == "" || s.pod.Name == "" {
		return true, nil
	}
	var pod corev1.Pod
	err := a.cell.Get(ctx, s.pod, &pod)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if pod.UID != s.podUID {
		return true, nil
	}
	if status := containerStatus(&pod, s.container); status == nil || status.State.Running == nil ||
		s.containerID != "" && status.ContainerID != s.containerID {
		return true, nil
	}
	grace := int(a.cfg.KillGrace.Seconds())
	out, code, err := run(ctx, a.exec, s.pod, s.container, killCommand(s.markerDir, s.sessionUID, grace))
	if err != nil || code != 0 {
		log.FromContext(ctx).Info("could not stop session processes", "session", s.sessionUID,
			"pod", s.pod.String(), "exitCode", code, "output", out, "error", err)
		return false, nil
	}
	return true, nil
}
