// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"

	"go.datum.net/compute/internal/controller/instancecontrol"
)

const (
	claimTestNamespace  = "ns-aabbccdd-0000-1111-2222-333344445555"
	claimTestDeployment = "claim-test-wd"
	claimTestNetwork    = "default"

	// claimTestClass and the addresses below mirror what NSO publishes on a
	// bound claim: a class-allocated external address, and an interface address
	// in CIDR notation.
	claimTestClass       = "public-ipv4"
	claimTestAddress     = "10.128.0.2"
	claimTestAddressCIDR = claimTestAddress + "/32"
	claimTestExternalIP  = "203.0.113.10"
)

// newClaimTestScheme builds a scheme carrying compute and networking types, the
// pair a cell serves once the networking integration is on.
func newClaimTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = computev1alpha.AddToScheme(s)
	_ = networkingv1alpha.AddToScheme(s)
	return s
}

// newClaimTestDeployment builds a deployment shaped the way the federator
// delivers it to a cell.
func newClaimTestDeployment() *computev1alpha.WorkloadDeployment {
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claimTestDeployment,
			Namespace: claimTestNamespace,
			UID:       "claim-test-wd-uid",
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode:    wdControllerTestCityCode,
			WorkloadRef: computev1alpha.WorkloadReference{Name: "claim-test-workload"},
		},
	}
}

// newClaimTestInstance builds an instance with the given interfaces and the
// scheduling gates the instance-control strategy stamps at creation.
func newClaimTestInstance(name string, interfaces ...computev1alpha.InstanceNetworkInterface) *computev1alpha.Instance {
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         claimTestNamespace,
			CreationTimestamp: metav1.Now(),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: computev1alpha.GroupVersion.String(),
				Kind:       kindWorkloadDeployment,
				Name:       claimTestDeployment,
				UID:        "claim-test-wd-uid",
				Controller: new(true),
			}},
		},
		Spec: computev1alpha.InstanceSpec{
			NetworkInterfaces: interfaces,
			Controller: &computev1alpha.InstanceController{
				SchedulingGates: []computev1alpha.SchedulingGate{
					{Name: instancecontrol.NetworkSchedulingGate.String()},
					{Name: instancecontrol.QuotaSchedulingGate.String()},
				},
			},
		},
	}
}

// TestReconcileNetworkInterfaceClaims_CreatesClaimPerInterface verifies one
// claim is created per instance interface, named after the slot, owned by the
// instance, and carrying the interface request verbatim.
func TestReconcileNetworkInterfaceClaims_CreatesClaimPerInterface(t *testing.T) {
	t.Parallel()

	deployment := newClaimTestDeployment()
	instance := newClaimTestInstance(claimTestDeployment+"-0",
		computev1alpha.InstanceNetworkInterface{
			Network:    networkingv1alpha.NetworkRef{Name: claimTestNetwork},
			Name:       defaultInterfaceName,
			IPFamilies: []networkingv1alpha.IPFamily{networkingv1alpha.IPv6Protocol},
		},
		computev1alpha.InstanceNetworkInterface{
			Network:       networkingv1alpha.NetworkRef{Name: claimTestNetwork},
			Name:          "eth1",
			IPFamilies:    []networkingv1alpha.IPFamily{networkingv1alpha.IPv4Protocol},
			ReclaimPolicy: networkingv1alpha.NetworkInterfaceReclaimPolicyRetain,
			Addresses: []computev1alpha.InstanceNetworkInterfaceAddressRequest{
				{Class: claimTestClass},
			},
		},
	)

	cl := fake.NewClientBuilder().
		WithScheme(newClaimTestScheme()).
		WithObjects(deployment, instance).
		Build()

	r := &WorkloadDeploymentReconciler{NetworkingEnabled: true}
	ready, err := r.reconcileNetworkInterfaceClaims(context.Background(), cl, deployment,
		[]computev1alpha.Instance{*instance})
	require.NoError(t, err)
	assert.False(t, ready[instance.Name],
		"a freshly created claim holds no addresses yet")

	var claims networkingv1alpha.NetworkInterfaceClaimList
	require.NoError(t, cl.List(context.Background(), &claims, client.InNamespace(claimTestNamespace)))
	require.Len(t, claims.Items, 2)

	byName := map[string]networkingv1alpha.NetworkInterfaceClaim{}
	for _, claim := range claims.Items {
		byName[claim.Name] = claim
	}

	eth0, ok := byName[instance.Name+"-eth0"]
	require.True(t, ok, "the claim is named after the instance slot and the interface")
	assert.Equal(t, claimTestNetwork, eth0.Spec.Network.Name)
	assert.Equal(t, defaultInterfaceName, eth0.Spec.InterfaceName)
	assert.Equal(t, []networkingv1alpha.IPFamily{networkingv1alpha.IPv6Protocol}, eth0.Spec.IPFamilies)
	assert.Empty(t, eth0.Spec.NetworkInterfaceName)

	owner := metav1.GetControllerOf(&eth0)
	require.NotNil(t, owner)
	assert.Equal(t, "Instance", owner.Kind,
		"the claim is owned by the instance, so ending the slot releases it")
	assert.Equal(t, instance.Name, owner.Name)

	eth1, ok := byName[instance.Name+"-eth1"]
	require.True(t, ok)
	assert.Equal(t, networkingv1alpha.NetworkInterfaceReclaimPolicyRetain, eth1.Spec.ReclaimPolicy)
	require.Len(t, eth1.Spec.Addresses, 1)
	assert.Equal(t, claimTestClass, eth1.Spec.Addresses[0].Class)
}

