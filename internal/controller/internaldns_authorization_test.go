// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
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
)

func TestFindContributionGrantRequiresCurrentScopedAuthority(t *testing.T) {
	registration := newDNSObject(dnsRegistrationGVK, internalDNSTestNamespace, "web")
	registration.SetUID("registration-uid")
	registration.SetGeneration(2)
	for _, tc := range []struct {
		name  string
		path  []string
		value any
		found bool
	}{
		{name: "active matching grant", found: true},
		{name: "different principal", path: []string{"spec", "principal", "subject"}, value: "other"},
		{name: "different source cluster", path: []string{"spec", "principal", "clusterUID"}, value: "other"},
		{name: "different registration lifetime", path: []string{"spec", "registrationRef", "uid"}, value: "other"},
		{name: "stale registration generation", path: []string{"spec", "registrationRef", "generation"}, value: int64(1)},
		{name: "different name scope", path: []string{"spec", "nameScopes"}, value: []any{"other"}},
		{name: "missing record type", path: []string{"spec", "recordTypes"}, value: []any{"A"}},
		{name: "inactive grant", path: []string{"status", "conditions"}, value: []any{map[string]any{"type": "Active", "status": "False", "observedGeneration": int64(1)}}},
		{name: "stale active condition", path: []string{"status", "conditions"}, value: []any{map[string]any{"type": "Active", "status": "True", "observedGeneration": int64(0)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := runtime.NewScheme()
			s.AddKnownTypeWithName(dnsGrantGVK, &unstructured.Unstructured{})
			s.AddKnownTypeWithName(dnsGrantGVK.GroupVersion().WithKind("DNSContributionGrantList"), &unstructured.UnstructuredList{})
			grant := newDNSObject(dnsGrantGVK, internalDNSTestNamespace, "trusted-grant")
			grant.SetUID("grant-uid")
			grant.SetGeneration(1)
			grant.Object["spec"] = map[string]any{"registrationRef": objectReference(registration), "principal": map[string]any{"clusterUID": "project-api", "subject": "compute"}, "nameScopes": []any{"web"}, "recordTypes": []any{"A", "AAAA"}}
			grant.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Active", "status": "True", "observedGeneration": int64(1)}}}
			if len(tc.path) > 0 {
				require.NoError(t, unstructured.SetNestedField(grant.Object, tc.value, tc.path...))
			}
			cl := fake.NewClientBuilder().WithScheme(s).WithObjects(grant).Build()
			got, err := findContributionGrant(context.Background(), cl, internalDNSTestNamespace, registration, "project-api", "compute", "web")
			require.NoError(t, err)
			require.Equal(t, tc.found, got != nil)
		})
	}
}

func TestRefreshObservationCannotExtendSourceDeadline(t *testing.T) {
	now := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsContributionGVK, &unstructured.Unstructured{})
	c := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "web")
	c.SetUID("contribution")
	c.SetGeneration(1)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(c).WithStatusSubresource(c).Build()
	r := &InternalDNSPublisherReconciler{Now: func() time.Time { return now }, LeaseDuration: time.Minute}
	deadline := now.Add(25 * time.Second)
	for _, step := range []time.Duration{0, 10 * time.Second, 26 * time.Second} {
		now = time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC).Add(step)
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(c), c))
		_, err := r.refreshObservation(context.Background(), cl, c, 3, true, deadline)
		require.NoError(t, err)
		eligible, _, _ := unstructured.NestedBool(c.Object, "status", "eligible")
		require.Equal(t, step < 25*time.Second, eligible)
		if eligible {
			raw, _, _ := unstructured.NestedString(c.Object, "status", "validUntil")
			got, err := time.Parse(time.RFC3339Nano, raw)
			require.NoError(t, err)
			require.True(t, got.Equal(deadline))
		}
	}
}

func TestAllocatedInstanceDNSName(t *testing.T) {
	first := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: "Web_01", UID: "first"}}
	second := first.DeepCopy()
	second.UID = "second"
	require.Regexp(t, `^web-01-[a-f0-9]{16}$`, allocatedInstanceDNSName(first))
	require.NotEqual(t, allocatedInstanceDNSName(first), allocatedInstanceDNSName(second))
	require.Equal(t, allocatedInstanceDNSName(first), allocatedInstanceDNSName(first.DeepCopy()))
}

