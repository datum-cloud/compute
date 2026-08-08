// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

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
	logger := log.FromContext(ctx)
	logger.Info("SuspendConsumer hook invoked", "consumerProject", consumerProject, "serviceNames", serviceNames)
	for _, svcName := range serviceNames {
		// Suspend all WorkloadDeployments
		var wds computev1alpha.WorkloadDeploymentList
		if err := consumerClient.List(ctx, &wds,
			client.MatchingLabels{labelServiceName: svcName},
		); err != nil {
			return fmt.Errorf("listing workloaddeployments for service %q: %w", svcName, err)
		}
		logger.Info("found workloaddeployments to suspend", "consumerProject", consumerProject, "service", svcName, "count", len(wds.Items))
		for i := range wds.Items {
			wd := &wds.Items[i]
			if wd.Status.Suspended {
				continue // already suspended — idempotent
			}
			base := wd.DeepCopy()
			wd.Status.Suspended = true
			if err := consumerClient.Status().Patch(ctx, wd, client.MergeFrom(base)); err != nil {
				if cs.recorder != nil {
					cs.recorder.Eventf(wd, nil, corev1.EventTypeWarning,
						eventReasonPauseFailed, eventActionSuspending,
						"Failed to suspend workloaddeployment %s/%s for project %s: %v",
						wd.Namespace, wd.Name, consumerProject, err)
				}
				return fmt.Errorf("patching workloaddeployment %s/%s status to suspended: %w",
					wd.Namespace, wd.Name, err)
			}
			logger.Info("workloaddeployment suspended", "consumerProject", consumerProject, "workloadDeployment", client.ObjectKeyFromObject(wd))
			if cs.recorder != nil {
				cs.recorder.Eventf(wd, nil, corev1.EventTypeNormal,
					eventReasonProjectPaused, eventActionSuspending,
					"WorkloadDeployment suspended due to project suspension of %s", consumerProject)
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
	logger := log.FromContext(ctx)
	logger.Info("ResumeConsumer hook invoked", "consumerProject", consumerProject, "serviceNames", serviceNames)
	for _, svcName := range serviceNames {
		// Resume all WorkloadDeployments
		var wds computev1alpha.WorkloadDeploymentList
		if err := consumerClient.List(ctx, &wds,
			client.MatchingLabels{labelServiceName: svcName},
		); err != nil {
			return fmt.Errorf("listing workloaddeployments for service %q: %w", svcName, err)
		}
		logger.Info("found workloaddeployments to resume", "consumerProject", consumerProject, "service", svcName, "count", len(wds.Items))
		for i := range wds.Items {
			wd := &wds.Items[i]
			if !wd.Status.Suspended {
				continue // already active — idempotent
			}
			base := wd.DeepCopy()
			wd.Status.Suspended = false
			if err := consumerClient.Status().Patch(ctx, wd, client.MergeFrom(base)); err != nil {
				if cr.recorder != nil {
					cr.recorder.Eventf(wd, nil, corev1.EventTypeWarning,
						eventReasonPauseFailed, eventActionResuming,
						"Failed to resume workloaddeployment %s/%s for project %s: %v",
						wd.Namespace, wd.Name, consumerProject, err)
				}
				return fmt.Errorf("patching workloaddeployment %s/%s status to resumed: %w",
					wd.Namespace, wd.Name, err)
			}
			logger.Info("workloaddeployment resumed", "consumerProject", consumerProject, "workloadDeployment", client.ObjectKeyFromObject(wd))
			if cr.recorder != nil {
				cr.recorder.Eventf(wd, nil, corev1.EventTypeNormal,
					eventReasonProjectResumed, eventActionResuming,
					"WorkloadDeployment resumed after project reinstatement of %s", consumerProject)
			}
		}
	}
	return nil
}