// TestReconcileNetworkInterfaceClaims_ReadinessPerInstance verifies readiness is
// reported per instance: an instance whose claims are bound and allocated is
// ready even while a sibling's claim is still pending.
func TestReconcileNetworkInterfaceClaims_ReadinessPerInstance(t *testing.T) {
	t.Parallel()

	deployment := newClaimTestDeployment()
	networkInterface := computev1alpha.InstanceNetworkInterface{
		Network: networkingv1alpha.NetworkRef{Name: claimTestNetwork},
		Name:    defaultInterfaceName,
	}
	allocated := newClaimTestInstance(claimTestDeployment+"-0", networkInterface)
	pending := newClaimTestInstance(claimTestDeployment+"-1", networkInterface)

	allocatedClaim := &networkingv1alpha.NetworkInterfaceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: allocated.Name + "-eth0", Namespace: claimTestNamespace},
		Status: networkingv1alpha.NetworkInterfaceClaimStatus{
			Conditions: []metav1.Condition{
				claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionTrue, "Bound"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimAllocated, metav1.ConditionTrue, "Allocated"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimProgrammed, metav1.ConditionUnknown, "Pending"),
			},
		},
	}
	pendingClaim := &networkingv1alpha.NetworkInterfaceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: pending.Name + "-eth0", Namespace: claimTestNamespace},
		Status: networkingv1alpha.NetworkInterfaceClaimStatus{
			Conditions: []metav1.Condition{
				claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionTrue, "Bound"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimAllocated, metav1.ConditionFalse, "AddressPoolExhausted"),
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(newClaimTestScheme()).
		WithObjects(deployment, allocated, pending, allocatedClaim, pendingClaim).
		Build()

	r := &WorkloadDeploymentReconciler{NetworkingEnabled: true}
	ready, err := r.reconcileNetworkInterfaceClaims(context.Background(), cl, deployment,
		[]computev1alpha.Instance{*allocated, *pending})
	require.NoError(t, err)

	assert.True(t, ready[allocated.Name],
		"Bound and Allocated is the whole criterion; Programmed is never set today")
	assert.False(t, ready[pending.Name])
}

