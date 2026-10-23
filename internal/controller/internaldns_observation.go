// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InternalDNSObservedInterface reports confirmed interface eligibility.
type InternalDNSObservedInterface struct {
	Addresses []networkingv1alpha.NetworkInterfaceAddress
	Eligible  bool
}

type InternalDNSObserveFunc func(context.Context, client.Reader, string, *computev1alpha.Instance, computev1alpha.InstanceNetworkInterface) (InternalDNSObservedInterface, error)

// errNetworkObservationUnconfigured marks a location that internal DNS does
// not serve. It is reported in status rather than retried as a failure.
var errNetworkObservationUnconfigured = errors.New("no authenticated network observation source is configured")

// InternalDNSObservationSource supplies an authenticated edge API reader.
type InternalDNSObservationSource struct {
	Reader client.Reader
}

// ObserveInternalDNSInterface decides eligibility from the project Instance
// alone. Instance Ready already accounts for the runtime and every interface,
// so the publisher follows it rather than reading the edge cluster itself.
func ObserveInternalDNSInterface(_ context.Context, _ client.Reader, namespace string, instance *computev1alpha.Instance, nic computev1alpha.InstanceNetworkInterface, source InternalDNSObservationSource) (InternalDNSObservedInterface, error) {
	var result InternalDNSObservedInterface
	if nic.Network.Namespace != "" && nic.Network.Namespace != namespace {
		return result, fmt.Errorf("interface observations require a network in the Instance namespace")
	}
	if source.Reader == nil {
		return result, fmt.Errorf("%w for location %q", errNetworkObservationUnconfigured, instance.Labels[computev1alpha.LocationLabel])
	}
	if !instance.DeletionTimestamp.IsZero() || !apimeta.IsStatusConditionTrue(instance.Status.Conditions, computev1alpha.InstanceReady) {
		return result, nil
	}
	// Ready tolerates a runtime that was never observed, for runtimes that do
	// not run instances as Pods. Every runtime private DNS serves does, so once
	// RuntimeReady is reported it must be True, including when it is Unknown.
	if runtimeCondition := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceRuntimeReady); runtimeCondition != nil && runtimeCondition.Status != metav1.ConditionTrue {
		return result, nil
	}
	var status *computev1alpha.InstanceNetworkInterfaceStatus
	for i := range instance.Status.NetworkInterfaces {
		if instance.Status.NetworkInterfaces[i].Name == internalDNSInterfaceName(nic) {
			status = &instance.Status.NetworkInterfaces[i]
		}
	}
	if status == nil || status.NetworkInterfaceRef == nil {
		return result, nil
	}
	for _, conditionType := range []string{computev1alpha.InstanceNetworkInterfaceAllocated, computev1alpha.InstanceNetworkInterfaceProgrammed} {
		if !apimeta.IsStatusConditionTrue(status.Conditions, conditionType) {
			return result, nil
		}
	}
	allocations := make([]networkingv1alpha.NetworkInterfaceAddress, 0, len(status.Addresses))
	for _, address := range status.Addresses {
		allocations = append(allocations, networkingv1alpha.NetworkInterfaceAddress{Family: address.Family, Address: address.Address})
	}
	result.Addresses = interfaceHostAddresses(allocations)
	result.Eligible = true
	return result, nil
}

// interfaceHostAddresses returns the guest host address for each allocation.
// Galactic assigns a delegated IPv6 block to the guest by the address it is
// written with, so fd20::5:0:0/96 is configured as fd20::5:0:0. Runtimes do
// not reliably report Pod IPs (Kraftlet never does), so the allocation is the
// only source that covers every runtime.
func interfaceHostAddresses(allocations []networkingv1alpha.NetworkInterfaceAddress) []networkingv1alpha.NetworkInterfaceAddress {
	var addresses []networkingv1alpha.NetworkInterfaceAddress
	seen := map[netip.Addr]bool{}
	for _, allocation := range allocations {
		observed := allocation
		address, ok := hostAddress(allocation.Address)
		if !ok {
			prefix, err := netip.ParsePrefix(allocation.Address)
			if err != nil || !prefix.Addr().Is6() || prefix.Addr().Is4In6() {
				continue
			}
			address = prefix.Addr()
			observed.Address = address.String()
		}
		if seen[address] {
			continue
		}
		addresses = append(addresses, observed)
		seen[address] = true
	}
	return addresses
}
