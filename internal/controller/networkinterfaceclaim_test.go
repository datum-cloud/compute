// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// TestNetworkInterfaceClaimName covers the name a claim is derived from. The
// name identifies the instance slot, so it must be stable, and it must remain a
// valid DNS subdomain even when the instance name alone approaches the limit.
func TestNetworkInterfaceClaimName(t *testing.T) {
	t.Parallel()

	t.Run("joins the instance and interface names", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "my-wd-0-eth0", networkInterfaceClaimName("my-wd-0", defaultInterfaceName))
	})

	t.Run("is stable across calls", func(t *testing.T) {
		t.Parallel()
		long := strings.Repeat("a", 250)
		assert.Equal(t, networkInterfaceClaimName(long, "eth1"), networkInterfaceClaimName(long, "eth1"))
	})

	t.Run("truncates and hashes when the joined name is too long", func(t *testing.T) {
		t.Parallel()

		long := strings.Repeat("a", 250)
		name := networkInterfaceClaimName(long, defaultInterfaceName)

		assert.LessOrEqual(t, len(name), maxObjectNameLength)
		assert.Empty(t, validation.IsDNS1123Subdomain(name),
			"the fallback must still produce a valid DNS subdomain")
		assert.NotEqual(t, long+"-"+defaultInterfaceName, name)
	})

	t.Run("distinguishes interfaces of the same over-long instance name", func(t *testing.T) {
		t.Parallel()

		long := strings.Repeat("a", 250)
		assert.NotEqual(t, networkInterfaceClaimName(long, defaultInterfaceName), networkInterfaceClaimName(long, "eth1"),
			"two interfaces of one instance must not collide on the same claim")
	})

	t.Run("does not leave a trailing separator after truncation", func(t *testing.T) {
		t.Parallel()

		// An instance name whose truncation point lands on a separator would
		// otherwise yield "...--<hash>".
		name := networkInterfaceClaimName(strings.Repeat("a", 239)+"-"+strings.Repeat("b", 20), defaultInterfaceName)
		assert.Empty(t, validation.IsDNS1123Subdomain(name))
	})
}

// TestNetworkIPProjection pins the rule for the single address clients read as
// the instance's IP: host prefixes are reported bare, and anything shorter is
// reported as the CIDR it is, because no single address in a delegated block is
// the instance's.
func TestNetworkIPProjection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		address string
		want    string
	}{
		{"IPv4 host prefix is stripped", claimTestAddressCIDR, claimTestAddress},
		{"IPv6 host prefix is stripped", "2001:db8:a001::1/128", "2001:db8:a001::1"},
		{"IPv6 delegated block keeps its prefix", "2001:db8:a001::/96", "2001:db8:a001::/96"},
		{"IPv4 subnet prefix keeps its prefix", "10.128.0.0/24", "10.128.0.0/24"},
		{"a bare address passes through", "10.128.0.2", "10.128.0.2"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, networkIPProjection(tc.address))
		})
	}
}

// TestDesiredNetworkInterfaceClaimSpec verifies the interface request is copied
// onto the claim verbatim, and that the fields the instance must not decide —
// the bound interface, the location — are left for NSO.
func TestDesiredNetworkInterfaceClaimSpec(t *testing.T) {
	t.Parallel()

	spec := desiredNetworkInterfaceClaimSpec(computev1alpha.InstanceNetworkInterface{
		Network:       networkingv1alpha.NetworkRef{Namespace: "other-namespace", Name: claimTestNetwork},
		Name:          "eth1",
		IPFamilies:    []networkingv1alpha.IPFamily{networkingv1alpha.IPv6Protocol, networkingv1alpha.IPv4Protocol},
		ReclaimPolicy: networkingv1alpha.NetworkInterfaceReclaimPolicyRetain,
		Addresses: []computev1alpha.InstanceNetworkInterfaceAddressRequest{
			{Class: claimTestClass},
		},
	})

	assert.Equal(t, claimTestNetwork, spec.Network.Name)
	assert.Equal(t, "eth1", spec.InterfaceName)
	assert.Equal(t, []networkingv1alpha.IPFamily{networkingv1alpha.IPv6Protocol, networkingv1alpha.IPv4Protocol}, spec.IPFamilies)
	assert.Equal(t, networkingv1alpha.NetworkInterfaceReclaimPolicyRetain, spec.ReclaimPolicy)
	require.Len(t, spec.Addresses, 1)
	assert.Equal(t, claimTestClass, spec.Addresses[0].Class)
	assert.Empty(t, spec.NetworkInterfaceName,
		"the claim must bind the interface of its own name so a retained one is reused")

	defaulted := desiredNetworkInterfaceClaimSpec(computev1alpha.InstanceNetworkInterface{
		Network: networkingv1alpha.NetworkRef{Name: claimTestNetwork},
	})
	assert.Equal(t, defaultInterfaceName, defaulted.InterfaceName)
}

