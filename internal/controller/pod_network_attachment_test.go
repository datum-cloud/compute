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

const (
	testPodNICName         = "pod-nic"
	testPodNamespace       = "tenant"
	testRetainedClaimName  = "resolver-node"
	testSecondaryInterface = "net1"
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
		Labels:      map[string]string{PodPrimaryNetworkEnabledLabel: PodPrimaryNetworkEnabledValue},
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
	if err := cl.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: "pod-" + string(pod.UID) + "-eth0"}, claim); err != nil {
		t.Fatal(err)
	}
	if claim.Spec.Network.Name != "galactic" || claim.Spec.InterfaceName != defaultInterfaceName || !metav1.IsControlledBy(claim, pod) {
		t.Fatalf("bad Pod claim: %+v", claim)
	}

	claim.Status.NetworkInterfaceRef = &networkingv1alpha.LocalNetworkInterfaceRef{Name: testPodNICName}
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
	interfaceObject := &networkingv1alpha.NetworkInterface{ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: testPodNICName}}
	interfaceObject.Status.AttachmentRef = &networkingv1alpha.NetworkInterfaceAttachmentRef{APIGroup: "cloud.datumapis.com", Kind: "VPCAttachment", Name: testPodNICName}
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

func newPodNetworkTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := networkingv1alpha.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func listedInterfacesPod(annotation string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: testPodNamespace, Name: "resolver-abc", UID: types.UID("0b0f6a52-7c5e-4d5e-9a0e-1f6f4f6c2b11"),
		Labels: map[string]string{
			PodNetworkInterfacesEnabledLabel: PodPrimaryNetworkEnabledValue,
			"compute.datumapis.com/tier":     "node",
			"app":                            "resolver",
		},
		Annotations: map[string]string{PodNetworkInterfacesAnnotation: annotation},
	}, Spec: corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: PodNetworkSchedulingGate}}}}
}

func markPodClaimPrepared(t *testing.T, ctx context.Context, cl client.Client, claim *networkingv1alpha.NetworkInterfaceClaim, interfaceName, address string) {
	t.Helper()
	claim.Status.NetworkInterfaceRef = &networkingv1alpha.LocalNetworkInterfaceRef{Name: interfaceName}
	claim.Status.Addresses = []networkingv1alpha.NetworkInterfaceAddress{{Family: networkingv1alpha.IPv6Protocol, Address: address}}
	for _, conditionType := range []string{networkingv1alpha.NetworkInterfaceClaimBound, networkingv1alpha.NetworkInterfaceClaimAllocated, networkingv1alpha.NetworkInterfaceClaimPrepared} {
		claim.Status.Conditions = append(claim.Status.Conditions, metav1.Condition{Type: conditionType, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()})
	}
	if err := cl.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	networkInterface := &networkingv1alpha.NetworkInterface{ObjectMeta: metav1.ObjectMeta{Namespace: claim.Namespace, Name: interfaceName}}
	networkInterface.Status.AttachmentRef = &networkingv1alpha.NetworkInterfaceAttachmentRef{APIGroup: "galactic.datumapis.com", Kind: "VPCAttachment", Name: interfaceName}
	if err := cl.Create(ctx, networkInterface); err != nil {
		t.Fatal(err)
	}
}

func TestPodNetworkAttachmentSecondaryRetainedInterface(t *testing.T) {
	ctx := context.Background()
	pod := listedInterfacesPod(`[{"network":"producer","interface":"net1","claimName":"resolver-node","reclaimPolicy":"Retain"}]`)
	cl := newPodNetworkTestClient(t, pod)
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
	if err := cl.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: testRetainedClaimName}, claim); err != nil {
		t.Fatal(err)
	}
	if claim.Spec.InterfaceName != testSecondaryInterface || claim.Spec.ReclaimPolicy != networkingv1alpha.NetworkInterfaceReclaimPolicyRetain || !metav1.IsControlledBy(claim, pod) {
		t.Fatalf("bad retained claim: %+v", claim)
	}
	if claim.Labels["compute.datumapis.com/tier"] != "node" || claim.Labels["app"] != "" || claim.Labels[PodNetworkInterfacesEnabledLabel] != "" {
		t.Fatalf("claim labels = %v", claim.Labels)
	}

	markPodClaimPrepared(t, ctx, cl, claim, testRetainedClaimName, "fd20:0:e::4:0:0/96")
	reconcile()
	if err := cl.Get(ctx, key, pod); err != nil {
		t.Fatal(err)
	}
	if got := pod.Annotations[MultusNetworksAnnotation]; got != "tenant/resolver-node@net1" {
		t.Fatalf("networks = %q", got)
	}
	if got := pod.Annotations[PodInterfaceAddressAnnotation(testSecondaryInterface)]; got != "fd20:0:e::4:0:0" {
		t.Fatalf("address = %q", got)
	}
	if pod.Annotations[MultusDefaultNetworkAnnotation] != "" {
		t.Fatal("secondary interface replaced the primary network")
	}
	if !hasPodNetworkGate(pod) {
		t.Fatal("gate removed in same write as annotations")
	}
	reconcile()
	if err := cl.Get(ctx, key, pod); err != nil {
		t.Fatal(err)
	}
	if hasPodNetworkGate(pod) {
		t.Fatal("gate was not removed")
	}
}