func TestPublisherWaitsForIssuerAndNeverMutatesGrants(t *testing.T) {
	s := runtime.NewScheme()
	for _, gvk := range []schema.GroupVersionKind{dnsResolverContextGVK, dnsRegistrationGVK, dnsGrantGVK, dnsContributionGVK} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	contextObject := resolverContextObject("context", "project-a", "vpc-a", true)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(contextObject).WithStatusSubresource(newDNSObject(dnsRegistrationGVK, "", ""), newDNSObject(dnsContributionGVK, "", "")).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		obj.SetUID(types.UID(obj.GetName() + "-uid"))
		obj.SetGeneration(1)
		return c.Create(ctx, obj, opts...)
	}}).Build()
	guard := interceptor.NewClient(cl, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			require.NotEqual(t, dnsGrantGVK, obj.GetObjectKind().GroupVersionKind())
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			require.NotEqual(t, dnsGrantGVK, obj.GetObjectKind().GroupVersionKind())
			return c.Update(ctx, obj, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			require.NotEqual(t, dnsGrantGVK, obj.GetObjectKind().GroupVersionKind())
			return c.Delete(ctx, obj, opts...)
		},
	})
	now := time.Now().UTC().Truncate(time.Second)
	r := &InternalDNSPublisherReconciler{PrincipalSubject: "compute", Now: func() time.Time { return now }}
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: internalDNSTestNamespace, UID: "instance-uid"}}
	owner := metav1.OwnerReference{APIVersion: computev1alpha.GroupVersion.String(), Kind: "Instance", Name: instance.Name, UID: instance.UID, Controller: ptrBool(true)}
	access := InternalDNSProjectAccess{Reader: guard, Writer: guard}
	identity := InternalDNSProjectIdentity{ProjectUID: "project-a", SourceClusterUID: "project-api"}
	attachment := instanceDNSAttachment{VPCUID: "vpc-a", Eligible: true, ValidUntil: now.Add(40 * time.Second), RecordSets: []any{map[string]any{"recordType": "A", "records": []any{map[string]any{"name": "", "ttl": int64(30), "a": map[string]any{"content": "10.0.0.8"}}}}}}
	ctx := context.Background()
	for range 2 {
		ready, err := r.reconcileAttachment(ctx, access, identity, internalDNSTestNamespace, instance, owner, attachment, "record")
		require.NoError(t, err)
		require.False(t, ready)
	}
	registration := newDNSObject(dnsRegistrationGVK, internalDNSTestNamespace, "record")
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(registration), registration))
	name, _, _ := unstructured.NestedString(registration.Object, "spec", "name")
	require.Equal(t, allocatedInstanceDNSName(instance), name)
	grant := newDNSObject(dnsGrantGVK, internalDNSTestNamespace, "issuer-grant")
	grant.Object["spec"] = map[string]any{"registrationRef": objectReference(registration), "principal": map[string]any{"clusterUID": "project-api", "subject": "compute"}, "nameScopes": []any{name}, "recordTypes": []any{"A", "AAAA"}}
	grant.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Active", "status": "True", "observedGeneration": int64(1)}}, "activeWriterEpoch": int64(3), "observedGrantGeneration": int64(1), "observedRegistrationGeneration": int64(1)}
	require.NoError(t, cl.Create(ctx, grant))
	ready, err := r.reconcileAttachment(ctx, access, identity, internalDNSTestNamespace, instance, owner, attachment, "record")
	require.NoError(t, err)
	require.False(t, ready)
	contribution := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "record")
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(contribution), contribution))
	contribution.Object["status"] = map[string]any{"writerEpoch": int64(3)}
	require.NoError(t, cl.Status().Update(ctx, contribution))
	ready, err = r.reconcileAttachment(ctx, access, identity, internalDNSTestNamespace, instance, owner, attachment, "record")
	require.NoError(t, err)
	require.True(t, ready)
	require.NoError(t, r.deletePublishedResources(ctx, access, internalDNSTestNamespace, instance.UID, nil))
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(grant), grant))
}