// TestNetworkInterfaceClaimSatisfied is the regression guard for the readiness
// criterion: the gate waits for the data plane to be prepared, and never for it
// to be programmed, which only happens once the Pod the gate withholds exists.
func TestNetworkInterfaceClaimSatisfied(t *testing.T) {
	t.Parallel()

	t.Run("bound, allocated and prepared is enough, with Programmed still Unknown", func(t *testing.T) {
		t.Parallel()

		claim := &networkingv1alpha.NetworkInterfaceClaim{
			Status: networkingv1alpha.NetworkInterfaceClaimStatus{
				Conditions: []metav1.Condition{
					claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionTrue, "Bound"),
					claimCondition(networkingv1alpha.NetworkInterfaceClaimAllocated, metav1.ConditionTrue, "Allocated"),
					claimCondition(networkingv1alpha.NetworkInterfaceClaimPrepared, metav1.ConditionTrue, "Prepared"),
					claimCondition(networkingv1alpha.NetworkInterfaceClaimProgrammed, metav1.ConditionUnknown, "Pending"),
					claimCondition(networkingv1alpha.NetworkInterfaceClaimReady, metav1.ConditionUnknown, "NotProgrammed"),
				},
			},
		}

		assert.True(t, networkInterfaceClaimSatisfied(claim))
	})

	t.Run("not satisfied while the data plane has not prepared", func(t *testing.T) {
		t.Parallel()

		claim := &networkingv1alpha.NetworkInterfaceClaim{
			Status: networkingv1alpha.NetworkInterfaceClaimStatus{
				Conditions: []metav1.Condition{
					claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionTrue, "Bound"),
					claimCondition(networkingv1alpha.NetworkInterfaceClaimAllocated, metav1.ConditionTrue, "Allocated"),
					claimCondition(networkingv1alpha.NetworkInterfaceClaimPrepared, metav1.ConditionUnknown, "Pending"),
				},
			},
		}

		assert.False(t, networkInterfaceClaimSatisfied(claim),
			"a Pod created before the data plane is ready for it cannot be attached")
	})

	t.Run("not satisfied while allocation is outstanding", func(t *testing.T) {
		t.Parallel()

		claim := &networkingv1alpha.NetworkInterfaceClaim{
			Status: networkingv1alpha.NetworkInterfaceClaimStatus{
				Conditions: []metav1.Condition{
					claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionTrue, "Bound"),
					claimCondition(networkingv1alpha.NetworkInterfaceClaimAllocated, metav1.ConditionFalse, "AddressPoolExhausted"),
				},
			},
		}

		assert.False(t, networkInterfaceClaimSatisfied(claim))
	})

	t.Run("not satisfied before the controller reports anything", func(t *testing.T) {
		t.Parallel()
		assert.False(t, networkInterfaceClaimSatisfied(&networkingv1alpha.NetworkInterfaceClaim{}))
	})
}

