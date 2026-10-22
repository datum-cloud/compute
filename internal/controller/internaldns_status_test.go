// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPublicationStatusRequiresCurrentAuthorizedPublication(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, UID: internalDNSTestInstanceUID, Generation: 2}}
	for _, tc := range []struct {
		name                         string
		domain                       string
		foreignZone                  bool
		rejectedRegistration         bool
		current, observed, wantReady bool
	}{
		{name: "published", current: true, observed: true, wantReady: true},
		{name: "custom domain", domain: "private.example.net", current: true, observed: true, wantReady: true},
		{name: "different zone", foreignZone: true, current: true, observed: true},
		{name: "stale generation", observed: true},
		{name: "rejected contribution", current: true},
		{name: "name conflict", current: true, observed: true, rejectedRegistration: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.domain == "" {
				tc.domain = internalDNSTestDefaultDomain
			}
			s := runtime.NewScheme()
			s.AddKnownTypeWithName(dnsResolverContextGVK, &unstructured.Unstructured{})
			s.AddKnownTypeWithName(dnsResolverContextGVK.GroupVersion().WithKind("DNSResolverContextList"), &unstructured.UnstructuredList{})
			resolver := resolverContextObject("context", internalDNSTestProjectUID, internalDNSTestVPCUID, true)
			require.NoError(t, unstructured.SetNestedField(resolver.Object, tc.domain, internalDNSStatusField, "managedNamespace", "suffix"))
			s.AddKnownTypeWithName(dnsRegistrationGVK, &unstructured.Unstructured{})
			s.AddKnownTypeWithName(dnsContributionGVK, &unstructured.Unstructured{})
			registration := newDNSObject(dnsRegistrationGVK, internalDNSTestNamespace, internalDNSTestInstanceName)
			registration.SetGeneration(2)
			contribution := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, internalDNSTestInstanceName)
			contribution.SetGeneration(2)
			observed := int64(1)
			if tc.current {
				observed = 2
			}
			conditions := []any{map[string]any{internalDNSConditionTypeField: internalDNSConditionPublished, internalDNSStatusField: string(metav1.ConditionTrue), internalDNSObservedGenerationField: observed}}
			accepted := metav1.ConditionTrue
			if tc.rejectedRegistration {
				accepted = metav1.ConditionFalse
			}
			registrationConditions := append(append([]any(nil), conditions...), map[string]any{internalDNSConditionTypeField: internalDNSConditionAccepted, internalDNSStatusField: string(accepted), internalDNSObservedGenerationField: observed, internalDNSReasonField: internalDNSNameConflict})
			registration.Object[internalDNSStatusField] = map[string]any{"canonicalFQDN": allocatedInstanceDNSName(instance) + "." + tc.domain + ".", internalDNSConditionsField: registrationConditions}
			zoneName := "zone"
			if tc.foreignZone {
				zoneName = internalDNSTestForeignValue
			}
			registration.Object[internalDNSSpecField] = map[string]any{internalDNSZoneRefField: map[string]any{internalDNSNameField: zoneName}}
			contribution.Object[internalDNSStatusField] = map[string]any{internalDNSConditionsField: conditions}
			cl := fake.NewClientBuilder().WithScheme(s).WithObjects(resolver, registration, contribution).Build()
			r := &InternalDNSPublisherReconciler{Now: func() time.Time { return now }}
			got, err := r.publicationStatus(context.Background(), cl, InternalDNSProjectIdentity{ProjectUID: internalDNSTestProjectUID}, internalDNSTestNamespace, instance, instanceDNSAttachment{VPCUID: internalDNSTestVPCUID, Eligible: true}, internalDNSTestInstanceName, tc.observed, nil)
			require.NoError(t, err)
			if tc.foreignZone {
				require.Empty(t, got.Hostnames)
			} else {
				require.Equal(t, []string{allocatedInstanceDNSName(instance) + "." + tc.domain}, got.Hostnames)
			}
			require.Equal(t, tc.wantReady, got.Conditions[0].Status == metav1.ConditionTrue)
			if tc.rejectedRegistration {
				require.Equal(t, internalDNSNameConflict, got.Conditions[0].Reason)
			}
		})
	}
}

