// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

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
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func TestUnavailableObservationLeavesPublishedStateUntouched(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsContributionGVK, &unstructured.Unstructured{})
	published := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "record")
	published.SetUID("contribution")
	published.Object[internalDNSSpecField] = map[string]any{"recordSets": []any{map[string]any{internalDNSRecordTypeField: "A", internalDNSRecordsField: []any{map[string]any{internalDNSNameField: "api", "a": map[string]any{internalDNSContentField: "10.0.0.8"}}}}}}
	published.Object[internalDNSSpecField].(map[string]any)[internalDNSEligibleField] = true
	published.Object[internalDNSStatusField] = map[string]any{internalDNSConditionsField: []any{map[string]any{internalDNSConditionTypeField: internalDNSConditionPublished, internalDNSStatusField: string(metav1.ConditionTrue)}}}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(published).WithStatusSubresource(published).Build()
	r := &InternalDNSPublisherReconciler{}
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: "api", UID: internalDNSTestInstanceUID}}
	_, err := r.reconcileAttachment(context.Background(), InternalDNSProjectAccess{Reader: cl, Writer: cl}, InternalDNSProjectIdentity{}, internalDNSTestNamespace, instance, metav1.OwnerReference{}, instanceDNSAttachment{ObservationError: context.DeadlineExceeded}, "record")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	current := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "record")
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(published), current))
	require.Equal(t, published.Object, current.Object)
}

func TestUnconfiguredObservationIsNotAPublicationFailure(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsContributionGVK, &unstructured.Unstructured{})
	published := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "record")
	published.SetUID("contribution")
	published.Object[internalDNSSpecField] = map[string]any{internalDNSEligibleField: true}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(published).Build()
	r := &InternalDNSPublisherReconciler{}
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, UID: internalDNSTestInstanceUID}}
	attachment := instanceDNSAttachment{ObservationError: fmt.Errorf("%w for location %q", errNetworkObservationUnconfigured, "us-east-1")}
	ready, err := r.reconcileAttachment(context.Background(), InternalDNSProjectAccess{Reader: cl, Writer: cl}, InternalDNSProjectIdentity{}, internalDNSTestNamespace, instance, metav1.OwnerReference{}, attachment, "record")
	require.NoError(t, err)
	require.False(t, ready)
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
	resolver := resolverContextObject("context", internalDNSTestProjectUID, internalDNSTestVPCUID, true)
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
		ready, err = reconciler.reconcileAttachment(context.Background(), access, InternalDNSProjectIdentity{ProjectUID: internalDNSTestProjectUID}, internalDNSTestNamespace, instance, owner, attachment, "record")
		require.NoError(t, err)
	}
	require.True(t, ready)
	contribution := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "record")
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(contribution), contribution))
	eligible, _, _ := unstructured.NestedBool(contribution.Object, internalDNSSpecField, internalDNSEligibleField)
	require.True(t, eligible)
	reason, _, _ := unstructured.NestedString(contribution.Object, internalDNSSpecField, internalDNSReasonField)
	require.Equal(t, "AddressAllocatedAndProgrammed", reason)

	contribution.Object[internalDNSStatusField] = map[string]any{internalDNSConditionsField: []any{map[string]any{internalDNSConditionTypeField: internalDNSConditionPublished, internalDNSStatusField: string(metav1.ConditionTrue)}}}
	require.NoError(t, cl.Status().Update(context.Background(), contribution))
	publishedStatus := contribution.Object[internalDNSStatusField]
	attachment.Eligible = false
	attachment.RecordSets = []any{map[string]any{internalDNSRecordTypeField: "A", internalDNSRecordsField: []any{map[string]any{internalDNSNameField: "", "a": map[string]any{internalDNSContentField: "10.0.0.9"}}}}}
	_, err := reconciler.reconcileAttachment(context.Background(), access, InternalDNSProjectIdentity{ProjectUID: internalDNSTestProjectUID}, internalDNSTestNamespace, instance, owner, attachment, "record")
	require.NoError(t, err)
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(contribution), contribution))
	eligible, _, _ = unstructured.NestedBool(contribution.Object, internalDNSSpecField, internalDNSEligibleField)
	require.False(t, eligible)
	recordSets, _, _ := unstructured.NestedSlice(contribution.Object, internalDNSSpecField, internalDNSRecordSetsField)
	record := recordSets[0].(map[string]any)[internalDNSRecordsField].([]any)[0].(map[string]any)
	require.Equal(t, "10.0.0.9", record["a"].(map[string]any)[internalDNSContentField])
	require.Equal(t, publishedStatus, contribution.Object[internalDNSStatusField])
}

type internalDNSTestCluster struct {
	cluster.Cluster
	client client.Client
}

func (c internalDNSTestCluster) GetAPIReader() client.Reader { return c.client }
func (c internalDNSTestCluster) GetClient() client.Client    { return c.client }

type internalDNSTestClusters map[multicluster.ClusterName]cluster.Cluster

func (c internalDNSTestClusters) GetCluster(_ context.Context, name multicluster.ClusterName) (cluster.Cluster, error) {
	return c[name], nil
}

func TestPublisherReconcileRequeuesAtTheResyncInterval(t *testing.T) {
	scheme := newTestScheme(t)
	for _, gvk := range []schema.GroupVersionKind{dnsResolverContextGVK, dnsRegistrationGVK, dnsContributionGVK} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, Namespace: internalDNSTestNamespace, UID: internalDNSTestInstanceUID}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).WithStatusSubresource(instance).Build()
	const project = "project"
	r := &InternalDNSPublisherReconciler{
		mgr:               internalDNSTestClusters{project: internalDNSTestCluster{client: cl}},
		ProjectIdentities: map[string]InternalDNSProjectIdentity{project: {ProjectUID: internalDNSTestProjectUID}},
	}

	result, err := r.Reconcile(context.Background(), mcreconcile.Request{ClusterName: project, Request: reconcile.Request{NamespacedName: client.ObjectKeyFromObject(instance)}})
	require.NoError(t, err)
	require.GreaterOrEqual(t, result.RequeueAfter, internalDNSResyncInterval)
	require.Less(t, result.RequeueAfter, internalDNSResyncInterval+internalDNSResyncInterval/2)
}

func TestDNSPollingSpreadsReconciliation(t *testing.T) {
	for _, base := range []time.Duration{internalDNSResyncInterval} {
		intervals := map[time.Duration]bool{}
		for range 100 {
			got := internalDNSPollInterval(base)
			require.GreaterOrEqual(t, got, base)
			require.Less(t, got, base+base/2)
			intervals[got] = true
		}
		require.Greater(t, len(intervals), 1)
	}
}
