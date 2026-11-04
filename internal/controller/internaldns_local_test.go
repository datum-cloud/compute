// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

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
)

const (
	localTestRegion  = "us-east-1"
	localTestNetwork = "app"
)

func localDNSScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, computev1alpha.AddToScheme(s))
	for _, gvk := range []schema.GroupVersionKind{dnsResolverContextGVK, dnsRegistrationGVK, dnsContributionGVK} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	return s
}

// copyOf marks obj as a DNS copy whose origin UID is origin and whose origin
// generation is 7. The region's DNS control plane reports conditions against
// the origin generation, as it reads copies with it.
func copyOf(obj *unstructured.Unstructured, origin string) *unstructured.Unstructured {
	if conditions, ok, _ := unstructured.NestedSlice(obj.Object, internalDNSStatusField, internalDNSConditionsField); ok {
		for _, c := range conditions {
			c.(map[string]any)[internalDNSObservedGenerationField] = int64(7)
		}
		_ = unstructured.SetNestedSlice(obj.Object, conditions, internalDNSStatusField, internalDNSConditionsField)
	}
	obj.SetUID(types.UID("local-" + origin))
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[internalDNSOriginLabel] = "project"
	obj.SetLabels(labels)
	obj.SetAnnotations(map[string]string{internalDNSOriginUIDAnnotation: origin, internalDNSOriginGenerationAnnotation: "7"})
	return obj
}

func TestLocalPublisherWritesIntoTheCellWithItsRegion(t *testing.T) {
	ctx := context.Background()
	s := localDNSScheme(t)
	resolver := copyOf(resolverContextObject("ctx", internalDNSTestProjectUID, internalDNSTestVPCUID, true), "ctx-uid")
	registration := newDNSObject(dnsRegistrationGVK, internalDNSTestNamespace, "compute-instances-"+shortHash("ctx-uid"))
	registration.SetLabels(map[string]string{internalDNSManagedBy: internalDNSManager})
	registration.SetGeneration(2)
	copyOf(registration, "reg-uid")
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(resolver, registration).Build()
	access := InternalDNSProjectAccess{Reader: originUIDReader{cl}, Writer: cl}
	r := &InternalDNSPublisherReconciler{LocalRegion: localTestRegion}
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, Namespace: internalDNSTestNamespace, UID: internalDNSTestInstanceUID}}
	owner := metav1.OwnerReference{APIVersion: computev1alpha.GroupVersion.String(), Kind: internalDNSInstanceKind, Name: instance.Name, UID: instance.UID, Controller: ptrBool(true)}
	attachment := instanceDNSAttachment{VPCUID: internalDNSTestVPCUID, Eligible: true, RecordSets: []any{map[string]any{internalDNSRecordsField: []any{map[string]any{}}}}}
	identity := InternalDNSProjectIdentity{ProjectName: internalDNSTestNamespace, ProjectUID: internalDNSTestProjectUID}

	_, err := r.reconcileAttachment(ctx, access, identity, internalDNSTestNamespace, instance, owner, attachment, "web-contribution")
	require.NoError(t, err)

	contribution := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "web-contribution")
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(contribution), contribution))
	assert.Equal(t, localTestRegion, contribution.GetLabels()[internalDNSOriginLabel])
	uid, _, _ := unstructured.NestedString(contribution.Object, internalDNSSpecField, internalDNSRegistrationRefField, internalDNSUIDField)
	assert.Equal(t, "reg-uid", uid, "the contribution must reference the project's registration UID")
	generation, _, _ := unstructured.NestedInt64(contribution.Object, internalDNSSpecField, internalDNSRegistrationRefField, "generation")
	assert.Equal(t, int64(7), generation, "the contribution must reference the project's registration generation")

	registrations := &unstructured.UnstructuredList{}
	registrations.SetGroupVersionKind(dnsRegistrationGVK.GroupVersion().WithKind("DNSRegistrationList"))
	require.NoError(t, cl.List(ctx, registrations))
	assert.Len(t, registrations.Items, 1, "a cell never creates the project's reservation")
}

