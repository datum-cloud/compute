// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"net/netip"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	corev1 "k8s.io/api/core/v1"
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

// InternalDNSObservationSource supplies an authenticated edge API reader.
type InternalDNSObservationSource struct {
	Reader client.Reader
}

// ObserveInternalDNSInterface checks interface programming and runtime health.
func ObserveInternalDNSInterface(ctx context.Context, project client.Reader, namespace string, instance *computev1alpha.Instance, nic computev1alpha.InstanceNetworkInterface, source InternalDNSObservationSource) (InternalDNSObservedInterface, error) {
	var result InternalDNSObservedInterface
	if nic.Network.Namespace != "" && nic.Network.Namespace != namespace {
		return result, fmt.Errorf("interface observations require a network in the Instance namespace")
	}
	if source.Reader == nil {
		return result, fmt.Errorf("no authenticated network observation source is configured for location %q", instance.Labels[computev1alpha.LocationLabel])
	}
	var projectNS corev1.Namespace
	if err := project.Get(ctx, client.ObjectKey{Name: namespace}, &projectNS); err != nil {
		return result, err
	}
	if projectNS.UID == "" {
		return result, fmt.Errorf("project namespace has no lifetime identity")
	}
	var network networkingv1alpha.Network
	if err := project.Get(ctx, client.ObjectKey{Namespace: namespace, Name: nic.Network.Name}, &network); err != nil {
		return result, err
	}
	if network.UID == "" || !network.DeletionTimestamp.IsZero() {
		return result, nil
	}
	sourceNamespace := "ns-" + string(projectNS.UID)
	var current computev1alpha.Instance
	if err := source.Reader.Get(ctx, client.ObjectKey{Namespace: sourceNamespace, Name: instance.Name}, &current); err != nil {
		return result, err
	}
	if current.UID == "" || string(current.UID) != instance.Labels[computev1alpha.InstanceSourceUIDLabel] || !current.DeletionTimestamp.IsZero() {
		return result, fmt.Errorf("source Instance lifetime does not match the project projection")
	}
	iface, err := observeProgrammedInterface(ctx, source.Reader, &current, nic)
	if err != nil || iface == nil {
		return result, err
	}
	if err := verifyObservedNetwork(ctx, source.Reader, iface, &network, instance.Labels[computev1alpha.LocationLabel]); err != nil {
		return result, err
	}
	pod, err := observeRuntimePod(ctx, source.Reader, &current)
	if err != nil || pod == nil {
		return result, err
	}
	healthy, err := observeNodeHealth(ctx, source.Reader, pod.Spec.NodeName)
	if err != nil || !healthy {
		return result, err
	}
	result.Addresses = observedHostAddresses(iface.Spec.Addresses, pod)
	result.Eligible = true
	return result, nil
}

func verifyObservedNetwork(ctx context.Context, reader client.Reader, iface *networkingv1alpha.NetworkInterface, network *networkingv1alpha.Network, location string) error {
	if iface.Status.NetworkContextRef == nil || location == "" {
		return fmt.Errorf("interface has no verified network context")
	}
	var networkContext networkingv1alpha.NetworkContext
	if err := reader.Get(ctx, client.ObjectKey{Namespace: iface.Namespace, Name: iface.Status.NetworkContextRef.Name}, &networkContext); err != nil {
		return err
	}
	if networkContext.UID == "" || !networkContext.DeletionTimestamp.IsZero() || networkContext.Labels[networkingv1alpha.NetworkUIDLabel] != string(network.UID) || networkContext.Spec.Network.Name != network.Name || networkContext.Spec.Location.Name != location {
		return fmt.Errorf("interface network lifetime or location does not match the project")
	}
	return nil
}

