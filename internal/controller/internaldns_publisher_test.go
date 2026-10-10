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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	internalDNSTestNamespace    = "dns-test"
	internalDNSTestInstanceName = "api"
	internalDNSTestInstanceUID  = "dns-instance-uid"
)

func TestInstanceDNSAttachmentsMapsTenancyAndAddressFamilies(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, networkingv1alpha.AddToScheme(scheme))
	network := &networkingv1alpha.Network{ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: internalDNSTestNamespace, UID: "vpc-uid"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network).Build()
	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, Namespace: internalDNSTestNamespace, UID: internalDNSTestInstanceUID},
		Spec:       computev1alpha.InstanceSpec{NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{{Name: defaultInterfaceName, Network: networkingv1alpha.NetworkRef{Name: "prod"}}}},
		Status: computev1alpha.InstanceStatus{
			// Application readiness is deliberately false. Instance identity is
			// governed by the interface allocation/programming lifecycle.
			Conditions: []metav1.Condition{{Type: computev1alpha.InstanceReady, Status: metav1.ConditionFalse}},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterfaceStatus{{
				Name: defaultInterfaceName,
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

	eligible := true
	observation := func(context.Context, client.Reader, string, *computev1alpha.Instance, computev1alpha.InstanceNetworkInterface) (InternalDNSObservedInterface, error) {
		return InternalDNSObservedInterface{Addresses: []networkingv1alpha.NetworkInterfaceAddress{{Address: "10.0.0.8/32"}, {Address: "2001:db8::8/128"}, {Address: "2001:db8:1::/96"}}, Eligible: eligible, ValidUntil: time.Now().Add(time.Minute)}, nil
	}
	attachments, err := instanceDNSAttachments(context.Background(), cl, internalDNSTestNamespace, instance, observation)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	assert.Equal(t, types.UID("vpc-uid"), attachments[0].VPCUID)
	assert.True(t, attachments[0].Eligible)
	assert.Len(t, attachments[0].RecordSets, 2, "A and AAAA host addresses should be retained")

	eligible = false
	attachments, err = instanceDNSAttachments(context.Background(), cl, internalDNSTestNamespace, instance, observation)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	assert.False(t, attachments[0].Eligible)
	assert.NotNil(t, attachments[0].RecordSets)
	assert.Empty(t, attachments[0].RecordSets)
}

func TestFindResolverContextRequiresProjectAndVPCUID(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsResolverContextGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsResolverContextGVK.GroupVersion().WithKind("DNSResolverContextList"), &unstructured.UnstructuredList{})
	wrong := resolverContextObject("wrong", "other-project", "vpc-a", true)
	right := resolverContextObject("right", "project-a", "vpc-a", true)
	wrongVPC := resolverContextObject("wrong-vpc", "project-a", "vpc-b", true)
	pending := resolverContextObject("pending", "project-a", "vpc-a", false)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(wrong, wrongVPC, pending, right).Build()

	got, err := findResolverContext(context.Background(), cl, internalDNSTestNamespace, "project-a", "vpc-a")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "right", got.GetName())

	got, err = findResolverContext(context.Background(), cl, internalDNSTestNamespace, "project-b", "vpc-a")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestFindResolverContextRejectsAmbiguityAndIgnoresTerminatingBinding(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsResolverContextGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsResolverContextGVK.GroupVersion().WithKind("DNSResolverContextList"), &unstructured.UnstructuredList{})
	first := resolverContextObject("first", "project-a", "vpc-a", true)
	second := resolverContextObject("second", "project-a", "vpc-a", true)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(first, second).Build()

	_, err := findResolverContext(context.Background(), cl, internalDNSTestNamespace, "project-a", "vpc-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple ready")

	now := metav1.Now()
	second.SetDeletionTimestamp(&now)
	second.SetFinalizers([]string{"test.example/finalizer"})
	cl = fake.NewClientBuilder().WithScheme(s).WithObjects(first, second).Build()
	got, err := findResolverContext(context.Background(), cl, internalDNSTestNamespace, "project-a", "vpc-a")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "first", got.GetName())
}

func resolverContextObject(name, projectUID, vpcUID string, accepted bool) *unstructured.Unstructured {
	status := string(metav1.ConditionFalse)
	if accepted {
		status = string(metav1.ConditionTrue)
	}
	obj := newDNSObject(dnsResolverContextGVK, internalDNSTestNamespace, name)
	obj.SetUID(types.UID(name + "-uid"))
	obj.SetGeneration(1)
	obj.Object["spec"] = map[string]any{"consumerID": projectUID + "/" + vpcUID}
	obj.Object[internalDNSStatusField] = map[string]any{
		"conditions":       []any{map[string]any{"type": "Accepted", internalDNSStatusField: status, "observedGeneration": int64(1)}, map[string]any{"type": "Ready", internalDNSStatusField: status, "observedGeneration": int64(1)}},
		"managedNamespace": map[string]any{"suffix": "datum.internal", "dnsZoneRef": map[string]any{internalDNSNameField: "zone", internalDNSUIDField: "zone-uid"}},
	}
	return obj
}

