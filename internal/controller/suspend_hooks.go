// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	// eventReasonProjectPaused is emitted on each Instance after it is
	// successfully suspended due to a project suspension signal.
	eventReasonProjectPaused = "ProjectPaused"

	// eventReasonProjectResumed is emitted on each Instance after it is
	// successfully reinstated following a project reinstatement signal.
	eventReasonProjectResumed = "ProjectResumed"

	// eventReasonPauseFailed is emitted on an Instance when the controller
	// fails to suspend or resume it. The error is still returned so the
	// provider's backoff retry fires; no teardown or deletion is attempted.
	eventReasonPauseFailed = "PauseFailed"

	eventActionSuspending = "Suspending"
	eventActionResuming   = "Resuming"
)

// ComputeSuspend implements consumer.Suspend. It sets spec.suspended=true on
// every Instance owned by the suspended consumer project, scoped by the
// services.miloapis.com/service-name label. It is idempotent: instances that
// are already suspended are skipped.
//
// On success it emits a ProjectPaused event on each affected Instance.
// On any patch failure it emits a PauseFailed Warning event on that Instance
// and returns the error immediately so the service-catalog provider retries
// with backoff. No teardown or deletion is ever attempted.
type ComputeSuspend struct {
	recorder events.EventRecorder
}

// NewComputeSuspend creates a ComputeSuspend. recorder is used to emit
// ProjectPaused / PauseFailed events on affected Instance objects.
func NewComputeSuspend(recorder events.EventRecorder) *ComputeSuspend {
	return &ComputeSuspend{recorder: recorder}
}

// SuspendConsumer implements consumer.Suspend.
func (cs *ComputeSuspend) SuspendConsumer(
	ctx context.Context,
	consumerProject string,
	consumerClient client.Client,
	serviceNames []string,
) error {
	for _, svcName := range serviceNames {
		var instances computev1alpha.InstanceList
		if err := consumerClient.List(ctx, &instances,
			client.MatchingLabels{labelServiceName: svcName},
		); err != nil {
			return fmt.Errorf("listing instances for service %q: %w", svcName, err)
		}
		for i := range instances.Items {
			inst := &instances.Items[i]
			if inst.Spec.Suspended {
				continue // already suspended — idempotent
			}
			base := inst.DeepCopy()
			inst.Spec.Suspended = true
			if err := consumerClient.Patch(ctx, inst, client.MergeFrom(base)); err != nil {
				// Emit before returning so the event is always recorded even if
				// the caller doesn't inspect the error detail.
				if cs.recorder != nil {
					cs.recorder.Eventf(inst, nil, corev1.EventTypeWarning,
						eventReasonPauseFailed, eventActionSuspending,
						"Failed to suspend instance %s/%s for project %s: %v",
						inst.Namespace, inst.Name, consumerProject, err)
				}
				return fmt.Errorf("patching instance %s/%s to suspended: %w",
					inst.Namespace, inst.Name, err)
			}
			if cs.recorder != nil {
				cs.recorder.Eventf(inst, nil, corev1.EventTypeNormal,
					eventReasonProjectPaused, eventActionSuspending,
					"Instance suspended due to project suspension of %s", consumerProject)
			}
		}
	}
	return nil
}

// ComputeResume implements consumer.Resume. It clears spec.suspended on every
// Instance owned by the reinstated consumer project, scoped by the
// services.miloapis.com/service-name label. It is idempotent: instances that
// are already active are skipped.
//
// On success it emits a ProjectResumed event on each affected Instance.
// On any patch failure it emits a PauseFailed Warning event on that Instance
// and returns the error immediately so the service-catalog provider retries
// with backoff. No teardown or deletion is ever attempted.
type ComputeResume struct {
	recorder events.EventRecorder
}

// NewComputeResume creates a ComputeResume. recorder is used to emit
// ProjectResumed / PauseFailed events on affected Instance objects.
func NewComputeResume(recorder events.EventRecorder) *ComputeResume {
	return &ComputeResume{recorder: recorder}
}

// ResumeConsumer implements consumer.Resume.
func (cr *ComputeResume) ResumeConsumer(
	ctx context.Context,
	consumerProject string,
	consumerClient client.Client,
	serviceNames []string,
) error {
	for _, svcName := range serviceNames {
		var instances computev1alpha.InstanceList
		if err := consumerClient.List(ctx, &instances,
			client.MatchingLabels{labelServiceName: svcName},
		); err != nil {
			return fmt.Errorf("listing instances for service %q: %w", svcName, err)
		}
		for i := range instances.Items {
			inst := &instances.Items[i]
			if !inst.Spec.Suspended {
				continue // already active — idempotent
			}
			base := inst.DeepCopy()
			inst.Spec.Suspended = false
			if err := consumerClient.Patch(ctx, inst, client.MergeFrom(base)); err != nil {
				// Emit before returning so the event is always recorded.
				if cr.recorder != nil {
					cr.recorder.Eventf(inst, nil, corev1.EventTypeWarning,
						eventReasonPauseFailed, eventActionResuming,
						"Failed to resume instance %s/%s for project %s: %v",
						inst.Namespace, inst.Name, consumerProject, err)
				}
				return fmt.Errorf("patching instance %s/%s to resumed: %w",
					inst.Namespace, inst.Name, err)
			}
			if cr.recorder != nil {
				cr.recorder.Eventf(inst, nil, corev1.EventTypeNormal,
					eventReasonProjectResumed, eventActionResuming,
					"Instance resumed after project reinstatement of %s", consumerProject)
			}
		}
	}
	return nil
}
