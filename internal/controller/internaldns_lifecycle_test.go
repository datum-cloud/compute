// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	lifecycleTestProject      = "project"
	lifecycleTestReason       = "Test"
	lifecycleTestAddressCIDR  = internalDNSTestAddress + "/32"
	lifecycleTestMovedAddress = "10.0.0.9"
	lifecycleTestMovedCIDR    = lifecycleTestMovedAddress + "/32"
)

// dnsLifecycle drives the publisher through an Instance's life against a fake
// project API. The fake has no garbage collector, so anything left behind by a
// deleted or replaced Instance stays visible to the test.
type dnsLifecycle struct {
	t         *testing.T
	client    client.Client
	publisher *InternalDNSPublisherReconciler
}

func newDNSLifecycle(t *testing.T) *dnsLifecycle {
	scheme := newTestScheme(t)
	require.NoError(t, networkingv1alpha.AddToScheme(scheme))
	for _, gvk := range []schema.GroupVersionKind{dnsResolverContextGVK, dnsRegistrationGVK, dnsContributionGVK} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	network := &networkingv1alpha.Network{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestNetworkName, Namespace: internalDNSTestNamespace, UID: internalDNSTestVPCUID}}
	resolver := resolverContextObject("context", internalDNSTestProjectUID, internalDNSTestVPCUID, true)
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, resolver).
		WithStatusSubresource(&computev1alpha.Instance{}, newDNSObject(dnsRegistrationGVK, "", ""), newDNSObject(dnsContributionGVK, "", "")).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if obj.GetUID() == "" {
					obj.SetUID(types.UID(obj.GetName() + "-uid"))
				}
				obj.SetGeneration(1)
				return c.Create(ctx, obj, opts...)
			},
		}).Build()
	return &dnsLifecycle{t: t, client: cl, publisher: &InternalDNSPublisherReconciler{
		mgr:                internalDNSTestClusters{lifecycleTestProject: internalDNSTestCluster{client: cl}},
		ProjectIdentities:  map[string]InternalDNSProjectIdentity{lifecycleTestProject: {ProjectUID: internalDNSTestProjectUID}},
		ObservationSources: map[string]InternalDNSObservationSource{"served": {Reader: cl}},
	}}
}

// lifecycleInstance is a running Instance on the test network. Runtimes that
// do not run instances as Pods never report RuntimeReady.
func lifecycleInstance(name string, uid types.UID, address string, reportsRuntime bool) *computev1alpha.Instance {
	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: internalDNSTestNamespace, UID: uid, Labels: map[string]string{computev1alpha.LocationLabel: "served"}},
		Spec:       computev1alpha.InstanceSpec{NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{{Network: networkingv1alpha.NetworkRef{Name: internalDNSTestNetworkName}}}},
	}
	setLifecycleRunning(instance, true, reportsRuntime)
	instance.Status.NetworkInterfaces = []computev1alpha.InstanceNetworkInterfaceStatus{{
		Name:                defaultInterfaceName,
		NetworkInterfaceRef: &networkingv1alpha.LocalNetworkInterfaceRef{Name: name + "-nic"},
		Addresses:           []computev1alpha.InstanceNetworkInterfaceAddress{{Address: address}},
		Conditions: []metav1.Condition{
			{Type: computev1alpha.InstanceNetworkInterfaceAllocated, Status: metav1.ConditionTrue, Reason: lifecycleTestReason},
			{Type: computev1alpha.InstanceNetworkInterfaceProgrammed, Status: metav1.ConditionTrue, Reason: lifecycleTestReason},
		},
	}}
	return instance
}

func setLifecycleRunning(instance *computev1alpha.Instance, running, reportsRuntime bool) {
	status := map[bool]metav1.ConditionStatus{true: metav1.ConditionTrue, false: metav1.ConditionFalse}[running]
	apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceReady, Status: status, Reason: lifecycleTestReason})
	if reportsRuntime {
		apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceRuntimeReady, Status: status, Reason: lifecycleTestReason})
	}
}

func (l *dnsLifecycle) create(instance *computev1alpha.Instance) {
	l.t.Helper()
	status := instance.Status
	require.NoError(l.t, l.client.Create(context.Background(), instance))
	instance.Status = status
	require.NoError(l.t, l.client.Status().Update(context.Background(), instance))
}

