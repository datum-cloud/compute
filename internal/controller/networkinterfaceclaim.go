// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	// maxObjectNameLength is the DNS subdomain limit a claim name must respect.
	maxObjectNameLength = 253

	// claimNameHashLen is the length of the digest appended when a derived name
	// would exceed the limit, matching the fallback NSO uses for IPClaim names.
	claimNameHashLen = 12

	// defaultInterfaceName mirrors the API default for an interface that does not
	// name itself. Claims are named after the interface, so a name is needed even
	// for objects written before the field existed.
	defaultInterfaceName = "eth0"
)

// networkInterfaceClaimName derives a claim name from the instance slot and the
// interface within it. The name identifies the slot rather than the instance
// object, so an instance replaced by another filling the same slot derives the
// same name, binds the interface already there, and returns to the addresses it
// was holding — which is what makes reclaimPolicy Retain observable.
//
// The truncate-and-hash fallback mirrors NSO's own derivation so an over-long
// name still yields a valid, stable DNS subdomain rather than a rejected write.
func networkInterfaceClaimName(instanceName, interfaceName string) string {
	candidate := instanceName + "-" + interfaceName
	if len(candidate) <= maxObjectNameLength && len(validation.IsDNS1123Subdomain(candidate)) == 0 {
		return candidate
	}

	sum := sha256.Sum256([]byte(instanceName + "\x00" + interfaceName))
	suffix := hex.EncodeToString(sum[:])[:claimNameHashLen]

	prefix := instanceName
	if limit := maxObjectNameLength - 1 - claimNameHashLen; len(prefix) > limit {
		prefix = prefix[:limit]
	}
	return strings.TrimRight(prefix, "-.") + "-" + suffix
}

// instanceInterfaceName is the device name an interface presents to the guest,
// falling back to the API default when the field is unset.
func instanceInterfaceName(networkInterface computev1alpha.InstanceNetworkInterface) string {
	if networkInterface.Name == "" {
		return defaultInterfaceName
	}
	return networkInterface.Name
}

// desiredNetworkInterfaceClaimSpec copies an instance's interface request onto a
// claim spec. Every field carries the meaning, defaults, and immutability the
// claim API defines, so they are copied verbatim. The location is deliberately
// absent: the claim is served by the control plane the instance runs in, which
// is already location scoped. networkInterfaceName is left unset so the claim
// binds the interface of its own name — the retained interface, when there is
// one.
func desiredNetworkInterfaceClaimSpec(networkInterface computev1alpha.InstanceNetworkInterface) networkingv1alpha.NetworkInterfaceClaimSpec {
	spec := networkingv1alpha.NetworkInterfaceClaimSpec{
		Network:       networkingv1alpha.LocalNetworkRef{Name: networkInterface.Network.Name},
		InterfaceName: instanceInterfaceName(networkInterface),
		IPFamilies:    append([]networkingv1alpha.IPFamily(nil), networkInterface.IPFamilies...),
		ReclaimPolicy: networkInterface.ReclaimPolicy,
	}

	for _, address := range networkInterface.Addresses {
		spec.Addresses = append(spec.Addresses, networkingv1alpha.NetworkInterfaceAddressRequest{
			Class: address.Class,
		})
	}

	return spec
}

// networkInterfaceClaimSatisfied reports whether a claim holds the addresses the
// instance needs to boot, and whether the data plane is ready to receive it.
//
// Bound and Allocated say the addresses exist. Prepared says the data plane's
// pre-Pod artifacts exist, so a Pod created now can be attached. It becomes true
// before any Pod exists, which is what makes waiting on it safe.
//
// Programmed is excluded permanently, and the two conditions exist separately
// for this reason. Programmed becomes true when the interface is attached at
// sandbox creation, and an infrastructure provider defers creating the Pod while
// any scheduling gate remains, so a gate waiting on Programmed waits on itself:
// no Pod, no attachment; no attachment, no Programmed. Programmed reaches a
// consumer through Instance.status.networkInterfaces[].conditions, which
// instanceNetworkInterfaceStatus already mirrors, and never through the gate.
func networkInterfaceClaimSatisfied(claim *networkingv1alpha.NetworkInterfaceClaim) bool {
	return apimeta.IsStatusConditionTrue(claim.Status.Conditions, networkingv1alpha.NetworkInterfaceClaimBound) &&
		apimeta.IsStatusConditionTrue(claim.Status.Conditions, networkingv1alpha.NetworkInterfaceClaimAllocated) &&
		apimeta.IsStatusConditionTrue(claim.Status.Conditions, networkingv1alpha.NetworkInterfaceClaimPrepared)
}

