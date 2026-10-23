// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const healthTestInstance = "health-test-instance"

var withdrawn = metav1.Condition{
	Type:    networkingv1alpha.NetworkInterfaceClaimProgrammed,
	Status:  metav1.ConditionFalse,
	Reason:  "AdvertisementWithdrawn",
	Message: "the attachment has no network advertisement",
}

func TestReconcileInstanceReadyCondition_UnprogrammedInterface(t *testing.T) {
	t.Parallel()

	noNetworkFailure := func(context.Context, client.Client, *computev1alpha.Instance) (bool, string, error) {
		return false, "", nil
	}
	newInstance := func(programmed metav1.Condition) *computev1alpha.Instance {
		return &computev1alpha.Instance{
			ObjectMeta: metav1.ObjectMeta{Name: healthTestInstance, Namespace: claimTestNamespace, Generation: 1},
			Status: computev1alpha.InstanceStatus{
				Conditions: []metav1.Condition{
					{Type: computev1alpha.InstanceProgrammed, Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceProgrammedReasonProgrammed},
					{Type: computev1alpha.InstanceAvailable, Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceAvailableReasonAvailable},
				},
				NetworkInterfaces: []computev1alpha.InstanceNetworkInterfaceStatus{{
					Name:       defaultInterfaceName,
					Conditions: []metav1.Condition{programmed},
				}},
			},
		}
	}

	tests := []struct {
		name       string
		programmed metav1.Condition
		status     metav1.ConditionStatus
		reason     string
		message    string
	}{
		{
			name:       "a withdrawn interface withdraws Ready",
			programmed: withdrawn,
			status:     metav1.ConditionFalse,
			reason:     computev1alpha.InstanceReadyReasonNetworkInterfaceNotProgrammed,
			message:    `Interface "eth0" is not programmed: the attachment has no network advertisement`,
		},
		{
			name: "a withdrawal shorter than the grace keeps Ready",
			programmed: func() metav1.Condition {
				c := withdrawn
				c.LastTransitionTime = metav1.NewTime(time.Now().Add(-5 * time.Second))
				return c
			}(),
			status:  metav1.ConditionTrue,
			reason:  computev1alpha.InstanceReadyReasonAvailable,
			message: msgInstanceReady,
		},
		{
			name:       "an interface not yet reported does not block Ready",
			programmed: metav1.Condition{Type: networkingv1alpha.NetworkInterfaceProgrammed, Status: metav1.ConditionUnknown, Reason: "Pending"},
			status:     metav1.ConditionTrue,
			reason:     computev1alpha.InstanceReadyReasonAvailable,
			message:    msgInstanceReady,
		},
		{
			name:       "a programmed interface keeps Ready",
			programmed: metav1.Condition{Type: networkingv1alpha.NetworkInterfaceProgrammed, Status: metav1.ConditionTrue, Reason: wdTestReasonProgrammed},
			status:     metav1.ConditionTrue,
			reason:     computev1alpha.InstanceReadyReasonAvailable,
			message:    msgInstanceReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instance := newInstance(tt.programmed)
			r := &InstanceReconciler{}
			_, err := r.reconcileInstanceReadyCondition(context.Background(), nil, instance, noNetworkFailure)
			require.NoError(t, err)

			ready := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceReady)
			require.NotNil(t, ready)
			assert.Equal(t, tt.status, ready.Status)
			assert.Equal(t, tt.reason, ready.Reason)
			assert.Equal(t, tt.message, ready.Message)
			assert.True(t, apimeta.IsStatusConditionTrue(instance.Status.Conditions, computev1alpha.InstanceAvailable),
				"Available stays True so the instance still counts as running")
		})
	}
}

func TestUnprogrammedInterfaceRechecksAtGraceEnd(t *testing.T) {
	t.Parallel()

	now := time.Now()
	recent := withdrawn
	recent.LastTransitionTime = metav1.NewTime(now.Add(-10 * time.Second))
	instance := &computev1alpha.Instance{Status: computev1alpha.InstanceStatus{
		NetworkInterfaces: []computev1alpha.InstanceNetworkInterfaceStatus{{
			Name:       defaultInterfaceName,
			Conditions: []metav1.Condition{recent},
		}},
	}}

	_, unprogrammed, recheckAfter := unprogrammedInterface(instance, now)
	assert.False(t, unprogrammed)
	assert.Equal(t, interfaceWithdrawalGrace-10*time.Second, recheckAfter)

	_, unprogrammed, recheckAfter = unprogrammedInterface(instance, now.Add(interfaceWithdrawalGrace))
	assert.True(t, unprogrammed)
	assert.Zero(t, recheckAfter)
}

func TestNetworkInterfaceClaimIsControlledByItsInstance(t *testing.T) {
	t.Parallel()

	scheme := newTestScheme(t)
	require.NoError(t, networkingv1alpha.AddToScheme(scheme))
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	deployment := &computev1alpha.WorkloadDeployment{ObjectMeta: metav1.ObjectMeta{Name: "health-test-deployment", Namespace: claimTestNamespace}}
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: healthTestInstance, Namespace: claimTestNamespace, UID: "health-test-uid"}}
	networkInterface := computev1alpha.InstanceNetworkInterface{Name: defaultInterfaceName, Network: networkingv1alpha.NetworkRef{Name: claimTestNetwork}}

	claim, err := (&WorkloadDeploymentReconciler{}).ensureNetworkInterfaceClaim(context.Background(), cl, deployment, instance, networkInterface)
	require.NoError(t, err)

	owner := metav1.GetControllerOf(claim)
	require.NotNil(t, owner, "the Instance watch on claims maps back through the controller reference")
	assert.Equal(t, internalDNSInstanceKind, owner.Kind)
	assert.Equal(t, instance.UID, owner.UID)
}