func (l *dnsLifecycle) updateStatus(name string, mutate func(*computev1alpha.Instance)) {
	l.t.Helper()
	instance := l.instance(name)
	mutate(instance)
	require.NoError(l.t, l.client.Status().Update(context.Background(), instance))
}

func (l *dnsLifecycle) delete(name string) {
	l.t.Helper()
	require.NoError(l.t, l.client.Delete(context.Background(), l.instance(name)))
}

func (l *dnsLifecycle) instance(name string) *computev1alpha.Instance {
	l.t.Helper()
	instance := &computev1alpha.Instance{}
	require.NoError(l.t, l.client.Get(context.Background(), client.ObjectKey{Namespace: internalDNSTestNamespace, Name: name}, instance))
	return instance
}

// reconcile runs the publisher for name until it settles, standing in for the
// DNS operator between passes by accepting and publishing what it wrote.
func (l *dnsLifecycle) reconcile(name string) {
	l.t.Helper()
	for range 4 {
		_, err := l.publisher.Reconcile(context.Background(), mcreconcile.Request{ClusterName: lifecycleTestProject, Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: internalDNSTestNamespace, Name: name}}})
		require.NoError(l.t, err)
		l.publishAll()
	}
}

func (l *dnsLifecycle) publishAll() {
	l.t.Helper()
	for _, gvk := range []schema.GroupVersionKind{dnsRegistrationGVK, dnsContributionGVK} {
		for _, item := range l.list(gvk) {
			conditions := []any{map[string]any{internalDNSConditionTypeField: internalDNSConditionPublished, internalDNSStatusField: string(metav1.ConditionTrue), internalDNSObservedGenerationField: item.GetGeneration()}}
			if gvk == dnsRegistrationGVK {
				conditions = append(conditions, map[string]any{internalDNSConditionTypeField: internalDNSConditionAccepted, internalDNSStatusField: string(metav1.ConditionTrue), internalDNSObservedGenerationField: item.GetGeneration()})
				item.Object[internalDNSStatusField] = map[string]any{"canonicalFQDN": instanceDNSScope + "." + internalDNSTestDefaultDomain + ".", internalDNSConditionsField: conditions}
			} else {
				item.Object[internalDNSStatusField] = map[string]any{internalDNSConditionsField: conditions}
			}
			require.NoError(l.t, l.client.Status().Update(context.Background(), &item))
		}
	}
}

func (l *dnsLifecycle) list(gvk schema.GroupVersionKind) []unstructured.Unstructured {
	l.t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	require.NoError(l.t, l.client.List(context.Background(), list, client.InNamespace(internalDNSTestNamespace)))
	return list.Items
}

// answers is what the private zone serves for an instance name: the
// addresses of every eligible contribution to it, as the DNS operator serves
// them. It also checks that each answer carries the 30 second TTL, which
// bounds how long a resolver may keep serving a withdrawn address.
func (l *dnsLifecycle) answers(name string) []string {
	l.t.Helper()
	owner := name + "." + instanceDNSScope
	var addresses []string
	for _, contribution := range l.list(dnsContributionGVK) {
		if eligible, _, _ := unstructured.NestedBool(contribution.Object, internalDNSSpecField, internalDNSEligibleField); !eligible {
			continue
		}
		recordSets, _, _ := unstructured.NestedSlice(contribution.Object, internalDNSSpecField, internalDNSRecordSetsField)
		for _, rrset := range recordSets {
			for _, raw := range rrset.(map[string]any)[internalDNSRecordsField].([]any) {
				record := raw.(map[string]any)
				if record[internalDNSNameField] != owner {
					continue
				}
				require.EqualValues(l.t, 30, record["ttl"])
				for _, field := range []string{"a", "aaaa"} {
					if data, ok := record[field].(map[string]any); ok {
						addresses = append(addresses, data[internalDNSContentField].(string))
					}
				}
			}
		}
	}
	sort.Strings(addresses)
	return addresses
}