// TestNetworkInterfaceClaimRejection verifies the refusal reason reaches the
// caller verbatim, and that a claim merely waiting reports no refusal.
func TestNetworkInterfaceClaimRejection(t *testing.T) {
	t.Parallel()

	claim := &networkingv1alpha.NetworkInterfaceClaim{
		Status: networkingv1alpha.NetworkInterfaceClaimStatus{
			Conditions: []metav1.Condition{
				claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionFalse, "NetworkNotFound"),
			},
		},
	}
	reason, message := networkInterfaceClaimRejection(claim)
	assert.Equal(t, "NetworkNotFound", reason)
	assert.NotEmpty(t, message)

	pending := &networkingv1alpha.NetworkInterfaceClaim{
		Status: networkingv1alpha.NetworkInterfaceClaimStatus{
			Conditions: []metav1.Condition{
				claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionUnknown, "Pending"),
			},
		},
	}
	reason, _ = networkInterfaceClaimRejection(pending)
	assert.Empty(t, reason, "a claim that has not been answered yet is not a refusal")
}

// TestInstanceNetworkInterfaceStatus verifies the projection onto the instance:
// addresses and external addresses are carried across, the primary address
// feeds the single-address networkIP field, and only the conditions describing
// the interface itself are mirrored.
func TestInstanceNetworkInterfaceStatus(t *testing.T) {
	t.Parallel()

	claim := &networkingv1alpha.NetworkInterfaceClaim{
		Status: networkingv1alpha.NetworkInterfaceClaimStatus{
			Addresses: []networkingv1alpha.NetworkInterfaceAddress{
				{
					Family:  networkingv1alpha.IPv6Protocol,
					Address: "2001:db8:a001::1/128",
					Gateway: "2001:db8:a001::",
					Primary: true,
				},
				{
					Family:  networkingv1alpha.IPv4Protocol,
					Address: claimTestAddressCIDR,
					Class:   "private-ipv4",
				},
			},
			ExternalAddresses: []networkingv1alpha.NetworkInterfaceExternalAddress{
				{Family: networkingv1alpha.IPv4Protocol, Address: claimTestExternalIP, Class: claimTestClass},
			},
			Conditions: []metav1.Condition{
				claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionTrue, "Bound"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimAllocated, metav1.ConditionTrue, "Allocated"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimProgrammed, metav1.ConditionUnknown, "Pending"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimReady, metav1.ConditionUnknown, "NotProgrammed"),
			},
		},
	}

	status := instanceNetworkInterfaceStatus(defaultInterfaceName, claim)

	assert.Equal(t, defaultInterfaceName, status.Name)
	require.Len(t, status.Addresses, 2)
	assert.Equal(t, "2001:db8:a001::1/128", status.Addresses[0].Address,
		"the interface addresses keep their prefix length")
	assert.Equal(t, "2001:db8:a001::", status.Addresses[0].Gateway)
	require.Len(t, status.ExternalAddresses, 1)

	require.NotNil(t, status.Assignments.NetworkIP)
	assert.Equal(t, "2001:db8:a001::1", *status.Assignments.NetworkIP,
		"networkIP projects the primary address, bare")
	require.NotNil(t, status.Assignments.ExternalIP)
	assert.Equal(t, claimTestExternalIP, *status.Assignments.ExternalIP)

	conditionTypes := make([]string, 0, len(status.Conditions))
	for _, condition := range status.Conditions {
		conditionTypes = append(conditionTypes, condition.Type)
		assert.Zero(t, condition.ObservedGeneration,
			"the claim's generation says nothing about the instance")
	}
	assert.Equal(t, []string{
		computev1alpha.InstanceNetworkInterfaceAllocated,
		computev1alpha.InstanceNetworkInterfaceProgrammed,
	}, conditionTypes)
}

// TestInstanceNetworkInterfaceStatus_NoClaim verifies an interface whose claim
// does not exist yet is still reported by name, so the status has the shape of
// the spec from the start.
func TestInstanceNetworkInterfaceStatus_NoClaim(t *testing.T) {
	t.Parallel()

	status := instanceNetworkInterfaceStatus(defaultInterfaceName, nil)

	assert.Equal(t, defaultInterfaceName, status.Name)
	assert.Empty(t, status.Addresses)
	assert.Nil(t, status.Assignments.NetworkIP)
}

// claimCondition builds a claim status condition with a message, mirroring the
// shape NSO writes.
func claimCondition(conditionType string, status metav1.ConditionStatus, reason string) metav1.Condition {
	return metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            "condition message for " + conditionType,
		LastTransitionTime: metav1.Now(),
	}
}
