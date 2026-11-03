// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const observationTestAddress = "10.20.0.10/32"

func TestObserveInternalDNSInterface(t *testing.T) {
	const delegatedPrefix = "fd20:0:f::5:0:0/96"
	const delegatedAddress = "fd20:0:f::5:0:0"
	setCondition := func(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus) {
		apimeta.SetStatusCondition(conditions, metav1.Condition{Type: conditionType, Status: status, Reason: "Test"})
	}
	for _, tc := range []struct {
		name          string
		mutate        func(*computev1alpha.Instance)
		eligible      bool
		wantErr       bool
		wantAddresses []networkingv1alpha.NetworkInterfaceAddress
	}{
		{name: "ready instance with a programmed interface", eligible: true, wantAddresses: []networkingv1alpha.NetworkInterfaceAddress{{Family: networkingv1alpha.IPv4Protocol, Address: observationTestAddress}}},
		{name: "delegated IPv6 block publishes its host address", eligible: true, wantAddresses: []networkingv1alpha.NetworkInterfaceAddress{{Address: delegatedAddress}}, mutate: func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces[0].Addresses = []computev1alpha.InstanceNetworkInterfaceAddress{{Address: delegatedPrefix}}
		}},
		{name: "duplicate allocations", eligible: true, wantAddresses: []networkingv1alpha.NetworkInterfaceAddress{{Address: delegatedAddress}}, mutate: func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces[0].Addresses = []computev1alpha.InstanceNetworkInterfaceAddress{{Address: delegatedPrefix}, {Address: delegatedAddress + "/128"}}
		}},
		{name: "delegated IPv4 block", eligible: true, wantAddresses: []networkingv1alpha.NetworkInterfaceAddress{}, mutate: func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces[0].Addresses = []computev1alpha.InstanceNetworkInterfaceAddress{{Address: "10.20.0.0/24"}}
		}},
		{name: "runtime condition not yet reported", eligible: true, mutate: func(i *computev1alpha.Instance) {
			apimeta.RemoveStatusCondition(&i.Status.Conditions, computev1alpha.InstanceRuntimeReady)
		}},
		{name: "instance not ready", mutate: func(i *computev1alpha.Instance) {
			setCondition(&i.Status.Conditions, computev1alpha.InstanceReady, metav1.ConditionFalse)
		}},
		{name: "instance readiness unknown", mutate: func(i *computev1alpha.Instance) {
			setCondition(&i.Status.Conditions, computev1alpha.InstanceReady, metav1.ConditionUnknown)
		}},
		{name: "runtime crashing", mutate: func(i *computev1alpha.Instance) {
			setCondition(&i.Status.Conditions, computev1alpha.InstanceRuntimeReady, metav1.ConditionFalse)
		}},
		{name: "runtime not observed", mutate: func(i *computev1alpha.Instance) {
			setCondition(&i.Status.Conditions, computev1alpha.InstanceRuntimeReady, metav1.ConditionUnknown)
		}},
		{name: "interface not programmed", mutate: func(i *computev1alpha.Instance) {
			setCondition(&i.Status.NetworkInterfaces[0].Conditions, computev1alpha.InstanceNetworkInterfaceProgrammed, metav1.ConditionFalse)
		}},
		{name: "interface programming unknown", mutate: func(i *computev1alpha.Instance) {
			setCondition(&i.Status.NetworkInterfaces[0].Conditions, computev1alpha.InstanceNetworkInterfaceProgrammed, metav1.ConditionUnknown)
		}},
		{name: "interface not allocated", mutate: func(i *computev1alpha.Instance) {
			setCondition(&i.Status.NetworkInterfaces[0].Conditions, computev1alpha.InstanceNetworkInterfaceAllocated, metav1.ConditionFalse)
		}},
		{name: "interface not bound", mutate: func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces[0].NetworkInterfaceRef = nil
		}},
		{name: "interface not reported", mutate: func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces = nil
		}},
		{name: "terminating instance", mutate: func(i *computev1alpha.Instance) {
			now := metav1.Now()
			i.DeletionTimestamp = &now
		}},
		{name: "network in another namespace", wantErr: true, mutate: func(i *computev1alpha.Instance) {
			i.Spec.NetworkInterfaces[0].Network.Namespace = internalDNSTestForeignValue
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance := &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, Namespace: internalDNSTestObservationNamespace, UID: "project-instance", Labels: map[string]string{computev1alpha.LocationLabel: "dfw"}},
				Spec:       computev1alpha.InstanceSpec{NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{{Network: networkingv1alpha.NetworkRef{Name: internalDNSTestNetworkName}}}},
				Status: computev1alpha.InstanceStatus{
					Conditions: []metav1.Condition{
						{Type: computev1alpha.InstanceReady, Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceReadyReasonAvailable},
						{Type: computev1alpha.InstanceRuntimeReady, Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceRuntimeReadyReasonReady},
					},
					NetworkInterfaces: []computev1alpha.InstanceNetworkInterfaceStatus{{
						Name:                defaultInterfaceName,
						NetworkInterfaceRef: &networkingv1alpha.LocalNetworkInterfaceRef{Name: "interface"},
						Addresses:           []computev1alpha.InstanceNetworkInterfaceAddress{{Family: networkingv1alpha.IPv4Protocol, Address: observationTestAddress, Gateway: "10.20.0.1", Primary: true}},
						Conditions: []metav1.Condition{
							{Type: computev1alpha.InstanceNetworkInterfaceAllocated, Status: metav1.ConditionTrue, Reason: "Allocated"},
							{Type: computev1alpha.InstanceNetworkInterfaceProgrammed, Status: metav1.ConditionTrue, Reason: wdTestReasonProgrammed},
						},
					}},
				},
			}
			if tc.mutate != nil {
				tc.mutate(instance)
			}
			got, err := ObserveInternalDNSInterface(context.Background(), nil, internalDNSTestObservationNamespace, instance, instance.Spec.NetworkInterfaces[0], InternalDNSObservationSource{Reader: fake.NewClientBuilder().Build()})
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.eligible, got.Eligible)
			if tc.eligible {
				if tc.wantAddresses == nil {
					tc.wantAddresses = []networkingv1alpha.NetworkInterfaceAddress{{Family: networkingv1alpha.IPv4Protocol, Address: observationTestAddress}}
				}
				require.ElementsMatch(t, tc.wantAddresses, got.Addresses)
			} else {
				require.Empty(t, got.Addresses)
			}
		})
	}
}

func TestObserveInternalDNSInterfaceRejectsMissingSource(t *testing.T) {
	_, err := ObserveInternalDNSInterface(context.Background(), nil, internalDNSTestObservationNamespace, &computev1alpha.Instance{}, computev1alpha.InstanceNetworkInterface{}, InternalDNSObservationSource{})
	require.ErrorIs(t, err, errNetworkObservationUnconfigured)
}
