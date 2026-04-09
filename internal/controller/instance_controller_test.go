package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func TestReconcileInstanceReadyCondition(t *testing.T) {

	tests := []struct {
		name               string
		instance           *computev1alpha.Instance
		networkFailureFunc networkFailureChecker
		expectedChanged    bool
		expectedCondition  *metav1.Condition
	}{
		{
			name: "instance without ready condition should create default",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             computev1alpha.InstanceProgrammedReasonPendingProgramming,
				Message:            "Instance has not been programmed",
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance with scheduling gates should set scheduling gates present",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
				Spec: computev1alpha.InstanceSpec{
					Controller: &computev1alpha.InstanceController{
						SchedulingGates: []computev1alpha.SchedulingGate{
							{Name: "Network"},
						},
					},
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:               computev1alpha.InstanceReady,
							Status:             metav1.ConditionFalse,
							Reason:             computev1alpha.InstanceProgrammedReasonPendingProgramming,
							Message:            "Instance has not been programmed",
							ObservedGeneration: 1,
							LastTransitionTime: metav1.Now(),
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             computev1alpha.InstanceReadyReasonSchedulingGatesPresent,
				Message:            "Scheduling gates present: Network",
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance with scheduling gates and network failure should set network failed",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
				Spec: computev1alpha.InstanceSpec{
					Controller: &computev1alpha.InstanceController{
						SchedulingGates: []computev1alpha.SchedulingGate{
							{Name: "Network"},
						},
					},
				},
			},
			networkFailureFunc: func(ctx context.Context, upstreamClient client.Client, instance *computev1alpha.Instance) (bool, string, error) {
				return true, "Network creation failed: timeout", nil
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             "NetworkFailedToCreate",
				Message:            "Network creation failed: timeout",
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance not programmed should set pending programming",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionFalse,
							Reason:  "TestReason",
							Message: "Test message",
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             "TestReason",
				Message:            "Test message",
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance programmed but not running should wait for running",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammed,
							Message: "Instance has been programmed",
						},
						{
							Type:    computev1alpha.InstanceRunning,
							Status:  metav1.ConditionFalse,
							Reason:  "TestReason",
							Message: "Test message",
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionFalse,
				Reason:             "TestReason",
				Message:            "Test message",
				ObservedGeneration: 1,
			},
		},
		{
			name: "instance fully ready should set ready condition",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammed,
							Message: "Instance has been programmed",
						},
						{
							Type:    computev1alpha.InstanceRunning,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceRunningReasonRunning,
							Message: "Instance is running",
						},
					},
				},
			},
			expectedChanged: true,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionTrue,
				Reason:             computev1alpha.InstanceReadyReasonRunning,
				Message:            "Instance is ready",
				ObservedGeneration: 1,
			},
		},
		{
			name: "no change when condition already matches",
			instance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Namespace:  "default",
					Generation: 1,
				},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{
							Type:               computev1alpha.InstanceReady,
							Status:             metav1.ConditionTrue,
							Reason:             computev1alpha.InstanceReadyReasonRunning,
							Message:            "Instance is ready",
							ObservedGeneration: 1,
							LastTransitionTime: metav1.Now(),
						},
						{
							Type:    computev1alpha.InstanceProgrammed,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceProgrammedReasonProgrammed,
							Message: "Instance has been programmed",
						},
						{
							Type:    computev1alpha.InstanceRunning,
							Status:  metav1.ConditionTrue,
							Reason:  computev1alpha.InstanceRunningReasonRunning,
							Message: "Instance is running",
						},
					},
				},
			},
			expectedChanged: false,
			expectedCondition: &metav1.Condition{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionTrue,
				Reason:             computev1alpha.InstanceReadyReasonRunning,
				Message:            "Instance is ready",
				ObservedGeneration: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			reconciler := &InstanceReconciler{}

			networkFailureFunc := tt.networkFailureFunc
			if networkFailureFunc == nil {
				networkFailureFunc = func(ctx context.Context, upstreamClient client.Client, instance *computev1alpha.Instance) (bool, string, error) {
					return false, "", nil
				}
			}

			changed, err := reconciler.reconcileInstanceReadyCondition(
				ctx,
				nil,
				tt.instance,
				networkFailureFunc,
			)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedChanged, changed)

			readyCondition := apimeta.FindStatusCondition(tt.instance.Status.Conditions, computev1alpha.InstanceReady)
			require.NotNil(t, readyCondition)

			assert.Equal(t, tt.expectedCondition.Type, readyCondition.Type)
			assert.Equal(t, tt.expectedCondition.Status, readyCondition.Status)
			assert.Equal(t, tt.expectedCondition.Reason, readyCondition.Reason)
			assert.Equal(t, tt.expectedCondition.Message, readyCondition.Message)
			assert.Equal(t, tt.expectedCondition.ObservedGeneration, readyCondition.ObservedGeneration)
		})
	}
}