func TestPublicationStatusReportsUnconfiguredLocation(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsRegistrationGVK, &unstructured.Unstructured{})
	cl := fake.NewClientBuilder().WithScheme(s).Build()
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, UID: internalDNSTestInstanceUID, Generation: 1}}
	attachment := instanceDNSAttachment{VPCUID: internalDNSTestVPCUID, ObservationError: fmt.Errorf("%w for location %q", errNetworkObservationUnconfigured, "us-east-1")}
	got, err := (&InternalDNSPublisherReconciler{}).publicationStatus(context.Background(), cl, InternalDNSProjectIdentity{ProjectUID: internalDNSTestProjectUID}, internalDNSTestNamespace, instance, attachment, internalDNSTestInstanceName, false, nil)
	require.NoError(t, err)
	require.Len(t, got.Conditions, 1)
	require.Equal(t, metav1.ConditionFalse, got.Conditions[0].Status)
	require.Equal(t, "NetworkObservationUnconfigured", got.Conditions[0].Reason)
}

func TestReportPublicationPreservesRuntimeStatus(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, computev1alpha.AddToScheme(s))
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, Namespace: internalDNSTestNamespace}, Status: computev1alpha.InstanceStatus{Conditions: []metav1.Condition{{Type: computev1alpha.InstanceReady, Status: metav1.ConditionTrue}}}}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(instance).WithStatusSubresource(instance).Build()
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(instance), instance))
	r := &InternalDNSPublisherReconciler{}
	network := networkingv1alpha.NetworkRef{Name: internalDNSTestNetworkName, Namespace: internalDNSTestNamespace}
	require.NoError(t, r.reportPublication(context.Background(), cl, instance, []computev1alpha.InstanceDNSStatus{{Network: network}}))
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(instance), instance))
	require.Equal(t, metav1.ConditionTrue, instance.Status.Conditions[0].Status)
	require.Len(t, instance.Status.DNS, 1)
	require.Equal(t, network, instance.Status.DNS[0].Network)
}

func TestPublicationStatusMatchesNamespacedNetworkReference(t *testing.T) {
	network := networkingv1alpha.NetworkRef{Name: internalDNSTestNetworkName, Namespace: internalDNSTestNamespace}
	transition := metav1.NewTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		name    string
		network networkingv1alpha.NetworkRef
		matches bool
	}{
		{name: "same network", network: network, matches: true},
		{name: "different name", network: networkingv1alpha.NetworkRef{Name: "another-network", Namespace: network.Namespace}},
		{name: "same name in another namespace", network: networkingv1alpha.NetworkRef{Name: network.Name, Namespace: "another-namespace"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := runtime.NewScheme()
			s.AddKnownTypeWithName(dnsRegistrationGVK, &unstructured.Unstructured{})
			cl := fake.NewClientBuilder().WithScheme(s).Build()
			instance := &computev1alpha.Instance{Status: computev1alpha.InstanceStatus{DNS: []computev1alpha.InstanceDNSStatus{{
				Network:    tc.network,
				Conditions: []metav1.Condition{{Type: internalDNSConditionReady, Status: metav1.ConditionFalse, Reason: "PendingPublication", LastTransitionTime: transition}},
			}}}}
			r := &InternalDNSPublisherReconciler{}
			got, err := r.publicationStatus(context.Background(), cl, InternalDNSProjectIdentity{}, internalDNSTestNamespace, instance, instanceDNSAttachment{Network: network, Eligible: true}, internalDNSTestInstanceName, false, nil)
			require.NoError(t, err)
			require.Equal(t, network, got.Network)
			require.Len(t, got.Conditions, 1)
			require.Equal(t, tc.matches, got.Conditions[0].LastTransitionTime.Equal(&transition))
		})
	}
}
