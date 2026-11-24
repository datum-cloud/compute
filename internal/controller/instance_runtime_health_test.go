// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func runningPod(containers ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: containers,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

func TestRuntimeReadyCondition(t *testing.T) {
	now := time.Date(2026, 10, 22, 22, 31, 0, 0, time.UTC)
	crashed := &corev1.ContainerStateTerminated{ExitCode: 1, FinishedAt: metav1.NewTime(now.Add(-30 * time.Second))}

	tests := []struct {
		name         string
		pod          *corev1.Pod
		status       metav1.ConditionStatus
		reason       string
		message      string
		recheckAfter time.Duration
	}{
		{
			name:    "no pod",
			status:  metav1.ConditionUnknown,
			reason:  computev1alpha.InstanceRuntimeReadyReasonNotObserved,
			message: msgRuntimeNotObserved,
		},
		{
			name:    "running and ready",
			pod:     runningPod(corev1.ContainerStatus{Name: testContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}),
			status:  metav1.ConditionTrue,
			reason:  computev1alpha.InstanceRuntimeReadyReasonReady,
			message: msgRuntimeReady,
		},
		{
			name: "running without a ready condition",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			}},
			status:  metav1.ConditionTrue,
			reason:  computev1alpha.InstanceRuntimeReadyReasonReady,
			message: msgRuntimeReady,
		},
		{
			name: "crash loop back-off",
			pod: runningPod(corev1.ContainerStatus{
				Name:                 testContainerName,
				RestartCount:         4,
				State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: crashed},
			}),
			status:  metav1.ConditionFalse,
			reason:  computev1alpha.InstanceReadyReasonInstanceCrashing,
			message: `Container "app" exited with code 1 and has restarted 4 times`,
		},
		{
			name: "container exited",
			pod: runningPod(corev1.ContainerStatus{
				Name:  testContainerName,
				State: corev1.ContainerState{Terminated: crashed},
			}),
			status:  metav1.ConditionFalse,
			reason:  computev1alpha.InstanceReadyReasonInstanceCrashing,
			message: `Container "app" exited with code 1 and has restarted 0 times`,
		},
		{
			name: "restarted inside the crash window",
			pod: runningPod(corev1.ContainerStatus{
				Name:                 testContainerName,
				RestartCount:         1,
				State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: crashed},
			}),
			status:       metav1.ConditionFalse,
			reason:       computev1alpha.InstanceReadyReasonInstanceCrashing,
			message:      `Container "app" exited with code 1 and has restarted 1 times`,
			recheckAfter: runtimeCrashWindow - 30*time.Second,
		},
		{
			name: "restarted outside the crash window",
			pod: runningPod(corev1.ContainerStatus{
				Name:         testContainerName,
				RestartCount: 1,
				State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					ExitCode:   1,
					FinishedAt: metav1.NewTime(now.Add(-runtimeCrashWindow - time.Second)),
				}},
			}),
			status:  metav1.ConditionTrue,
			reason:  computev1alpha.InstanceRuntimeReadyReasonReady,
			message: msgRuntimeReady,
		},
		{
			name: "restarted inside the crash window without a last state",
			pod: runningPod(corev1.ContainerStatus{
				Name:         testContainerName,
				RestartCount: 3,
				State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-30 * time.Second))}},
			}),
			status:       metav1.ConditionFalse,
			reason:       computev1alpha.InstanceReadyReasonInstanceCrashing,
			message:      `Container "app" keeps exiting and has restarted 3 times`,
			recheckAfter: runtimeCrashWindow - 30*time.Second,
		},
		{
			name: "finished container under a non-restarting policy",
			pod: func() *corev1.Pod {
				pod := runningPod(corev1.ContainerStatus{
					Name:  testContainerName,
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
				})
				pod.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
				return pod
			}(),
			status:  metav1.ConditionTrue,
			reason:  computev1alpha.InstanceRuntimeReadyReasonReady,
			message: msgRuntimeReady,
		},
		{
			name: "exit 0 under an always-restart policy",
			pod: func() *corev1.Pod {
				pod := runningPod(corev1.ContainerStatus{
					Name:  testContainerName,
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
				})
				pod.Spec.RestartPolicy = corev1.RestartPolicyAlways
				return pod
			}(),
			status:  metav1.ConditionFalse,
			reason:  computev1alpha.InstanceReadyReasonInstanceCrashing,
			message: `Container "app" exited with code 0 and has restarted 0 times`,
		},
		{
			name: "pod not ready",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionFalse, Message: "node is not ready"},
				},
			}},
			status:  metav1.ConditionFalse,
			reason:  computev1alpha.InstanceRuntimeReadyReasonNotReady,
			message: msgRuntimeNotReady + ": node is not ready",
		},
		{
			name:    "pending",
			pod:     &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}},
			status:  metav1.ConditionFalse,
			reason:  computev1alpha.InstanceReadyReasonProvisioning,
			message: msgRuntimeStarting,
		},
		{
			name:    "failed",
			pod:     &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Message: "evicted"}},
			status:  metav1.ConditionFalse,
			reason:  computev1alpha.InstanceRuntimeReadyReasonExited,
			message: "The instance runtime exited: evicted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			condition, recheckAfter := runtimeReadyCondition(tt.pod, now)
			assert.Equal(t, computev1alpha.InstanceRuntimeReady, condition.Type)
			assert.Equal(t, tt.status, condition.Status)
			assert.Equal(t, tt.reason, condition.Reason)
			assert.Equal(t, tt.message, condition.Message)
			assert.Equal(t, tt.recheckAfter, recheckAfter)
		})
	}
}

