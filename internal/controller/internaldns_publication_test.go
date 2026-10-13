// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestObservationPersistsUntilExplicitWithdrawal(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsContributionGVK, &unstructured.Unstructured{})
	c := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, internalDNSTestInstanceName)
	c.SetUID("contribution")
	c.SetGeneration(1)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(c).WithStatusSubresource(c).Build()
	r := &InternalDNSPublisherReconciler{}
	for _, eligible := range []bool{true, true, false} {
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(c), c))
		_, err := r.refreshObservation(context.Background(), cl, c, eligible)
		require.NoError(t, err)
		got, _, _ := unstructured.NestedBool(c.Object, internalDNSStatusField, internalDNSEligibleField)
		require.Equal(t, eligible, got)
		_, found, _ := unstructured.NestedString(c.Object, internalDNSStatusField, "validUntil")
		require.False(t, found)
	}
}

func TestUnavailableObservationLeavesPublishedStateUntouched(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsContributionGVK, &unstructured.Unstructured{})
	published := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "record")
	published.SetUID("contribution")
	published.Object[internalDNSSpecField] = map[string]any{"recordSets": []any{map[string]any{internalDNSRecordTypeField: "A", internalDNSRecordsField: []any{map[string]any{internalDNSNameField: "api", "a": map[string]any{internalDNSContentField: "10.0.0.8"}}}}}}
	published.Object[internalDNSStatusField] = map[string]any{internalDNSSequenceField: int64(9), internalDNSEligibleField: true}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(published).WithStatusSubresource(published).Build()
	r := &InternalDNSPublisherReconciler{}
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: "api", UID: internalDNSTestInstanceUID}}
	_, err := r.reconcileAttachment(context.Background(), InternalDNSProjectAccess{Reader: cl, Writer: cl}, InternalDNSProjectIdentity{}, internalDNSTestNamespace, instance, metav1.OwnerReference{}, instanceDNSAttachment{ObservationError: context.DeadlineExceeded}, "record")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	current := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "record")
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(published), current))
	require.Equal(t, published.Object, current.Object)
}

func TestAllocatedInstanceDNSName(t *testing.T) {
	first := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: "Web_01", UID: "first"}}
	second := first.DeepCopy()
	second.UID = "second"
	require.Equal(t, "web-01", allocatedInstanceDNSName(first))
	require.Equal(t, allocatedInstanceDNSName(first), allocatedInstanceDNSName(second))
	require.Equal(t, allocatedInstanceDNSName(first), allocatedInstanceDNSName(first.DeepCopy()))
}

func TestPublisherCreatesRegistrationAndContribution(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, gvk := range []schema.GroupVersionKind{dnsResolverContextGVK, dnsRegistrationGVK, dnsContributionGVK} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	resolver := resolverContextObject("context", "project-a", internalDNSTestVPCUID, true)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(resolver).WithStatusSubresource(newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "record")).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			obj.SetUID(types.UID(obj.GetName() + "-uid"))
			obj.SetGeneration(1)
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
	reconciler := &InternalDNSPublisherReconciler{}
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, Namespace: internalDNSTestNamespace, UID: internalDNSTestInstanceUID}}
	owner := metav1.OwnerReference{APIVersion: computev1alpha.GroupVersion.String(), Kind: internalDNSInstanceKind, Name: instance.Name, UID: instance.UID, Controller: ptrBool(true)}
	access := InternalDNSProjectAccess{Reader: cl, Writer: cl}
	attachment := instanceDNSAttachment{VPCUID: internalDNSTestVPCUID, Eligible: true, RecordSets: []any{map[string]any{internalDNSRecordTypeField: "A", internalDNSRecordsField: []any{map[string]any{internalDNSNameField: "", "a": map[string]any{internalDNSContentField: "10.0.0.8"}}}}}}
	ready := false
	for range 3 {
		var err error
		ready, err = reconciler.reconcileAttachment(context.Background(), access, InternalDNSProjectIdentity{ProjectUID: "project-a"}, internalDNSTestNamespace, instance, owner, attachment, "record")
		require.NoError(t, err)
	}
	require.True(t, ready)
	contribution := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "record")
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(contribution), contribution))
	eligible, _, _ := unstructured.NestedBool(contribution.Object, internalDNSStatusField, internalDNSEligibleField)
	sequence, _, _ := unstructured.NestedInt64(contribution.Object, internalDNSStatusField, internalDNSSequenceField)
	require.True(t, eligible)
	require.Equal(t, int64(1), sequence)
	_, found, _ := unstructured.NestedFieldNoCopy(contribution.Object, internalDNSSpecField, "grantRef")
	require.False(t, found)
}
