// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// runtimeCrashWindow is how long a restarted container must keep running
// before the instance stops being reported as crashing. Without it, a process
// that crashes every few minutes would read Ready for most of each cycle.
const runtimeCrashWindow = 2 * time.Minute

const (
	msgRuntimeNotObserved = "No runtime Pod has been observed for this instance"

	msgRuntimeReady = "The instance runtime is running and ready"

	msgRuntimeStarting = "The instance runtime is starting"

	msgRuntimeNotReady = "The instance runtime is not ready"
)

// reconcileRuntimeReadyCondition sets RuntimeReady from the instance's runtime
// Pod. It returns whether the condition changed, and how long until the
// condition must be evaluated again without a Pod event, which is non-zero only
// while a restarted container is inside runtimeCrashWindow.
func (r *InstanceReconciler) reconcileRuntimeReadyCondition(
	ctx context.Context,
	clusterClient client.Client,
	instance *computev1alpha.Instance,
) (bool, time.Duration, error) {
	pod, err := findRuntimePod(ctx, clusterClient, instance)
	if err != nil {
		return false, 0, fmt.Errorf("failed finding runtime pod: %w", err)
	}

	condition, recheckAfter := runtimeReadyCondition(pod, time.Now())
	condition.ObservedGeneration = instance.Generation
	return apimeta.SetStatusCondition(&instance.Status.Conditions, condition), recheckAfter, nil
}

// findRuntimePod returns the newest live Pod the instance controls, or nil when
// there is none.
func findRuntimePod(ctx context.Context, clusterClient client.Client, instance *computev1alpha.Instance) (*corev1.Pod, error) {
	options := []client.ListOption{client.InNamespace(instance.Namespace)}
	deployment, index := instance.Labels[computev1alpha.WorkloadDeploymentUIDLabel], instance.Labels[computev1alpha.InstanceIndexLabel]
	if deployment != "" && index != "" {
		options = append(options, client.MatchingLabels{
			computev1alpha.WorkloadDeploymentUIDLabel: deployment,
			computev1alpha.InstanceIndexLabel:         index,
		})
	}

	var pods corev1.PodList
	if err := clusterClient.List(ctx, &pods, options...); err != nil {
		return nil, err
	}

	var newest *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !metav1.IsControlledBy(pod, instance) || !pod.DeletionTimestamp.IsZero() {
			continue
		}
		if newest == nil || newest.CreationTimestamp.Before(&pod.CreationTimestamp) {
			newest = pod
		}
	}
	return newest, nil
}

// runtimeReadyCondition derives RuntimeReady from a runtime Pod.
//
// The Pod's Ready condition is trusted when the runtime reports one. A runtime
// that never reports it is judged by phase alone, so it is not held unready by
// a signal it cannot give.
func runtimeReadyCondition(pod *corev1.Pod, now time.Time) (metav1.Condition, time.Duration) {
	condition := metav1.Condition{Type: computev1alpha.InstanceRuntimeReady}

	if pod == nil {
		condition.Status = metav1.ConditionUnknown
		condition.Reason = computev1alpha.InstanceRuntimeReadyReasonNotObserved
		condition.Message = msgRuntimeNotObserved
		return condition, 0
	}

	switch pod.Status.Phase {
	case corev1.PodFailed, corev1.PodSucceeded:
		condition.Status = metav1.ConditionFalse
		condition.Reason = computev1alpha.InstanceRuntimeReadyReasonExited
		condition.Message = fmt.Sprintf("The instance runtime exited (%s)", pod.Status.Phase)
		if pod.Status.Message != "" {
			condition.Message = fmt.Sprintf("The instance runtime exited: %s", pod.Status.Message)
		}
		return condition, 0
	}

	if message, recheckAfter, crashing := runtimeCrash(pod.Status.ContainerStatuses, now); crashing {
		condition.Status = metav1.ConditionFalse
		condition.Reason = computev1alpha.InstanceReadyReasonInstanceCrashing
		condition.Message = message
		return condition, recheckAfter
	}

	if pod.Status.Phase == corev1.PodPending || pod.Status.Phase == "" {
		condition.Status = metav1.ConditionFalse
		condition.Reason = computev1alpha.InstanceReadyReasonProvisioning
		condition.Message = msgRuntimeStarting
		return condition, 0
	}

	for _, podCondition := range pod.Status.Conditions {
		if podCondition.Type != corev1.PodReady {
			continue
		}
		if podCondition.Status == corev1.ConditionTrue {
			break
		}
		condition.Status = metav1.ConditionFalse
		condition.Reason = computev1alpha.InstanceRuntimeReadyReasonNotReady
		condition.Message = msgRuntimeNotReady
		if podCondition.Message != "" {
			condition.Message = fmt.Sprintf("%s: %s", msgRuntimeNotReady, podCondition.Message)
		}
		return condition, 0
	}

	condition.Status = metav1.ConditionTrue
	condition.Reason = computev1alpha.InstanceRuntimeReadyReasonReady
	condition.Message = msgRuntimeReady
	return condition, 0
}

// runtimeCrash reports whether any container is crashing: waiting to be
// restarted, stopped, or running again for less than runtimeCrashWindow after
// exiting. For a container inside the window it also returns the time left.
func runtimeCrash(statuses []corev1.ContainerStatus, now time.Time) (string, time.Duration, bool) {
	for _, status := range statuses {
		last := status.LastTerminationState.Terminated
		switch {
		case status.State.Waiting != nil && status.State.Waiting.Reason == "CrashLoopBackOff":
			return crashMessage(status.Name, last, status.RestartCount), 0, true
		case status.State.Terminated != nil:
			return crashMessage(status.Name, status.State.Terminated, status.RestartCount), 0, true
		case status.State.Running != nil && last != nil:
			if remaining := last.FinishedAt.Add(runtimeCrashWindow).Sub(now); remaining > 0 {
				return crashMessage(status.Name, last, status.RestartCount), remaining, true
			}
		}
	}
	return "", 0, false
}

func crashMessage(container string, terminated *corev1.ContainerStateTerminated, restarts int32) string {
	if terminated == nil {
		return fmt.Sprintf("Container %q keeps exiting and has restarted %d times", container, restarts)
	}
	return fmt.Sprintf("Container %q exited with code %d and has restarted %d times", container, terminated.ExitCode, restarts)
}
