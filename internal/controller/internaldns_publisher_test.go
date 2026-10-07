// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

func internalDNSTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, computev1alpha.AddToScheme(s))
	require.NoError(t, networkingv1alpha.AddToScheme(s))
	for _, gvk := range []struct {
		kind schemaKind
	}{
		{schemaKind{dnsManagedNamespaceGVK}},
		{schemaKind{dnsRegistrationGVK}},
		{schemaKind{dnsGrantGVK}},
		{schemaKind{dnsContributionGVK}},
	} {
		s.AddKnownTypeWithName(gvk.kind.gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.kind.gvk.GroupVersion().WithKind(gvk.kind.gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	return s
}

type schemaKind struct{ gvk schema.GroupVersionKind }

func TestInstanceDNSAttachmentsMapsTenancyAndAddressFamilies(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, networkingv1alpha.AddToScheme(scheme))
	network := &networkingv1alpha.Network{ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default", UID: "vpc-uid"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network).Build()
	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", UID: "instance-uid"},
		Spec:       computev1alpha.InstanceSpec{NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{{Name: "eth0", Network: networkingv1alpha.NetworkRef{Name: "prod"}}}},
		Status: computev1alpha.InstanceStatus{
			// Application readiness is deliberately false. Instance identity is
			// governed by the interface allocation/programming lifecycle.
			Conditions: []metav1.Condition{{Type: computev1alpha.InstanceReady, Status: metav1.ConditionFalse}},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterfaceStatus{{
				Name: "eth0",
				Addresses: []computev1alpha.InstanceNetworkInterfaceAddress{
					{Address: "10.0.0.8/32"},
					{Address: "2001:db8::8/128"},
					{Address: "2001:db8:1::/96"}, // delegated block is not one host identity
				},
				Conditions: []metav1.Condition{
					{Type: computev1alpha.InstanceNetworkInterfaceAllocated, Status: metav1.ConditionTrue},
					{Type: computev1alpha.InstanceNetworkInterfaceProgrammed, Status: metav1.ConditionTrue},
				},
			}},
		},
	}

	attachments, err := instanceDNSAttachments(context.Background(), cl, "default", instance)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	assert.Equal(t, types.UID("vpc-uid"), attachments[0].VPCUID)
	assert.True(t, attachments[0].Eligible)
	assert.Len(t, attachments[0].RecordSets, 2, "A and AAAA host addresses should be retained")

	instance.Status.NetworkInterfaces[0].Conditions[1].Status = metav1.ConditionFalse
	attachments, err = instanceDNSAttachments(context.Background(), cl, "default", instance)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	assert.False(t, attachments[0].Eligible)
	assert.NotNil(t, attachments[0].RecordSets)
	assert.Empty(t, attachments[0].RecordSets)
}

func TestFindManagedNamespaceRequiresProjectAndVPCUID(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsManagedNamespaceGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsManagedNamespaceGVK.GroupVersion().WithKind("DNSManagedNamespaceList"), &unstructured.UnstructuredList{})
	wrong := managedNamespaceObject("wrong", "other-project", "vpc-a", true)
	right := managedNamespaceObject("right", "project-a", "vpc-a", true)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(wrong, right).Build()

	got, err := findManagedNamespace(context.Background(), cl, "default", "project-a", "vpc-a")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "right", got.GetName())

	got, err = findManagedNamespace(context.Background(), cl, "default", "project-b", "vpc-a")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestFindManagedNamespaceRejectsAmbiguityAndIgnoresTerminatingBinding(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsManagedNamespaceGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsManagedNamespaceGVK.GroupVersion().WithKind("DNSManagedNamespaceList"), &unstructured.UnstructuredList{})
	first := managedNamespaceObject("first", "project-a", "vpc-a", true)
	second := managedNamespaceObject("second", "project-a", "vpc-a", true)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(first, second).Build()

	_, err := findManagedNamespace(context.Background(), cl, "default", "project-a", "vpc-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple accepted")

	now := metav1.Now()
	second.SetDeletionTimestamp(&now)
	second.SetFinalizers([]string{"test.example/finalizer"})
	cl = fake.NewClientBuilder().WithScheme(s).WithObjects(first, second).Build()
	got, err := findManagedNamespace(context.Background(), cl, "default", "project-a", "vpc-a")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "first", got.GetName())
}

