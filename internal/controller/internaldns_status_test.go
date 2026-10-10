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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPublicationStatusRequiresCurrentUnexpiredPublication(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: "web", UID: "instance-uid", Generation: 2}}
	for _, tc := range []struct {
		name                           string
		deadline                       time.Time
		current, authorized, wantReady bool
	}{
		{name: "published", deadline: now.Add(time.Minute), current: true, authorized: true, wantReady: true},
		{name: "expired", deadline: now.Add(-time.Second), current: true, authorized: true},
		{name: "stale generation", deadline: now.Add(time.Minute), authorized: true},
		{name: "revoked grant", deadline: now.Add(time.Minute), current: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := runtime.NewScheme()
			s.AddKnownTypeWithName(dnsRegistrationGVK, &unstructured.Unstructured{})
			s.AddKnownTypeWithName(dnsContributionGVK, &unstructured.Unstructured{})
			registration := newDNSObject(dnsRegistrationGVK, internalDNSTestNamespace, "web")
			registration.SetGeneration(2)
			contribution := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, "web")
			contribution.SetGeneration(2)
			observed := int64(1)
			if tc.current {
				observed = 2
			}
			conditions := []any{map[string]any{"type": "Published", "status": "True", "observedGeneration": observed}}
			registration.Object["status"] = map[string]any{"canonicalFQDN": allocatedInstanceDNSName(instance) + ".datum.internal.", "conditions": conditions}
			contribution.Object["status"] = map[string]any{"conditions": conditions, "validUntil": tc.deadline.Format(time.RFC3339Nano)}
			cl := fake.NewClientBuilder().WithScheme(s).WithObjects(registration, contribution).Build()
			r := &InternalDNSPublisherReconciler{Now: func() time.Time { return now }}
			got, err := r.publicationStatus(context.Background(), cl, internalDNSTestNamespace, instance, instanceDNSAttachment{VPCUID: "vpc-a", Eligible: true}, "web", tc.authorized, nil)
			require.NoError(t, err)
			require.Equal(t, []string{allocatedInstanceDNSName(instance) + ".datum.internal"}, got.Hostnames)
			require.Equal(t, tc.wantReady, got.Conditions[0].Status == metav1.ConditionTrue)
		})
	}
}

func TestReportPublicationPreservesRuntimeStatus(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, computev1alpha.AddToScheme(s))
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: internalDNSTestNamespace}, Status: computev1alpha.InstanceStatus{Conditions: []metav1.Condition{{Type: computev1alpha.InstanceReady, Status: metav1.ConditionTrue}}}}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(instance).WithStatusSubresource(instance).Build()
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(instance), instance))
	r := &InternalDNSPublisherReconciler{}
	require.NoError(t, r.reportPublication(context.Background(), cl, instance, []computev1alpha.InstanceDNSStatus{{NetworkUID: "vpc-a"}}))
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(instance), instance))
	require.Equal(t, metav1.ConditionTrue, instance.Status.Conditions[0].Status)
	require.Len(t, instance.Status.DNS, 1)
}