// networkInterfaceClaimRejection returns the reason and message of the first
// condition reporting that a claim cannot be fulfilled, or ("", "") while it is
// merely pending. NSO's rejection reasons (NetworkNotFound, AddressPoolExhausted,
// RetainedAddressConflict, and so on) are designed to be read by a person, so
// they are passed through rather than collapsed into a generic failure.
func networkInterfaceClaimRejection(claim *networkingv1alpha.NetworkInterfaceClaim) (reason, message string) {
	for _, conditionType := range []string{
		networkingv1alpha.NetworkInterfaceClaimBound,
		networkingv1alpha.NetworkInterfaceClaimAllocated,
		networkingv1alpha.NetworkInterfaceClaimPrepared,
	} {
		condition := apimeta.FindStatusCondition(claim.Status.Conditions, conditionType)
		if condition != nil && condition.Status == metav1.ConditionFalse {
			return condition.Reason, condition.Message
		}
	}
	return "", ""
}

// instanceNetworkInterfaceStatus projects a claim's published addresses onto the
// instance status entry for one interface.
func instanceNetworkInterfaceStatus(
	interfaceName string,
	claim *networkingv1alpha.NetworkInterfaceClaim,
) computev1alpha.InstanceNetworkInterfaceStatus {
	status := computev1alpha.InstanceNetworkInterfaceStatus{Name: interfaceName}
	if claim == nil {
		return status
	}

	// Published so a provider reads the interface without deriving the claim name.
	if ref := claim.Status.NetworkInterfaceRef; ref != nil {
		status.NetworkInterfaceRef = ref.DeepCopy()
	}

	for _, address := range claim.Status.Addresses {
		status.Addresses = append(status.Addresses, computev1alpha.InstanceNetworkInterfaceAddress{
			Family:  address.Family,
			Address: address.Address,
			Gateway: address.Gateway,
			Primary: address.Primary,
			Class:   address.Class,
		})
		if address.Primary {
			status.Assignments.NetworkIP = new(networkIPProjection(address.Address))
		}
	}

	for _, address := range claim.Status.ExternalAddresses {
		status.ExternalAddresses = append(status.ExternalAddresses, computev1alpha.InstanceNetworkInterfaceExternalAddress{
			Family:  address.Family,
			Address: address.Address,
			Class:   address.Class,
		})
	}
	if len(status.ExternalAddresses) > 0 {
		status.Assignments.ExternalIP = new(status.ExternalAddresses[0].Address)
	}

	// Only the conditions describing the interface itself are mirrored. Bound and
	// Ready describe the claim object, which is an implementation detail of how
	// the interface was obtained.
	for _, conditionType := range []string{
		networkingv1alpha.NetworkInterfaceClaimAllocated,
		networkingv1alpha.NetworkInterfaceClaimPrepared,
		networkingv1alpha.NetworkInterfaceClaimProgrammed,
	} {
		if condition := apimeta.FindStatusCondition(claim.Status.Conditions, conditionType); condition != nil {
			mirrored := *condition
			// The claim's generation says nothing about the instance, and carrying
			// it would invite a reader to compare it against the wrong object.
			mirrored.ObservedGeneration = 0
			status.Conditions = append(status.Conditions, mirrored)
		}
	}

	return status
}

// networkIPProjection reduces an interface address to the single value clients
// read as "the instance's IP".
//
// A host address (/32 or /128) is reported bare, because that is what every
// existing consumer of assignments.networkIP expects. Anything shorter is a
// block delegated to the interface — an IPv6 /96, say — where no single address
// is the instance's, so the CIDR is reported unchanged rather than silently
// presenting a network address as a host one. This matches NSO's own bare
// address derivation.
func networkIPProjection(address string) string {
	prefix, err := netip.ParsePrefix(address)
	if err != nil {
		// Not a CIDR: the value is already bare, or malformed and better surfaced
		// than swallowed.
		return address
	}
	if prefix.Bits() != prefix.Addr().BitLen() {
		return address
	}
	return prefix.Addr().String()
}

// desiredNetworkInterfaceClaimLabels returns the well-known labels compute
// stamps on the claims it creates, so a networking NetworkService can select
// claim membership by label without a consumer labelling anything first.
//
// The keys are the ones already stamped on every Instance, reused rather than
// reinvented, and are sourced from the deployment that drove the instance. A key
// whose source is empty is omitted rather than stamped blank, so a selector on
// it matches nothing instead of matching every unset claim.
func desiredNetworkInterfaceClaimLabels(
	deployment *computev1alpha.WorkloadDeployment,
	instance *computev1alpha.Instance,
) map[string]string {
	candidates := map[string]string{
		computev1alpha.WorkloadNameLabel:  deployment.Spec.WorkloadRef.Name,
		computev1alpha.PlacementNameLabel: deployment.Spec.PlacementName,
		computev1alpha.CityCodeLabel:      deployment.Spec.CityCode,
		computev1alpha.InstanceIndexLabel: instance.Labels[computev1alpha.InstanceIndexLabel],
	}

	labels := make(map[string]string, len(candidates))
	for key, value := range candidates {
		if value != "" {
			labels[key] = value
		}
	}
	return labels
}

// networkInterfaceClaimLabelsStale reports whether any label compute owns is
// absent from or differs on a live claim. Only the keys compute sets are
// considered: networking stamps its own keys on the claim, and a key compute
// does not set is none of its business.
func networkInterfaceClaimLabelsStale(current, desired map[string]string) bool {
	for key, value := range desired {
		if current[key] != value {
			return true
		}
	}
	return false
}