// TestReconcileInstanceGates_NetworkGatePerInstance is the regression test for
// the readiness criterion. An instance whose claim is bound and allocated boots
// even though Programmed — and therefore Ready — is still Unknown, and an
// instance whose allocation is outstanding stays gated.
func TestReconcileInstanceGates_NetworkGatePerInstance(t *testing.T) {
	t.Parallel()

	deployment := newClaimTestDeployment()
	networkInterface := computev1alpha.InstanceNetworkInterface{
		Network: networkingv1alpha.NetworkRef{Name: claimTestNetwork},
		Name:    defaultInterfaceName,
	}
	allocated := newClaimTestInstance(claimTestDeployment+"-0", networkInterface)
	pending := newClaimTestInstance(claimTestDeployment+"-1", networkInterface)

	cl := fake.NewClientBuilder().
		WithScheme(newClaimTestScheme()).
		WithObjects(deployment, allocated, pending).
		WithStatusSubresource(allocated, pending).
		Build()

	r := &WorkloadDeploymentReconciler{NetworkingEnabled: true}
	_, _, _, _, _, err := r.reconcileInstanceGates(
		context.Background(),
		cl,
		deployment,
		[]computev1alpha.Instance{*allocated, *pending},
		map[string]bool{allocated.Name: true, pending.Name: false},
	)
	require.NoError(t, err)

	gateNames := func(name string) []string {
		var instance computev1alpha.Instance
		require.NoError(t, cl.Get(context.Background(),
			client.ObjectKey{Namespace: claimTestNamespace, Name: name}, &instance))
		require.NotNil(t, instance.Spec.Controller)
		names := make([]string, 0, len(instance.Spec.Controller.SchedulingGates))
		for _, gate := range instance.Spec.Controller.SchedulingGates {
			names = append(names, gate.Name)
		}
		return names
	}

	assert.Equal(t, []string{instancecontrol.QuotaSchedulingGate.String()}, gateNames(allocated.Name),
		"the Network gate is released once the instance's own claims hold their addresses")
	assert.Equal(t, []string{
		instancecontrol.NetworkSchedulingGate.String(),
		instancecontrol.QuotaSchedulingGate.String(),
	}, gateNames(pending.Name),
		"an instance whose allocation is outstanding stays gated")
}

// TestReconcileNetworkInterfaceStatus verifies the instance publishes the
// addresses its claims hold, and that a second pass over unchanged claims
// reports no change so the reconciler does not rewrite status on every event.
func TestReconcileNetworkInterfaceStatus(t *testing.T) {
	t.Parallel()

	instance := newClaimTestInstance(claimTestDeployment+"-0",
		computev1alpha.InstanceNetworkInterface{
			Network: networkingv1alpha.NetworkRef{Name: claimTestNetwork},
			Name:    defaultInterfaceName,
		})

	claim := &networkingv1alpha.NetworkInterfaceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: instance.Name + "-eth0", Namespace: claimTestNamespace},
		Status: networkingv1alpha.NetworkInterfaceClaimStatus{
			Addresses: []networkingv1alpha.NetworkInterfaceAddress{
				{Family: networkingv1alpha.IPv4Protocol, Address: claimTestAddressCIDR, Primary: true},
			},
			ExternalAddresses: []networkingv1alpha.NetworkInterfaceExternalAddress{
				{Family: networkingv1alpha.IPv4Protocol, Address: claimTestExternalIP, Class: claimTestClass},
			},
			Conditions: []metav1.Condition{
				claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionTrue, "Bound"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimAllocated, metav1.ConditionTrue, "Allocated"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimProgrammed, metav1.ConditionUnknown, "Pending"),
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(newClaimTestScheme()).
		WithObjects(instance, claim).
		Build()

	r := &InstanceReconciler{NetworkingEnabled: true}

	changed, err := r.reconcileNetworkInterfaceStatus(context.Background(), cl, instance)
	require.NoError(t, err)
	require.True(t, changed)

	require.Len(t, instance.Status.NetworkInterfaces, 1)
	published := instance.Status.NetworkInterfaces[0]
	assert.Equal(t, defaultInterfaceName, published.Name)
	require.NotNil(t, published.Assignments.NetworkIP)
	assert.Equal(t, claimTestAddress, *published.Assignments.NetworkIP)
	require.NotNil(t, published.Assignments.ExternalIP)
	assert.Equal(t, claimTestExternalIP, *published.Assignments.ExternalIP)
	assert.Len(t, published.Conditions, 2)

	changed, err = r.reconcileNetworkInterfaceStatus(context.Background(), cl, instance)
	require.NoError(t, err)
	assert.False(t, changed, "an unchanged claim must not rewrite the instance status")
}