func (l *dnsLifecycle) requireHostname(name string, published bool) {
	l.t.Helper()
	instance := l.instance(name)
	require.Len(l.t, instance.Status.DNS, 1)
	ready := apimeta.FindStatusCondition(instance.Status.DNS[0].Conditions, internalDNSConditionReady)
	require.NotNil(l.t, ready)
	require.Equal(l.t, published, ready.Status == metav1.ConditionTrue, "DNS Ready is %s: %s", ready.Status, ready.Reason)
	require.Equal(l.t, []string{name + "." + instanceDNSScope + "." + internalDNSTestDefaultDomain}, instance.Status.DNS[0].Hostnames)
	// The publisher writes only DNS status; Instance readiness is what the
	// test set, whatever happened to publication.
	require.Equal(l.t, published, apimeta.IsStatusConditionTrue(instance.Status.Conditions, computev1alpha.InstanceReady))
}

// TestInstanceNameLifecycle follows one instance name through creation,
// address changes, stop and start, recreation, rename and deletion, for a
// runtime that reports RuntimeReady and one that does not, on IPv4 and IPv6.
func TestInstanceNameLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name               string
		reportsRuntime     bool
		first, second      string
		firstAnswer, moved string
	}{
		{name: "pod runtime on IPv4", reportsRuntime: true, first: lifecycleTestAddressCIDR, firstAnswer: internalDNSTestAddress, second: lifecycleTestMovedCIDR, moved: lifecycleTestMovedAddress},
		{name: "runtime without a runtime condition on delegated IPv6", first: "fd20::5:0:0/96", firstAnswer: "fd20::5:0:0", second: "fd20::6:0:0/96", moved: "fd20::6:0:0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newDNSLifecycle(t)

			// Create: a running instance gets its name, shown in status.
			l.create(lifecycleInstance(internalDNSTestInstanceName, "api-1", tc.first, tc.reportsRuntime))
			l.reconcile(internalDNSTestInstanceName)
			require.Equal(t, []string{tc.firstAnswer}, l.answers(internalDNSTestInstanceName))
			l.requireHostname(internalDNSTestInstanceName, true)

			// Address change: the old address is replaced, not added to.
			l.updateStatus(internalDNSTestInstanceName, func(i *computev1alpha.Instance) { i.Status.NetworkInterfaces[0].Addresses[0].Address = tc.second })
			l.reconcile(internalDNSTestInstanceName)
			require.Equal(t, []string{tc.moved}, l.answers(internalDNSTestInstanceName))

			// Stop: the name stops answering but stays reserved, and comes back
			// on start.
			l.updateStatus(internalDNSTestInstanceName, func(i *computev1alpha.Instance) { setLifecycleRunning(i, false, tc.reportsRuntime) })
			l.reconcile(internalDNSTestInstanceName)
			require.Empty(t, l.answers(internalDNSTestInstanceName))
			l.requireHostname(internalDNSTestInstanceName, false)
			l.updateStatus(internalDNSTestInstanceName, func(i *computev1alpha.Instance) { setLifecycleRunning(i, true, tc.reportsRuntime) })
			l.reconcile(internalDNSTestInstanceName)
			require.Equal(t, []string{tc.moved}, l.answers(internalDNSTestInstanceName))
			l.requireHostname(internalDNSTestInstanceName, true)

			// Recreate: the replacement's first reconcile runs before the old
			// Instance's deletion is ever seen, and old answers still go away.
			l.delete(internalDNSTestInstanceName)
			l.create(lifecycleInstance(internalDNSTestInstanceName, "api-2", tc.first, tc.reportsRuntime))
			l.reconcile(internalDNSTestInstanceName)
			require.Equal(t, []string{tc.firstAnswer}, l.answers(internalDNSTestInstanceName))

			// A late write from the old lifetime cannot bring its answer back.
			stale := l.list(dnsContributionGVK)[0].DeepCopy()
			stale.SetName("instance-stale")
			stale.SetUID("")
			stale.SetResourceVersion("")
			stale.SetLabels(map[string]string{internalDNSInstanceUID: "api-1", internalDNSInstanceNameHash: shortHash(internalDNSTestInstanceName), internalDNSManagedBy: internalDNSManager})
			stale.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: computev1alpha.GroupVersion.String(), Kind: internalDNSInstanceKind, Name: internalDNSTestInstanceName, UID: "api-1", Controller: ptrBool(true)}})
			require.NoError(t, unstructured.SetNestedSlice(stale.Object, []any{map[string]any{internalDNSRecordTypeField: "A", internalDNSRecordsField: []any{map[string]any{internalDNSNameField: "api.instances", "ttl": int64(30), "a": map[string]any{internalDNSContentField: "10.9.9.9"}}}}}, internalDNSSpecField, internalDNSRecordSetsField))
			require.NoError(t, l.client.Create(context.Background(), stale))
			l.reconcile(internalDNSTestInstanceName)
			require.Equal(t, []string{tc.firstAnswer}, l.answers(internalDNSTestInstanceName))

			// Rename: the old name stops answering and the new one starts.
			l.delete(internalDNSTestInstanceName)
			l.create(lifecycleInstance("web", "web-1", tc.second, tc.reportsRuntime))
			l.reconcile(internalDNSTestInstanceName)
			l.reconcile("web")
			require.Empty(t, l.answers(internalDNSTestInstanceName))
			require.Equal(t, []string{tc.moved}, l.answers("web"))
			l.requireHostname("web", true)

			// Delete: nothing is left answering, and the namespace's name
			// reservation remains for the next instance.
			l.delete("web")
			l.reconcile("web")
			require.Empty(t, l.answers("web"))
			require.Empty(t, l.list(dnsContributionGVK))
			require.Len(t, l.list(dnsRegistrationGVK), 1)
		})
	}
}