func managedNamespaceObject(name, projectUID, vpcUID string, accepted bool) *unstructured.Unstructured {
	status := "False"
	if accepted {
		status = "True"
	}
	obj := newDNSObject(dnsManagedNamespaceGVK, "default", name)
	obj.Object["spec"] = map[string]any{"projectUID": projectUID, "vpcRef": map[string]any{"uid": vpcUID}}
	obj.Object["status"] = map[string]any{
		"conditions": []any{map[string]any{"type": "Accepted", "status": status}},
		"dnsZoneRef": map[string]any{"name": "zone", "uid": "zone-uid", "generation": int64(1)},
	}
	return obj
}

func TestRefreshObservationPreservesPlatformStatusAndIncrementsFence(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsContributionGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsContributionGVK.GroupVersion().WithKind("DNSRecordContributionList"), &unstructured.UnstructuredList{})
	contribution := newDNSObject(dnsContributionGVK, "default", "endpoint")
	contribution.SetUID("contribution-uid")
	contribution.SetGeneration(3)
	contribution.SetResourceVersion("1")
	contribution.Object["spec"] = map[string]any{"recordSets": []any{}}
	contribution.Object["status"] = map[string]any{
		"writerEpoch":       int64(7),
		"sequence":          int64(11),
		"eligible":          false,
		"publishedRevision": int64(44),
		"conditions":        []any{map[string]any{"type": "Published", "status": "True"}},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(contribution).WithStatusSubresource(contribution).Build()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	r := &InternalDNSPublisherReconciler{LeaseDuration: 60 * time.Second, Now: func() time.Time { return now }}

	var live unstructured.Unstructured
	live.SetGroupVersionKind(dnsContributionGVK)
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(contribution), &live))
	ready, err := r.refreshObservation(context.Background(), cl, &live, 7, true)
	require.NoError(t, err)
	assert.True(t, ready)

	var got unstructured.Unstructured
	got.SetGroupVersionKind(dnsContributionGVK)
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(contribution), &got))
	sequence, _, _ := unstructured.NestedInt64(got.Object, "status", "sequence")
	published, _, _ := unstructured.NestedInt64(got.Object, "status", "publishedRevision")
	conditions, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	assert.Equal(t, int64(12), sequence)
	assert.Equal(t, int64(44), published)
	assert.Len(t, conditions, 1)
}

func TestEnsureDNSObjectRefusesForeignCollision(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsRegistrationGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsRegistrationGVK.GroupVersion().WithKind("DNSRegistrationList"), &unstructured.UnstructuredList{})
	foreign := newDNSObject(dnsRegistrationGVK, "default", "instance-collision")
	foreign.SetUID("foreign")
	foreign.Object["spec"] = map[string]any{"name": "someone-else"}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(foreign).Build()
	owner := metav1.OwnerReference{APIVersion: computev1alpha.GroupVersion.String(), Kind: "Instance", Name: "api", UID: "instance-uid"}

	_, _, err := ensureDNSObject(context.Background(), InternalDNSProjectAccess{Reader: cl, Writer: cl}, dnsRegistrationGVK, "default", foreign.GetName(), map[string]string{
		internalDNSManagedBy: internalDNSManager, internalDNSInstanceUID: "instance-uid",
	}, owner, map[string]any{"name": "api"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to adopt foreign")
}

func TestEnsureDNSObjectCreatesProjectInstanceOwnershipWithoutDeleteBlock(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsRegistrationGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsRegistrationGVK.GroupVersion().WithKind("DNSRegistrationList"), &unstructured.UnstructuredList{})
	cl := fake.NewClientBuilder().WithScheme(s).Build()
	owner := metav1.OwnerReference{APIVersion: computev1alpha.GroupVersion.String(), Kind: "Instance", Name: "api", UID: "project-instance-uid", Controller: ptrBool(true)}

	created, changed, err := ensureDNSObject(context.Background(), InternalDNSProjectAccess{Reader: cl, Writer: cl}, dnsRegistrationGVK, "default", "instance-owned", map[string]string{
		internalDNSManagedBy: internalDNSManager, internalDNSInstanceUID: "project-instance-uid",
	}, owner, map[string]any{"name": "api"})
	require.NoError(t, err)
	assert.True(t, changed)
	require.Len(t, created.GetOwnerReferences(), 1)
	assert.Equal(t, types.UID("project-instance-uid"), created.GetOwnerReferences()[0].UID)
	assert.Nil(t, created.GetOwnerReferences()[0].BlockOwnerDeletion,
		"publisher must not require delete permission on the owning Instance")
}