// TestReconcileNetworkInterfaceStatus_NetworkingDisabled verifies nothing is
// read or published on a cell without the networking integration, where the
// claim CRD is absent.
func TestReconcileNetworkInterfaceStatus_NetworkingDisabled(t *testing.T) {
	t.Parallel()

	instance := newClaimTestInstance(claimTestDeployment+"-0",
		computev1alpha.InstanceNetworkInterface{
			Network: networkingv1alpha.NetworkRef{Name: claimTestNetwork},
		})

	r := &InstanceReconciler{}
	changed, err := r.reconcileNetworkInterfaceStatus(context.Background(), nil, instance)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Empty(t, instance.Status.NetworkInterfaces)
}

// TestCheckForNetworkCreationFailure_SurfacesClaimRejection verifies a refused
// claim reaches the instance with NSO's own reason, and that a claim still
// waiting is not reported as a failure.
func TestCheckForNetworkCreationFailure_SurfacesClaimRejection(t *testing.T) {
	t.Parallel()

	instance := newClaimTestInstance(claimTestDeployment+"-0",
		computev1alpha.InstanceNetworkInterface{
			Network: networkingv1alpha.NetworkRef{Name: claimTestNetwork},
			Name:    defaultInterfaceName,
		})

	rejected := &networkingv1alpha.NetworkInterfaceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: instance.Name + "-eth0", Namespace: claimTestNamespace},
		Status: networkingv1alpha.NetworkInterfaceClaimStatus{
			Conditions: []metav1.Condition{
				{
					Type:    networkingv1alpha.NetworkInterfaceClaimBound,
					Status:  metav1.ConditionFalse,
					Reason:  "NetworkNotFound",
					Message: `Network "default" was not found`,
				},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(newClaimTestScheme()).
		WithObjects(instance, rejected).
		Build()

	r := &InstanceReconciler{NetworkingEnabled: true}
	failed, message, err := r.checkForNetworkCreationFailure(context.Background(), cl, instance)
	require.NoError(t, err)
	assert.True(t, failed)
	assert.Contains(t, message, "NetworkNotFound", "NSO's reason must reach the user unaltered")
	assert.Contains(t, message, `Network "default" was not found`)

	// A claim that has not been created yet is a wait, not a failure.
	empty := fake.NewClientBuilder().WithScheme(newClaimTestScheme()).WithObjects(instance).Build()
	failed, _, err = r.checkForNetworkCreationFailure(context.Background(), empty, instance)
	require.NoError(t, err)
	assert.False(t, failed)
}

// TestInstancePublishesTheBoundNetworkInterface pins the reference a provider
// follows. Without it a provider has to rebuild the claim name from compute's
// own convention, which is private and changes without notice.
func TestInstancePublishesTheBoundNetworkInterface(t *testing.T) {
	t.Parallel()

	instance := newClaimTestInstance(claimTestDeployment+"-0",
		computev1alpha.InstanceNetworkInterface{
			Network: networkingv1alpha.NetworkRef{Name: claimTestNetwork},
			Name:    defaultInterfaceName,
		})

	claim := &networkingv1alpha.NetworkInterfaceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: instance.Name + "-eth0", Namespace: claimTestNamespace},
		Status: networkingv1alpha.NetworkInterfaceClaimStatus{
			NetworkInterfaceRef: &networkingv1alpha.LocalNetworkInterfaceRef{Name: "nic-4f2a9c1e"},
			Addresses: []networkingv1alpha.NetworkInterfaceAddress{
				{Family: networkingv1alpha.IPv4Protocol, Address: claimTestAddressCIDR, Primary: true},
			},
			Conditions: []metav1.Condition{
				claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionTrue, "Bound"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimAllocated, metav1.ConditionTrue, "Allocated"),
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(newClaimTestScheme()).
		WithObjects(instance, claim).
		Build()

	r := &InstanceReconciler{NetworkingEnabled: true}
	changed, err := r.reconcileNetworkInterfaceStatus(context.Background(), cl, instance)
	require.NoError(t, err)
	require.True(t, changed)

	require.Len(t, instance.Status.NetworkInterfaces, 1)
	published := instance.Status.NetworkInterfaces[0]
	require.NotNil(t, published.NetworkInterfaceRef,
		"a provider reads the interface through this reference")
	assert.Equal(t, "nic-4f2a9c1e", published.NetworkInterfaceRef.Name)

	// An unbound claim publishes no reference rather than an empty one.
	unbound := instanceNetworkInterfaceStatus(defaultInterfaceName,
		&networkingv1alpha.NetworkInterfaceClaim{})
	assert.Nil(t, unbound.NetworkInterfaceRef)
	assert.Nil(t, instanceNetworkInterfaceStatus(defaultInterfaceName, nil).NetworkInterfaceRef)
}

// TestNetworkGateReleasesWhileProgrammedIsNotTrue is the deadlock guard. The
// data plane reports Programmed at sandbox creation, and the infrastructure
// provider will not create the sandbox while a gate remains, so the Network gate
// has to release on Bound and Allocated no matter what Programmed says.
func TestNetworkGateReleasesWhileProgrammedIsNotTrue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		programmed metav1.ConditionStatus
		reason     string
	}{
		{name: "unknown", programmed: metav1.ConditionUnknown, reason: "Pending"},
		{name: "false", programmed: metav1.ConditionFalse, reason: "AttachmentPending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			deployment := newClaimTestDeployment()
			networkInterface := computev1alpha.InstanceNetworkInterface{
				Network: networkingv1alpha.NetworkRef{Name: claimTestNetwork},
				Name:    defaultInterfaceName,
			}
			instance := newClaimTestInstance(claimTestDeployment+"-0", networkInterface)

			claim := &networkingv1alpha.NetworkInterfaceClaim{
				ObjectMeta: metav1.ObjectMeta{Name: instance.Name + "-eth0", Namespace: claimTestNamespace},
				Status: networkingv1alpha.NetworkInterfaceClaimStatus{
					Conditions: []metav1.Condition{
						claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionTrue, "Bound"),
						claimCondition(networkingv1alpha.NetworkInterfaceClaimAllocated, metav1.ConditionTrue, "Allocated"),
						claimCondition(networkingv1alpha.NetworkInterfaceClaimProgrammed, tc.programmed, tc.reason),
						claimCondition(networkingv1alpha.NetworkInterfaceClaimReady, tc.programmed, "NotProgrammed"),
					},
				},
			}

			cl := fake.NewClientBuilder().
				WithScheme(newClaimTestScheme()).
				WithObjects(deployment, instance, claim).
				WithStatusSubresource(instance).
				Build()

			r := &WorkloadDeploymentReconciler{NetworkingEnabled: true}
			ready, err := r.reconcileNetworkInterfaceClaims(context.Background(), cl, deployment,
				[]computev1alpha.Instance{*instance})
			require.NoError(t, err)
			require.True(t, ready[instance.Name],
				"an unprogrammed claim still holds its addresses, which is the whole criterion")

			_, _, _, _, _, err = r.reconcileInstanceGates(
				context.Background(), cl, deployment,
				[]computev1alpha.Instance{*instance}, ready,
			)
			require.NoError(t, err)

			var gated computev1alpha.Instance
			require.NoError(t, cl.Get(context.Background(),
				client.ObjectKey{Namespace: claimTestNamespace, Name: instance.Name}, &gated))
			require.NotNil(t, gated.Spec.Controller)
			for _, gate := range gated.Spec.Controller.SchedulingGates {
				assert.NotEqual(t, instancecontrol.NetworkSchedulingGate.String(), gate.Name,
					"holding the Network gate for Programmed deadlocks: no sandbox, no Programmed")
			}

			// The condition still reaches the consumer, just through status.
			published := instanceNetworkInterfaceStatus(defaultInterfaceName, claim)
			mirrored := false
			for _, condition := range published.Conditions {
				if condition.Type == networkingv1alpha.NetworkInterfaceClaimProgrammed {
					mirrored = true
					assert.Equal(t, tc.programmed, condition.Status)
				}
			}
			assert.True(t, mirrored, "Programmed is reported on the instance rather than gated on")
		})
	}
}