// TestDeletedInstanceWithdrawalLeavesOtherInstancesAlone checks that cleaning
// up after a deleted Instance only touches resources that Instance owned.
func TestDeletedInstanceWithdrawalLeavesOtherInstancesAlone(t *testing.T) {
	l := newDNSLifecycle(t)
	l.create(lifecycleInstance(internalDNSTestInstanceName, "api-1", lifecycleTestAddressCIDR, true))
	l.create(lifecycleInstance("api-2", "api-2-1", lifecycleTestMovedCIDR, true))
	l.reconcile(internalDNSTestInstanceName)
	l.reconcile("api-2")

	l.delete(internalDNSTestInstanceName)
	l.reconcile(internalDNSTestInstanceName)
	require.Empty(t, l.answers(internalDNSTestInstanceName))
	require.Equal(t, []string{lifecycleTestMovedAddress}, l.answers("api-2"))
}

// TestRecreatedInstanceWithdrawsPredecessorWhenPublicationFails checks that a
// replacement which cannot publish yet still stops its predecessor answering.
func TestRecreatedInstanceWithdrawsPredecessorWhenPublicationFails(t *testing.T) {
	l := newDNSLifecycle(t)
	l.create(lifecycleInstance(internalDNSTestInstanceName, "api-1", lifecycleTestAddressCIDR, true))
	l.reconcile(internalDNSTestInstanceName)
	require.Equal(t, []string{internalDNSTestAddress}, l.answers(internalDNSTestInstanceName))

	l.delete(internalDNSTestInstanceName)
	replacement := lifecycleInstance(internalDNSTestInstanceName, "api-2", lifecycleTestMovedCIDR, true)
	replacement.Spec.NetworkInterfaces[0].Network.Name = "missing"
	l.create(replacement)
	_, err := l.publisher.Reconcile(context.Background(), mcreconcile.Request{ClusterName: lifecycleTestProject, Request: reconcile.Request{NamespacedName: client.ObjectKeyFromObject(replacement)}})
	require.Error(t, err)
	require.Empty(t, l.answers(internalDNSTestInstanceName))
}

// TestUnservedInstanceAPIDoesNotWithdrawNames checks that a 404 because the
// API is not serving Instances is not mistaken for a deleted Instance.
func TestUnservedInstanceAPIDoesNotWithdrawNames(t *testing.T) {
	l := newDNSLifecycle(t)
	l.create(lifecycleInstance(internalDNSTestInstanceName, "api-1", lifecycleTestAddressCIDR, true))
	l.reconcile(internalDNSTestInstanceName)

	unserved := interceptor.NewClient(l.client.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*computev1alpha.Instance); ok {
				return apierrors.NewNotFound(schema.GroupResource{Group: computev1alpha.GroupVersion.Group, Resource: ""}, "")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	l.publisher.mgr = internalDNSTestClusters{lifecycleTestProject: internalDNSTestCluster{client: unserved}}
	_, err := l.publisher.Reconcile(context.Background(), mcreconcile.Request{ClusterName: lifecycleTestProject, Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: internalDNSTestNamespace, Name: internalDNSTestInstanceName}}})
	require.Error(t, err)
	require.Equal(t, []string{internalDNSTestAddress}, l.answers(internalDNSTestInstanceName))
}
