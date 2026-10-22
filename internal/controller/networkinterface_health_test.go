// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const healthTestInstance = "health-test-instance"

func healthTestClaim(programmed metav1.ConditionStatus) *networkingv1alpha.NetworkInterfaceClaim {
	controller := true
	return &networkingv1alpha.NetworkInterfaceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      networkInterfaceClaimName(healthTestInstance, defaultInterfaceName),
			Namespace: claimTestNamespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: computev1alpha.GroupVersion.String(),
				Kind:       internalDNSInstanceKind,
				Name:       healthTestInstance,
				UID:        types.UID(testUIDString),
				Controller: &controller,
			}},
		},
		Status: networkingv1alpha.NetworkInterfaceClaimStatus{
			NetworkInterfaceRef: &networkingv1alpha.LocalNetworkInterfaceRef{Name: holderTestInterface},
			Conditions: []metav1.Condition{
				{Type: networkingv1alpha.NetworkInterfaceClaimAllocated, Status: metav1.ConditionTrue, Reason: networkingv1alpha.NetworkInterfaceClaimAllocated},
				{Type: networkingv1alpha.NetworkInterfaceClaimProgrammed, Status: programmed, Reason: wdTestReasonProgrammed},
			},
		},
	}
}

func healthTestInterface(programmed *metav1.Condition) *networkingv1alpha.NetworkInterface {
	networkInterface := newHolderTestInterface()
	networkInterface.Spec.ClaimRef = &networkingv1alpha.NetworkInterfaceClaimRef{
		Name: networkInterfaceClaimName(healthTestInstance, defaultInterfaceName),
	}
	if programmed != nil {
		networkInterface.Status.Conditions = []metav1.Condition{*programmed}
	}
	return networkInterface
}

var withdrawn = metav1.Condition{
	Type:    networkingv1alpha.NetworkInterfaceProgrammed,
	Status:  metav1.ConditionFalse,
	Reason:  "AdvertisementWithdrawn",
	Message: "the attachment has no network advertisement",
}

func healthTestInstanceSpec() *computev1alpha.Instance {
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: healthTestInstance, Namespace: claimTestNamespace},
		Spec: computev1alpha.InstanceSpec{
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{{}},
		},
	}
}

func TestReconcileNetworkInterfaceStatus_InterfaceProgrammedWins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		iface      *networkingv1alpha.NetworkInterface
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name:       "a withdrawn interface overrides the claim",
			iface:      healthTestInterface(&withdrawn),
			wantStatus: metav1.ConditionFalse,
			wantReason: "AdvertisementWithdrawn",
		},
		{
			name:       "an interface without the condition keeps the claim's copy",
			iface:      healthTestInterface(nil),
			wantStatus: metav1.ConditionTrue,
			wantReason: wdTestReasonProgrammed,
		},
		{
			name:       "a missing interface keeps the claim's copy",
			wantStatus: metav1.ConditionTrue,
			wantReason: wdTestReasonProgrammed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := []client.Object{healthTestClaim(metav1.ConditionTrue)}
			if tt.iface != nil {
				objects = append(objects, tt.iface)
			}
			cl := holderTestClient(objects...)
			instance := healthTestInstanceSpec()

			r := &InstanceReconciler{NetworkingEnabled: true}
			changed, err := r.reconcileNetworkInterfaceStatus(context.Background(), cl, instance)
			require.NoError(t, err)
			assert.True(t, changed)

			require.Len(t, instance.Status.NetworkInterfaces, 1)
			conditions := instance.Status.NetworkInterfaces[0].Conditions
			programmed := apimeta.FindStatusCondition(conditions, computev1alpha.InstanceNetworkInterfaceProgrammed)
			require.NotNil(t, programmed)
			assert.Equal(t, tt.wantStatus, programmed.Status)
			assert.Equal(t, tt.wantReason, programmed.Reason)
			assert.Zero(t, programmed.ObservedGeneration)
			assert.NotNil(t, apimeta.FindStatusCondition(conditions, computev1alpha.InstanceNetworkInterfaceAllocated))

			changed, err = r.reconcileNetworkInterfaceStatus(context.Background(), cl, instance)
			require.NoError(t, err)
			assert.False(t, changed, "a second pass over the same state must not rewrite status")
		})
	}
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
		})
	}
}

func TestMapNetworkInterfaceToInstance(t *testing.T) {
	t.Parallel()

	cl := holderTestClient(healthTestClaim(metav1.ConditionTrue))

	requests := mapNetworkInterfaceToInstance(context.Background(), cl, smTestCluster, healthTestInterface(nil))
	require.Len(t, requests, 1)
	assert.Equal(t, types.NamespacedName{Namespace: claimTestNamespace, Name: healthTestInstance}, requests[0].NamespacedName)
	assert.Equal(t, smTestCluster, string(requests[0].ClusterName))

	unclaimed := newHolderTestInterface()
	assert.Empty(t, mapNetworkInterfaceToInstance(context.Background(), cl, smTestCluster, unclaimed))

	orphaned := healthTestInterface(nil)
	orphaned.Spec.ClaimRef.Name = "missing"
	assert.Empty(t, mapNetworkInterfaceToInstance(context.Background(), cl, smTestCluster, orphaned))
}

func TestNetworkInterfaceProgrammedChanged(t *testing.T) {
	t.Parallel()

	programmed := metav1.Condition{Type: networkingv1alpha.NetworkInterfaceProgrammed, Status: metav1.ConditionTrue, Reason: wdTestReasonProgrammed}
	holder := metav1.Condition{Type: networkInterfaceHolderAvailable, Status: metav1.ConditionTrue, Reason: "Available"}

	withConditions := func(conditions ...metav1.Condition) *networkingv1alpha.NetworkInterface {
		networkInterface := newHolderTestInterface()
		networkInterface.Status.Conditions = conditions
		return networkInterface
	}

	p := networkInterfaceProgrammedChanged()
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: withConditions(programmed), ObjectNew: withConditions(withdrawn)}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: withConditions(), ObjectNew: withConditions(programmed)}))
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: withConditions(programmed), ObjectNew: withConditions(programmed, holder)}),
		"the instance controller's own HolderAvailable write must not requeue the instance")
	assert.True(t, p.Create(event.CreateEvent{Object: withConditions()}))
}