func TestLocalPublisherWaitsForTheProjectsReservation(t *testing.T) {
	ctx := context.Background()
	s := localDNSScheme(t)
	resolver := copyOf(resolverContextObject("ctx", internalDNSTestProjectUID, internalDNSTestVPCUID, true), "ctx-uid")
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(resolver).Build()
	r := &InternalDNSPublisherReconciler{LocalRegion: localTestRegion}
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, Namespace: internalDNSTestNamespace, UID: internalDNSTestInstanceUID}}
	ready, err := r.reconcileAttachment(ctx, InternalDNSProjectAccess{Reader: originUIDReader{cl}, Writer: cl}, InternalDNSProjectIdentity{ProjectUID: internalDNSTestProjectUID}, internalDNSTestNamespace, instance, metav1.OwnerReference{}, instanceDNSAttachment{VPCUID: internalDNSTestVPCUID}, "web-contribution")
	require.NoError(t, err)
	assert.False(t, ready)
	contributions := &unstructured.UnstructuredList{}
	contributions.SetGroupVersionKind(dnsContributionGVK.GroupVersion().WithKind("DNSRecordContributionList"))
	require.NoError(t, cl.List(ctx, contributions))
	assert.Empty(t, contributions.Items)
}

func TestCellResolvesTheNetworkFromItsContext(t *testing.T) {
	s := localDNSScheme(t)
	s.AddKnownTypeWithName(networkContextGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(networkContextGVK.GroupVersion().WithKind("NetworkContextList"), &unstructured.UnstructuredList{})
	nc := &unstructured.Unstructured{}
	nc.SetGroupVersionKind(networkContextGVK)
	nc.SetNamespace(internalDNSTestNamespace)
	nc.SetName("app-us-east-1")
	nc.SetLabels(map[string]string{networkNameLabel: localTestNetwork, networkLocationLabel: localTestRegion, networkUIDLabel: "project-network-uid"})
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(nc).Build()
	uid, err := cellNetworkUID(localTestRegion)(context.Background(), cl, internalDNSTestNamespace, localTestNetwork)
	require.NoError(t, err)
	assert.Equal(t, types.UID("project-network-uid"), uid)
	_, err = cellNetworkUID(localTestRegion)(context.Background(), cl, internalDNSTestNamespace, "other")
	assert.Error(t, err)
}

func TestLocalPublisherWaitsForAProjectCopyOfTheReservation(t *testing.T) {
	ctx := context.Background()
	s := localDNSScheme(t)
	resolver := copyOf(resolverContextObject("ctx", internalDNSTestProjectUID, internalDNSTestVPCUID, true), "ctx-uid")
	local := newDNSObject(dnsRegistrationGVK, internalDNSTestNamespace, "compute-instances-"+shortHash("ctx-uid"))
	local.SetLabels(map[string]string{internalDNSManagedBy: internalDNSManager})
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(resolver, local).Build()
	got, err := lookupInstanceDNSReservation(ctx, InternalDNSProjectAccess{Reader: originUIDReader{cl}, Writer: cl}, func() *unstructured.Unstructured {
		r := resolver.DeepCopy()
		withOriginUID(r)
		return r
	}())
	require.NoError(t, err)
	assert.Nil(t, got, "a registration that is not the project's copy must not be referenced")
}

func TestManagementHandsAnInstanceToItsCellOnlyOnceTheCellPublished(t *testing.T) {
	ctx := context.Background()
	s := localDNSScheme(t)
	cl := fake.NewClientBuilder().WithScheme(s).Build()
	handed, err := cellPublished(ctx, cl, internalDNSTestNamespace, "cell-uid")
	require.NoError(t, err)
	assert.False(t, handed)
	fromCell := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "from-cell")
	fromCell.SetLabels(map[string]string{internalDNSInstanceUID: "cell-uid", internalDNSOriginLabel: localTestRegion})
	require.NoError(t, cl.Create(ctx, fromCell))
	handed, err = cellPublished(ctx, cl, internalDNSTestNamespace, "cell-uid")
	require.NoError(t, err)
	assert.True(t, handed)
}