func TestFindRuntimePod(t *testing.T) {
	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testInstanceName,
			Namespace: testDefaultNamespace,
			UID:       types.UID(testUIDString),
		},
	}
	controller := true
	owned := func(name string, created time.Time) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         testDefaultNamespace,
			CreationTimestamp: metav1.NewTime(created),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: computev1alpha.GroupVersion.String(),
				Kind:       internalDNSInstanceKind,
				Name:       instance.Name,
				UID:        instance.UID,
				Controller: &controller,
			}},
		}}
	}
	now := time.Now().Truncate(time.Second)
	unrelated := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:              "unrelated",
		Namespace:         testDefaultNamespace,
		CreationTimestamp: metav1.NewTime(now.Add(time.Hour)),
	}}

	cl := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(owned("old", now.Add(-time.Hour)), owned("new", now), unrelated).
		Build()

	pod, err := findRuntimePod(context.Background(), cl, instance)
	require.NoError(t, err)
	require.NotNil(t, pod)
	assert.Equal(t, "new", pod.Name)

	empty := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(unrelated).Build()
	pod, err = findRuntimePod(context.Background(), empty, instance)
	require.NoError(t, err)
	assert.Nil(t, pod)
}

func TestReconcileInstanceReadyCondition_RuntimeReady(t *testing.T) {
	noNetworkFailure := func(context.Context, client.Client, *computev1alpha.Instance) (bool, string, error) {
		return false, "", nil
	}
	instanceWith := func(conditions ...metav1.Condition) *computev1alpha.Instance {
		return &computev1alpha.Instance{
			ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: testDefaultNamespace, Generation: 1},
			Status:     computev1alpha.InstanceStatus{Conditions: conditions},
		}
	}
	programmed := metav1.Condition{Type: computev1alpha.InstanceProgrammed, Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceProgrammedReasonProgrammed}
	available := metav1.Condition{Type: computev1alpha.InstanceAvailable, Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceAvailableReasonAvailable}
	crashing := metav1.Condition{
		Type:    computev1alpha.InstanceRuntimeReady,
		Status:  metav1.ConditionFalse,
		Reason:  computev1alpha.InstanceReadyReasonInstanceCrashing,
		Message: `Container "app" exited with code 1 and has restarted 4 times`,
	}
	notObserved := metav1.Condition{
		Type:    computev1alpha.InstanceRuntimeReady,
		Status:  metav1.ConditionUnknown,
		Reason:  computev1alpha.InstanceRuntimeReadyReasonNotObserved,
		Message: msgRuntimeNotObserved,
	}

	tests := []struct {
		name     string
		instance *computev1alpha.Instance
		status   metav1.ConditionStatus
		reason   string
		message  string
	}{
		{
			name:     "a crashing runtime withdraws Ready from an available instance",
			instance: instanceWith(programmed, available, crashing),
			status:   metav1.ConditionFalse,
			reason:   computev1alpha.InstanceReadyReasonInstanceCrashing,
			message:  crashing.Message,
		},
		{
			name:     "an unobserved runtime does not block Ready",
			instance: instanceWith(programmed, available, notObserved),
			status:   metav1.ConditionTrue,
			reason:   computev1alpha.InstanceReadyReasonAvailable,
			message:  msgInstanceReady,
		},
		{
			name:     "an unobserved runtime is not the reason an unprogrammed instance waits",
			instance: instanceWith(notObserved),
			status:   metav1.ConditionFalse,
			reason:   computev1alpha.InstanceProgrammedReasonPendingProgramming,
			message:  msgNotProgrammed,
		},
		{
			name:     "a crashing runtime is the reason an unprogrammed instance waits",
			instance: instanceWith(crashing),
			status:   metav1.ConditionFalse,
			reason:   computev1alpha.InstanceReadyReasonInstanceCrashing,
			message:  crashing.Message,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &InstanceReconciler{}
			_, err := r.reconcileInstanceReadyCondition(context.Background(), nil, tt.instance, noNetworkFailure)
			require.NoError(t, err)

			ready := apimeta.FindStatusCondition(tt.instance.Status.Conditions, computev1alpha.InstanceReady)
			require.NotNil(t, ready)
			assert.Equal(t, tt.status, ready.Status)
			assert.Equal(t, tt.reason, ready.Reason)
			assert.Equal(t, tt.message, ready.Message)
		})
	}
}
