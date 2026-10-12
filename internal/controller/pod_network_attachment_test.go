// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

func TestPodNetworkAttachmentReconcile(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := networkingv1alpha.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "tenant", Name: "dns-abc", UID: types.UID("79f1fbc8-2e37-4140-8185-b6c2f9e320dd"),
		Labels:      map[string]string{PodPrimaryNetworkEnabledLabel: "true"},
		Annotations: map[string]string{PodPrimaryNetworkAnnotation: "galactic"},
	}, Spec: corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: PodNetworkSchedulingGate}}}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	r := &PodNetworkAttachmentReconciler{Client: cl}
	key := client.ObjectKeyFromObject(pod)
	reconcile := func() {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
	}
	reconcile()
	claim := &networkingv1alpha.NetworkInterfaceClaim{}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: podNetworkClaimName(pod.UID)}, claim); err != nil {
		t.Fatal(err)
	}
	if claim.Spec.Network.Name != "galactic" || claim.Spec.InterfaceName != "eth0" || !metav1.IsControlledBy(claim, pod) {
		t.Fatalf("bad Pod claim: %+v", claim)
	}

	claim.Status.NetworkInterfaceRef = &networkingv1alpha.LocalNetworkInterfaceRef{Name: "pod-nic"}
	for _, conditionType := range []string{networkingv1alpha.NetworkInterfaceClaimBound, networkingv1alpha.NetworkInterfaceClaimAllocated, networkingv1alpha.NetworkInterfaceClaimPrepared} {
		claim.Status.Conditions = append(claim.Status.Conditions, metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()})
	}
	if err := cl.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	// NSO can publish the interface reference before the interface is visible
	// in Compute's cache. The Pod must keep polling because it has no interface
	// watch, and must remain gated until the provider artifact is available.
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("missing interface did not requeue")
	}
	if err := cl.Get(ctx, key, pod); err != nil {
		t.Fatal(err)
	}
	if !hasPodNetworkGate(pod) || pod.Annotations[MultusDefaultNetworkAnnotation] != "" {
		t.Fatal("Pod changed before its network interface was visible")
	}
	interfaceObject := &networkingv1alpha.NetworkInterface{ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: "pod-nic"}}
	interfaceObject.Status.AttachmentRef = &networkingv1alpha.NetworkInterfaceAttachmentRef{APIGroup: "cloud.datumapis.com", Kind: "VPCAttachment", Name: "pod-nic"}
	if err := cl.Create(ctx, interfaceObject); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if err := cl.Get(ctx, key, pod); err != nil {
		t.Fatal(err)
	}
	if got := pod.Annotations[MultusDefaultNetworkAnnotation]; got != "tenant/pod-nic" {
		t.Fatalf("default network = %q", got)
	}
	if !hasPodNetworkGate(pod) {
		t.Fatal("gate removed in same write as annotation")
	}
	reconcile()
	if err := cl.Get(ctx, key, pod); err != nil {
		t.Fatal(err)
	}
	if hasPodNetworkGate(pod) {
		t.Fatal("gate was not removed")
	}
}
