// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func TestDNSReservationExistsWithoutInstancesAndSurvivesEndpointCleanup(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, gvk := range []schema.GroupVersionKind{dnsResolverContextGVK, dnsRegistrationGVK, dnsContributionGVK} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	resolver := resolverContextObject("context", internalDNSTestProjectUID, internalDNSTestVPCUID, true)
	require.NoError(t, unstructured.SetNestedField(resolver.Object, true, internalDNSSpecField, "managedNamespace", "enabled"))
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(resolver).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		obj.SetUID(types.UID(obj.GetName() + "-uid"))
		obj.SetGeneration(1)
		return c.Create(ctx, obj, opts...)
	}}).Build()
	publisher := &InternalDNSPublisherReconciler{mgr: internalDNSTestClusters{internalDNSTestProjectUID: internalDNSTestCluster{client: cl}}}
	reconciler := internalDNSReservationReconciler{publisher: publisher}
	req := mcreconcile.Request{ClusterName: internalDNSTestProjectUID, Request: ctrl.Request{NamespacedName: client.ObjectKeyFromObject(resolver)}}
	for range 2 {
		_, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
	}
	root := newDNSObject(dnsRegistrationGVK, resolver.GetNamespace(), instanceDNSReservationName(resolver))
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(root), root))
	scopes, _, err := unstructured.NestedStringSlice(root.Object, internalDNSSpecField, "reservedDescendants")
	require.NoError(t, err)
	require.Equal(t, []string{instanceDNSScope}, scopes)
	require.Equal(t, resolver.GetUID(), root.GetOwnerReferences()[0].UID)
	require.Empty(t, root.GetLabels()[internalDNSInstanceUID])
	access := InternalDNSProjectAccess{Reader: cl, Writer: cl}
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, UID: internalDNSTestInstanceUID}}
	owner := metav1.OwnerReference{APIVersion: computev1alpha.GroupVersion.String(), Kind: internalDNSInstanceKind, Name: instance.Name, UID: instance.UID, Controller: ptrBool(true)}
	attachment := instanceDNSAttachment{VPCUID: internalDNSTestVPCUID, Eligible: true, RecordSets: []any{map[string]any{internalDNSRecordTypeField: "A", internalDNSRecordsField: []any{map[string]any{internalDNSNameField: "", "a": map[string]any{internalDNSContentField: internalDNSTestAddress}}}}}}
	for range 3 {
		_, err := publisher.reconcileAttachment(ctx, access, InternalDNSProjectIdentity{ProjectUID: internalDNSTestProjectUID}, resolver.GetNamespace(), instance, owner, attachment, "endpoint")
		require.NoError(t, err)
	}
	contribution := newDNSObject(dnsContributionGVK, resolver.GetNamespace(), "endpoint")
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(contribution), contribution))
	ref, _, _ := unstructured.NestedString(contribution.Object, internalDNSSpecField, internalDNSRegistrationRefField, internalDNSUIDField)
	require.Equal(t, string(root.GetUID()), ref)
	// Old per-instance registrations are removed even when their contribution is retained.
	legacy := newDNSObject(dnsRegistrationGVK, resolver.GetNamespace(), "endpoint")
	legacy.SetLabels(map[string]string{internalDNSManagedBy: internalDNSManager, internalDNSInstanceUID: string(instance.UID)})
	require.NoError(t, cl.Create(ctx, legacy))
	require.NoError(t, publisher.deletePublishedResources(ctx, access, resolver.GetNamespace(), instance.UID, map[string]struct{}{"endpoint": {}}))
	require.True(t, apierrors.IsNotFound(cl.Get(ctx, client.ObjectKeyFromObject(legacy), legacy)))
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(contribution), contribution))
	require.NoError(t, publisher.deletePublishedResources(ctx, access, resolver.GetNamespace(), instance.UID, nil))
	require.True(t, apierrors.IsNotFound(cl.Get(ctx, client.ObjectKeyFromObject(contribution), contribution)))
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(root), root))
	// Recreated contexts use independent policy identities, never an old root.
	replacement := resolver.DeepCopy()
	replacement.SetUID("replacement-context")
	require.NotEqual(t, instanceDNSReservationName(resolver), instanceDNSReservationName(replacement))
}