func TestPodNetworkAttachmentExistingClaims(t *testing.T) {
	ctx := context.Background()
	const annotation = `[{"network":"producer","interface":"net1","claimName":"resolver-node","reclaimPolicy":"Retain"}]`
	retained := func() *networkingv1alpha.NetworkInterfaceClaim {
		return &networkingv1alpha.NetworkInterfaceClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: testPodNamespace, Name: testRetainedClaimName},
			Spec: networkingv1alpha.NetworkInterfaceClaimSpec{
				Network:       networkingv1alpha.LocalNetworkRef{Name: "producer"},
				InterfaceName: testSecondaryInterface,
				ReclaimPolicy: networkingv1alpha.NetworkInterfaceReclaimPolicyRetain,
			},
		}
	}
	controlledBy := func(name string) []metav1.OwnerReference {
		controller := true
		return []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: name, UID: types.UID(name), Controller: &controller}}
	}

	t.Run("unowned claim is adopted", func(t *testing.T) {
		pod := listedInterfacesPod(annotation)
		cl := newPodNetworkTestClient(t, pod, retained())
		r := &PodNetworkAttachmentReconciler{Client: cl}
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}); err != nil {
			t.Fatal(err)
		}
		claim := &networkingv1alpha.NetworkInterfaceClaim{}
		if err := cl.Get(ctx, client.ObjectKey{Namespace: testPodNamespace, Name: testRetainedClaimName}, claim); err != nil {
			t.Fatal(err)
		}
		if !metav1.IsControlledBy(claim, pod) {
			t.Fatalf("claim was not adopted: %+v", claim.OwnerReferences)
		}
	})

	t.Run("claim held by another Pod waits", func(t *testing.T) {
		pod := listedInterfacesPod(annotation)
		claim := retained()
		claim.OwnerReferences = controlledBy("resolver-old")
		cl := newPodNetworkTestClient(t, pod, claim)
		r := &PodNetworkAttachmentReconciler{Client: cl}
		result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)})
		if err != nil {
			t.Fatal(err)
		}
		if result.RequeueAfter == 0 {
			t.Fatal("did not wait for the previous holder")
		}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(claim), claim); err != nil {
			t.Fatal(err)
		}
		if metav1.IsControlledBy(claim, pod) {
			t.Fatal("claim taken from another Pod")
		}
	})

	t.Run("terminating claim waits", func(t *testing.T) {
		pod := listedInterfacesPod(annotation)
		claim := retained()
		claim.Finalizers = []string{"networking.datumapis.com/test"}
		now := metav1.Now()
		claim.DeletionTimestamp = &now
		cl := newPodNetworkTestClient(t, pod, claim)
		r := &PodNetworkAttachmentReconciler{Client: cl}
		result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)})
		if err != nil {
			t.Fatal(err)
		}
		if result.RequeueAfter == 0 {
			t.Fatal("did not wait for the terminating claim")
		}
	})

	t.Run("claim with a different policy is refused", func(t *testing.T) {
		pod := listedInterfacesPod(annotation)
		claim := retained()
		claim.Spec.ReclaimPolicy = networkingv1alpha.NetworkInterfaceReclaimPolicyDelete
		cl := newPodNetworkTestClient(t, pod, claim)
		r := &PodNetworkAttachmentReconciler{Client: cl}
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}); err == nil {
			t.Fatal("mismatched claim was accepted")
		}
	})
}

func TestPodNetworkInterfaces(t *testing.T) {
	controller := true
	statefulSetOwner := []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "resolvers", UID: "set", Controller: &controller}}
	for _, tc := range []struct {
		name       string
		annotation string
		owners     []metav1.OwnerReference
		wantErr    bool
		wantClaim  string
	}{
		{name: "defaults", annotation: `[{"network":"producer"}]`, wantClaim: "pod-uid-eth0"},
		{name: "named claim", annotation: `[{"network":"producer","interface":"net1","claimName":"slot","reclaimPolicy":"Retain"}]`, wantClaim: "slot"},
		{name: "stateful set retain", annotation: `[{"network":"producer","interface":"net1","reclaimPolicy":"Retain"}]`, owners: statefulSetOwner, wantClaim: "resolver-0-net1"},
		{name: "retain without stable name", annotation: `[{"network":"producer","reclaimPolicy":"Retain"}]`, wantErr: true},
		{name: "missing network", annotation: `[{"interface":"net1"}]`, wantErr: true},
		{name: "duplicate interface", annotation: `[{"network":"a","interface":"net1"},{"network":"b","interface":"net1"}]`, wantErr: true},
		{name: "duplicate claim", annotation: `[{"network":"a","interface":"net1","claimName":"slot"},{"network":"b","interface":"net2","claimName":"slot"}]`, wantErr: true},
		{name: "unknown policy", annotation: `[{"network":"a","reclaimPolicy":"Keep"}]`, wantErr: true},
		{name: "empty list", annotation: `[]`, wantErr: true},
		{name: "not json", annotation: `producer`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: "resolver-0", UID: "uid", OwnerReferences: tc.owners,
				Labels:      map[string]string{PodNetworkInterfacesEnabledLabel: PodPrimaryNetworkEnabledValue},
				Annotations: map[string]string{PodNetworkInterfacesAnnotation: tc.annotation},
			}}
			interfaces, err := PodNetworkInterfaces(pod)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tc.wantErr {
				return
			}
			if got := podNetworkInterfaceClaimName(pod, interfaces[0]); got != tc.wantClaim {
				t.Fatalf("claim name = %q", got)
			}
		})
	}

	both := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
		PodPrimaryNetworkEnabledLabel:    PodPrimaryNetworkEnabledValue,
		PodNetworkInterfacesEnabledLabel: PodPrimaryNetworkEnabledValue,
	}}}
	if _, err := PodNetworkInterfaces(both); err == nil {
		t.Fatal("both opt-in labels were accepted")
	}
}
