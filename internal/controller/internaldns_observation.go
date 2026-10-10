// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"time"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const dnsNodeObservationLifetime = 45 * time.Second

// InternalDNSObservedInterface is a live interface observation bounded by node liveness.
// The original deadline is preserved when publishing the contribution.
type InternalDNSObservedInterface struct {
	Addresses  []networkingv1alpha.NetworkInterfaceAddress
	Eligible   bool
	ValidUntil time.Time
}

type InternalDNSObserveFunc func(context.Context, client.Reader, string, *computev1alpha.Instance, computev1alpha.InstanceNetworkInterface) (InternalDNSObservedInterface, error)

// InternalDNSObservationSource is an authenticated, uncached edge API reader.
// ClusterUID pins the kube-system namespace UID of the selected edge cluster.
type InternalDNSObservationSource struct {
	Reader     client.Reader
	ClusterUID types.UID
}

// ObserveInternalDNSInterface validates source identity, interface programming,
// runtime placement, and a current node heartbeat. Projected positive status is
// never sufficient to renew a private address.
func ObserveInternalDNSInterface(ctx context.Context, project client.Reader, namespace string, instance *computev1alpha.Instance, nic computev1alpha.InstanceNetworkInterface, source InternalDNSObservationSource, now time.Time) (InternalDNSObservedInterface, error) {
	var result InternalDNSObservedInterface
	if nic.Network.Namespace != "" && nic.Network.Namespace != namespace {
		return result, fmt.Errorf("interface observations require a network in the Instance namespace")
	}
	if source.Reader == nil || source.ClusterUID == "" {
		return result, fmt.Errorf("no authenticated network observation source is configured for location %q", instance.Labels[computev1alpha.LocationLabel])
	}
	var cluster, projectNS corev1.Namespace
	if err := source.Reader.Get(ctx, client.ObjectKey{Name: "kube-system"}, &cluster); err != nil {
		return result, err
	}
	if cluster.UID != source.ClusterUID {
		return result, fmt.Errorf("network observation source cluster lifetime does not match")
	}
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
	nodeName, err := observeRuntimeNode(ctx, source.Reader, &current)
	if err != nil || nodeName == "" {
		return result, err
	}
	deadline, err := observeNodeDeadline(ctx, source.Reader, nodeName, now)
	if err != nil || deadline.IsZero() {
		return result, err
	}
	result.Addresses = append([]networkingv1alpha.NetworkInterfaceAddress(nil), iface.Spec.Addresses...)
	result.Eligible = true
	result.ValidUntil = deadline
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
	for _, kind := range []string{"Allocated", "Programmed"} {
		condition := apimeta.FindStatusCondition(iface.Status.Conditions, kind)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != iface.Generation {
			return nil, nil
		}
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

func observeRuntimeNode(ctx context.Context, reader client.Reader, current *computev1alpha.Instance) (string, error) {
	var pods corev1.PodList
	if err := reader.List(ctx, &pods, client.InNamespace(current.Namespace)); err != nil {
		return "", err
	}
	var nodeName string
	for i := range pods.Items {
		pod := &pods.Items[i]
		if metav1.IsControlledBy(pod, current) && pod.DeletionTimestamp.IsZero() && pod.Status.Phase == corev1.PodRunning && pod.Spec.NodeName != "" {
			if nodeName != "" {
				return "", fmt.Errorf("multiple running runtime Pods own this Instance")
			}
			nodeName = pod.Spec.NodeName
		}
	}
	return nodeName, nil
}

func observeNodeDeadline(ctx context.Context, reader client.Reader, nodeName string, now time.Time) (time.Time, error) {
	var node corev1.Node
	if err := reader.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		return time.Time{}, err
	}
	ready := false
	for _, c := range node.Status.Conditions {
		ready = ready || (c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue)
	}
	if !ready || node.UID == "" || !node.DeletionTimestamp.IsZero() {
		return time.Time{}, nil
	}
	var lease coordinationv1.Lease
	if err := reader.Get(ctx, client.ObjectKey{Namespace: "kube-node-lease", Name: node.Name}, &lease); err != nil {
		return time.Time{}, err
	}
	owned := false
	for _, owner := range lease.OwnerReferences {
		owned = owned || (owner.Kind == "Node" && owner.UID == node.UID && owner.Name == node.Name)
	}
	if !owned || lease.Spec.RenewTime == nil || !lease.DeletionTimestamp.IsZero() {
		return time.Time{}, nil
	}
	renewed := lease.Spec.RenewTime.Time
	deadline := renewed.Add(dnsNodeObservationLifetime)
	if renewed.After(now.Add(5*time.Second)) || !deadline.After(now) {
		return time.Time{}, nil
	}
	return deadline, nil
}