func observeProgrammedInterface(ctx context.Context, reader client.Reader, current *computev1alpha.Instance, nic computev1alpha.InstanceNetworkInterface) (*networkingv1alpha.NetworkInterface, error) {
	var ifaceName string
	for _, status := range current.Status.NetworkInterfaces {
		if status.Name == internalDNSInterfaceName(nic) && status.NetworkInterfaceRef != nil {
			ifaceName = status.NetworkInterfaceRef.Name
		}
	}
	if ifaceName == "" {
		return nil, nil
	}
	var iface networkingv1alpha.NetworkInterface
	if err := reader.Get(ctx, client.ObjectKey{Namespace: current.Namespace, Name: ifaceName}, &iface); err != nil {
		return nil, err
	}
	if iface.UID == "" || !iface.DeletionTimestamp.IsZero() || iface.Spec.Network.Name != nic.Network.Name || iface.Spec.InterfaceName != internalDNSInterfaceName(nic) || iface.Spec.ClaimRef == nil {
		return nil, nil
	}
	unknown := false
	for _, kind := range []string{"Allocated", "Programmed"} {
		condition := apimeta.FindStatusCondition(iface.Status.Conditions, kind)
		if condition == nil || condition.ObservedGeneration != iface.Generation || condition.Status == metav1.ConditionUnknown {
			unknown = true
			continue
		}
		if condition.Status == metav1.ConditionFalse {
			return nil, nil
		}
	}
	if unknown {
		return nil, fmt.Errorf("interface %q programming health is unknown", iface.Name)
	}
	if iface.Spec.ClaimRef.Name != networkInterfaceClaimName(current.Name, internalDNSInterfaceName(nic)) {
		return nil, fmt.Errorf("interface is held by a different instance slot")
	}
	var claim networkingv1alpha.NetworkInterfaceClaim
	if err := reader.Get(ctx, client.ObjectKey{Namespace: current.Namespace, Name: iface.Spec.ClaimRef.Name}, &claim); err != nil {
		return nil, err
	}
	if claim.UID == "" || !claim.DeletionTimestamp.IsZero() || claim.Spec.Network.Name != iface.Spec.Network.Name || claim.Status.NetworkInterfaceRef == nil || claim.Status.NetworkInterfaceRef.Name != iface.Name {
		return nil, nil
	}
	return &iface, nil
}

func observeRuntimePod(ctx context.Context, reader client.Reader, current *computev1alpha.Instance) (*corev1.Pod, error) {
	var pods corev1.PodList
	options := []client.ListOption{client.InNamespace(current.Namespace)}
	deployment, index := current.Labels[computev1alpha.WorkloadDeploymentUIDLabel], current.Labels[computev1alpha.InstanceIndexLabel]
	if deployment != "" && index != "" {
		options = append(options, client.MatchingLabels{
			computev1alpha.WorkloadDeploymentUIDLabel: deployment,
			computev1alpha.InstanceIndexLabel:         index,
		})
	}
	if err := reader.List(ctx, &pods, options...); err != nil {
		return nil, err
	}
	var observed *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if metav1.IsControlledBy(pod, current) && pod.DeletionTimestamp.IsZero() && pod.Status.Phase == corev1.PodRunning && pod.Spec.NodeName != "" {
			if observed != nil {
				return nil, fmt.Errorf("multiple running runtime Pods own this Instance")
			}
			observed = pod
		}
	}
	return observed, nil
}

func observedHostAddresses(allocations []networkingv1alpha.NetworkInterfaceAddress, pod *corev1.Pod) []networkingv1alpha.NetworkInterfaceAddress {
	var addresses []networkingv1alpha.NetworkInterfaceAddress
	podIPs := append([]corev1.PodIP(nil), pod.Status.PodIPs...)
	podIPs = append(podIPs, corev1.PodIP{IP: pod.Status.PodIP})
	seen := map[netip.Addr]bool{}
	for _, allocation := range allocations {
		if address, ok := hostAddress(allocation.Address); ok {
			if !seen[address] {
				addresses = append(addresses, allocation)
				seen[address] = true
			}
			continue
		}
		prefix, err := netip.ParsePrefix(allocation.Address)
		if err != nil {
			continue
		}
		for _, podIP := range podIPs {
			address, err := netip.ParseAddr(podIP.IP)
			if err != nil || !prefix.Contains(address) || seen[address] {
				continue
			}
			observed := allocation
			observed.Address = address.String()
			addresses = append(addresses, observed)
			seen[address] = true
		}
	}
	return addresses
}

func observeNodeHealth(ctx context.Context, reader client.Reader, nodeName string) (bool, error) {
	var node corev1.Node
	if err := reader.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		return false, err
	}
	if node.UID == "" || !node.DeletionTimestamp.IsZero() {
		return false, nil
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type != corev1.NodeReady {
			continue
		}
		switch condition.Status {
		case corev1.ConditionTrue:
			return true, nil
		case corev1.ConditionFalse:
			return false, nil
		}
	}
	return false, fmt.Errorf("node %q health is unknown", nodeName)
}
