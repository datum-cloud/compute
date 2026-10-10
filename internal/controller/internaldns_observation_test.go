// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestObserveInternalDNSInterface(t *testing.T) {
	now := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		mutate     func(*computev1alpha.Instance, *networkingv1alpha.NetworkInterface, *corev1.Pod, *corev1.Node, *coordinationv1.Lease)
		eligible   bool
		wantErr    bool
		networkUID types.UID
	}{
		{name: "live network with unready application", eligible: true},
		{name: "replaced network lifetime", networkUID: "replacement-network", wantErr: true},
		{name: "different instance lifetime", mutate: func(i *computev1alpha.Instance, _ *networkingv1alpha.NetworkInterface, _ *corev1.Pod, _ *corev1.Node, _ *coordinationv1.Lease) {
			i.UID = "replacement"
		}, wantErr: true},
		{name: "stale programming generation", mutate: func(_ *computev1alpha.Instance, i *networkingv1alpha.NetworkInterface, _ *corev1.Pod, _ *corev1.Node, _ *coordinationv1.Lease) {
			i.Generation++
		}},
		{name: "different network", mutate: func(_ *computev1alpha.Instance, i *networkingv1alpha.NetworkInterface, _ *corev1.Pod, _ *corev1.Node, _ *coordinationv1.Lease) {
			i.Spec.Network.Name = internalDNSTestForeignValue
		}},
		{name: "different claim", mutate: func(_ *computev1alpha.Instance, i *networkingv1alpha.NetworkInterface, _ *corev1.Pod, _ *corev1.Node, _ *coordinationv1.Lease) {
			i.Spec.ClaimRef.Name = internalDNSTestForeignValue
		}, wantErr: true},
		{name: "runtime exited", mutate: func(_ *computev1alpha.Instance, _ *networkingv1alpha.NetworkInterface, p *corev1.Pod, _ *corev1.Node, _ *coordinationv1.Lease) {
			p.Status.Phase = corev1.PodFailed
		}},
		{name: "different runtime slot", mutate: func(_ *computev1alpha.Instance, _ *networkingv1alpha.NetworkInterface, p *corev1.Pod, _ *corev1.Node, _ *coordinationv1.Lease) {
			p.Labels[computev1alpha.InstanceIndexLabel] = "different-slot"
		}},
		{name: "node unavailable", mutate: func(_ *computev1alpha.Instance, _ *networkingv1alpha.NetworkInterface, _ *corev1.Pod, n *corev1.Node, _ *coordinationv1.Lease) {
			n.Status.Conditions[0].Status = corev1.ConditionFalse
		}},
		{name: "expired heartbeat", mutate: func(_ *computev1alpha.Instance, _ *networkingv1alpha.NetworkInterface, _ *corev1.Pod, _ *corev1.Node, l *coordinationv1.Lease) {
			l.Spec.RenewTime = &metav1.MicroTime{Time: now.Add(-time.Minute)}
		}},
		{name: "future heartbeat", mutate: func(_ *computev1alpha.Instance, _ *networkingv1alpha.NetworkInterface, _ *corev1.Pod, _ *corev1.Node, l *coordinationv1.Lease) {
			l.Spec.RenewTime = &metav1.MicroTime{Time: now.Add(time.Minute)}
		}},
		{name: "different node lifetime", mutate: func(_ *computev1alpha.Instance, _ *networkingv1alpha.NetworkInterface, _ *corev1.Pod, _ *corev1.Node, l *coordinationv1.Lease) {
			l.OwnerReferences[0].UID = "other-node"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, coordinationv1.AddToScheme, computev1alpha.AddToScheme, networkingv1alpha.AddToScheme} {
				require.NoError(t, add(scheme))
			}
			instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, Namespace: internalDNSTestObservationNamespace, UID: "project-instance", Labels: map[string]string{computev1alpha.InstanceSourceUIDLabel: internalDNSTestEdgeUID, computev1alpha.LocationLabel: "dfw"}}, Spec: computev1alpha.InstanceSpec{NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{{Network: networkingv1alpha.NetworkRef{Name: internalDNSTestNetworkName}}}}}
			edgeInstance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: instance.Name, Namespace: "ns-project-namespace", UID: internalDNSTestEdgeUID}, Status: computev1alpha.InstanceStatus{NetworkInterfaces: []computev1alpha.InstanceNetworkInterfaceStatus{{Name: defaultInterfaceName, NetworkInterfaceRef: &networkingv1alpha.LocalNetworkInterfaceRef{Name: "interface"}}}}}
			iface := &networkingv1alpha.NetworkInterface{ObjectMeta: metav1.ObjectMeta{Name: "interface", Namespace: edgeInstance.Namespace, UID: "interface-uid", Generation: 2}, Spec: networkingv1alpha.NetworkInterfaceSpec{Network: networkingv1alpha.LocalNetworkRef{Name: internalDNSTestNetworkName}, InterfaceName: defaultInterfaceName, ClaimRef: &networkingv1alpha.NetworkInterfaceClaimRef{Name: networkInterfaceClaimName(internalDNSTestInstanceName, defaultInterfaceName)}, Addresses: []networkingv1alpha.NetworkInterfaceAddress{{Address: "10.20.0.10/32"}}}, Status: networkingv1alpha.NetworkInterfaceStatus{Conditions: []metav1.Condition{{Type: "Allocated", Status: metav1.ConditionTrue, ObservedGeneration: 2}, {Type: "Programmed", Status: metav1.ConditionTrue, ObservedGeneration: 2}}}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runtime", Namespace: edgeInstance.Namespace, OwnerReferences: []metav1.OwnerReference{{APIVersion: computev1alpha.GroupVersion.String(), Kind: internalDNSInstanceKind, Name: edgeInstance.Name, UID: edgeInstance.UID, Controller: ptrBool(true)}}}, Spec: corev1.PodSpec{NodeName: "worker"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}}}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "node-uid"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
			lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: node.Name, Namespace: "kube-node-lease", OwnerReferences: []metav1.OwnerReference{{Kind: "Node", Name: node.Name, UID: node.UID}}}, Spec: coordinationv1.LeaseSpec{RenewTime: &metav1.MicroTime{Time: now.Add(-10 * time.Second)}}}
			edgeInstance.Labels = map[string]string{computev1alpha.WorkloadDeploymentUIDLabel: "dns-deployment", computev1alpha.InstanceIndexLabel: "0"}
			pod.Labels = map[string]string{computev1alpha.WorkloadDeploymentUIDLabel: "dns-deployment", computev1alpha.InstanceIndexLabel: "0"}
			if tc.mutate != nil {
				tc.mutate(edgeInstance, iface, pod, node, lease)
			}
			iface.Status.NetworkContextRef = &networkingv1alpha.LocalNetworkContextRef{Name: "context"}
			networkUID := types.UID("network-uid")
			if tc.networkUID != "" {
				networkUID = tc.networkUID
			}
			network := &networkingv1alpha.Network{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestNetworkName, Namespace: internalDNSTestObservationNamespace, UID: networkUID}}
			project := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestObservationNamespace, UID: "project-namespace"}}).Build()
			claim := &networkingv1alpha.NetworkInterfaceClaim{ObjectMeta: metav1.ObjectMeta{Name: networkInterfaceClaimName(internalDNSTestInstanceName, defaultInterfaceName), Namespace: edgeInstance.Namespace, UID: "claim-uid"}, Spec: networkingv1alpha.NetworkInterfaceClaimSpec{Network: networkingv1alpha.LocalNetworkRef{Name: internalDNSTestNetworkName}}, Status: networkingv1alpha.NetworkInterfaceClaimStatus{NetworkInterfaceRef: &networkingv1alpha.LocalNetworkInterfaceRef{Name: iface.Name}}}
			networkContext := &networkingv1alpha.NetworkContext{ObjectMeta: metav1.ObjectMeta{Name: "context", Namespace: edgeInstance.Namespace, UID: "network-context", Labels: map[string]string{networkingv1alpha.NetworkUIDLabel: "network-uid"}}, Spec: networkingv1alpha.NetworkContextSpec{Network: networkingv1alpha.LocalNetworkRef{Name: internalDNSTestNetworkName}, Location: networkingv1alpha.LocationReference{Name: "dfw"}}}
			source := fake.NewClientBuilder().WithScheme(scheme).WithObjects(edgeInstance, iface, claim, networkContext, pod, node, lease, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "edge-cluster"}}).Build()
			got, err := ObserveInternalDNSInterface(context.Background(), project, internalDNSTestObservationNamespace, instance, instance.Spec.NetworkInterfaces[0], InternalDNSObservationSource{Reader: source, ClusterUID: "edge-cluster"}, now)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.eligible, got.Eligible)
			if tc.eligible {
				require.WithinDuration(t, now.Add(35*time.Second), got.ValidUntil, time.Nanosecond)
				require.Equal(t, iface.Spec.Addresses, got.Addresses)
			}
		})
	}
}

func TestObserveInternalDNSInterfaceRejectsMissingSource(t *testing.T) {
	_, err := ObserveInternalDNSInterface(context.Background(), nil, internalDNSTestObservationNamespace, &computev1alpha.Instance{}, computev1alpha.InstanceNetworkInterface{}, InternalDNSObservationSource{}, time.Now())
	require.Error(t, err)
}