func TestRefreshObservationPreservesPlatformStatusAndIncrementsFence(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsContributionGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsContributionGVK.GroupVersion().WithKind("DNSRecordContributionList"), &unstructured.UnstructuredList{})
	contribution := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "endpoint")
	contribution.SetUID("contribution-uid")
	contribution.SetGeneration(3)
	contribution.SetResourceVersion("1")
	contribution.Object["spec"] = map[string]any{"recordSets": []any{}}
	contribution.Object[internalDNSStatusField] = map[string]any{
		"writerEpoch":       int64(7),
		"sequence":          int64(11),
		"eligible":          false,
		"publishedRevision": int64(44),
		"conditions":        []any{map[string]any{"type": "Published", internalDNSStatusField: string(metav1.ConditionTrue)}},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(contribution).WithStatusSubresource(contribution).Build()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	r := &InternalDNSPublisherReconciler{LeaseDuration: 60 * time.Second, Now: func() time.Time { return now }}

	var live unstructured.Unstructured
	live.SetGroupVersionKind(dnsContributionGVK)
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(contribution), &live))
	ready, err := r.refreshObservation(context.Background(), cl, &live, 7, true, now.Add(40*time.Second))
	require.NoError(t, err)
	assert.True(t, ready)

	var got unstructured.Unstructured
	got.SetGroupVersionKind(dnsContributionGVK)
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(contribution), &got))
	sequence, _, _ := unstructured.NestedInt64(got.Object, internalDNSStatusField, "sequence")
	published, _, _ := unstructured.NestedInt64(got.Object, internalDNSStatusField, "publishedRevision")
	conditions, _, _ := unstructured.NestedSlice(got.Object, internalDNSStatusField, "conditions")
	assert.Equal(t, int64(12), sequence)
	assert.Equal(t, int64(44), published)
	assert.Len(t, conditions, 1)
}

func TestEnsureDNSObjectRefusesForeignCollision(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsRegistrationGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsRegistrationGVK.GroupVersion().WithKind("DNSRegistrationList"), &unstructured.UnstructuredList{})
	foreign := newDNSObject(dnsRegistrationGVK, internalDNSTestNamespace, "instance-collision")
	foreign.SetUID("foreign")
	foreign.Object["spec"] = map[string]any{internalDNSNameField: "someone-else"}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(foreign).Build()
	owner := metav1.OwnerReference{APIVersion: computev1alpha.GroupVersion.String(), Kind: internalDNSInstanceKind, Name: internalDNSTestInstanceName, UID: internalDNSTestInstanceUID}

	_, _, err := ensureDNSObject(context.Background(), InternalDNSProjectAccess{Reader: cl, Writer: cl}, dnsRegistrationGVK, internalDNSTestNamespace, foreign.GetName(), map[string]string{
		internalDNSManagedBy: internalDNSManager, internalDNSInstanceUID: internalDNSTestInstanceUID,
	}, owner, map[string]any{internalDNSNameField: internalDNSTestInstanceName})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to adopt foreign")
}

func TestEnsureDNSObjectCreatesProjectInstanceOwnershipWithoutDeleteBlock(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsRegistrationGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsRegistrationGVK.GroupVersion().WithKind("DNSRegistrationList"), &unstructured.UnstructuredList{})
	cl := fake.NewClientBuilder().WithScheme(s).Build()
	owner := metav1.OwnerReference{APIVersion: computev1alpha.GroupVersion.String(), Kind: internalDNSInstanceKind, Name: internalDNSTestInstanceName, UID: "project-instance-uid", Controller: ptrBool(true)}

	created, changed, err := ensureDNSObject(context.Background(), InternalDNSProjectAccess{Reader: cl, Writer: cl}, dnsRegistrationGVK, internalDNSTestNamespace, "instance-owned", map[string]string{
		internalDNSManagedBy: internalDNSManager, internalDNSInstanceUID: "project-instance-uid",
	}, owner, map[string]any{internalDNSNameField: internalDNSTestInstanceName})
	require.NoError(t, err)
	assert.True(t, changed)
	require.Len(t, created.GetOwnerReferences(), 1)
	assert.Equal(t, types.UID("project-instance-uid"), created.GetOwnerReferences()[0].UID)
	assert.Nil(t, created.GetOwnerReferences()[0].BlockOwnerDeletion,
		"publisher must not require delete permission on the owning Instance")
}
