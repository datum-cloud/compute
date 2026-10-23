// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"testing"

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
	resourcemanagerv1alpha1 "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
)

const (
	internalDNSTestDiscoveredProjectName = "project-b"
	internalDNSTestProjectUID            = "project-a"
	internalDNSTestNamespace             = "dns-test"
	internalDNSTestDefaultDomain         = "datum.internal"
	internalDNSTestInstanceName          = "api"
	internalDNSTestInstanceUID           = "dns-instance-uid"
	internalDNSTestEdgeUID               = "dns-edge-instance"
	internalDNSTestForeignValue          = "dns-unauthorized"
	internalDNSTestSubject               = "system:serviceaccount:dns-test:publisher"
	internalDNSTestObservationNamespace  = "dns-observation-project"
	internalDNSTestNetworkName           = "dns-application"
	internalDNSTestVPCUID                = "dns-test-vpc"
	internalDNSTestSourceAPIUID          = "dns-project-api"
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
		return InternalDNSObservedInterface{Addresses: []networkingv1alpha.NetworkInterfaceAddress{{Address: "10.0.0.8/32"}, {Address: "2001:db8::8/128"}, {Address: "2001:db8:1::/96"}}, Eligible: eligible}, nil
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
	wrong := resolverContextObject("wrong", "other-project", internalDNSTestVPCUID, true)
	right := resolverContextObject("right", "project-a", internalDNSTestVPCUID, true)
	wrongVPC := resolverContextObject("wrong-vpc", "project-a", "vpc-b", true)
	pending := resolverContextObject("pending", "project-a", internalDNSTestVPCUID, false)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(wrong, wrongVPC, pending, right).Build()

	got, err := findResolverContext(context.Background(), cl, internalDNSTestNamespace, "project-a", internalDNSTestVPCUID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "right", got.GetName())

	got, err = findResolverContext(context.Background(), cl, internalDNSTestNamespace, internalDNSTestDiscoveredProjectName, internalDNSTestVPCUID)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestFindResolverContextRejectsAmbiguityAndIgnoresTerminatingBinding(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsResolverContextGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsResolverContextGVK.GroupVersion().WithKind("DNSResolverContextList"), &unstructured.UnstructuredList{})
	first := resolverContextObject("first", "project-a", internalDNSTestVPCUID, true)
	second := resolverContextObject("second", "project-a", internalDNSTestVPCUID, true)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(first, second).Build()

	_, err := findResolverContext(context.Background(), cl, internalDNSTestNamespace, "project-a", internalDNSTestVPCUID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple ready")

	now := metav1.Now()
	second.SetDeletionTimestamp(&now)
	second.SetFinalizers([]string{"test.example/finalizer"})
	cl = fake.NewClientBuilder().WithScheme(s).WithObjects(first, second).Build()
	got, err := findResolverContext(context.Background(), cl, internalDNSTestNamespace, "project-a", internalDNSTestVPCUID)
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
	obj.Object[internalDNSSpecField] = map[string]any{"consumerID": projectUID + "/" + vpcUID}
	obj.Object[internalDNSStatusField] = map[string]any{
		internalDNSConditionsField: []any{map[string]any{internalDNSConditionTypeField: internalDNSConditionReady, internalDNSStatusField: status, internalDNSObservedGenerationField: int64(1)}},
		"managedNamespace":         map[string]any{"suffix": internalDNSTestDefaultDomain, internalDNSZoneRefField: map[string]any{internalDNSNameField: "zone"}},
	}
	return obj
}

func TestEnsureDNSObjectRefusesForeignCollision(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(dnsRegistrationGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dnsRegistrationGVK.GroupVersion().WithKind("DNSRegistrationList"), &unstructured.UnstructuredList{})
	foreign := newDNSObject(dnsRegistrationGVK, internalDNSTestNamespace, "instance-collision")
	foreign.SetUID("foreign")
	foreign.Object[internalDNSSpecField] = map[string]any{internalDNSNameField: "someone-else"}
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

func TestDNSProjectIdentityDiscoversNewProjectsAndReplacementIdentities(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, resourcemanagerv1alpha1.AddToScheme(scheme))
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	resolve := InternalDNSProjectIdentityResolver(cl)
	_, err := resolve(context.Background(), internalDNSTestDiscoveredProjectName)
	require.Error(t, err)

	project := &resourcemanagerv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestDiscoveredProjectName, UID: "project-b-first"}}
	require.NoError(t, cl.Create(context.Background(), project))
	identity, err := resolve(context.Background(), project.Name)
	require.NoError(t, err)
	assert.Equal(t, InternalDNSProjectIdentity{ProjectName: project.Name, ProjectUID: project.UID}, identity)

	require.NoError(t, cl.Delete(context.Background(), project))
	project.ResourceVersion = ""
	project.UID = "project-b-second"
	require.NoError(t, cl.Create(context.Background(), project))
	identity, err = resolve(context.Background(), project.Name)
	require.NoError(t, err)
	assert.Equal(t, project.UID, identity.ProjectUID)
}

func TestDNSProjectIdentityRejectsInactiveProjectsAndReadFailures(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, resourcemanagerv1alpha1.AddToScheme(scheme))
	now := metav1.Now()
	for _, project := range []*resourcemanagerv1alpha1.Project{
		{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestDiscoveredProjectName}},
		{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestDiscoveredProjectName, UID: internalDNSTestDiscoveredProjectName, DeletionTimestamp: &now, Finalizers: []string{"test.datum.net/cleanup"}}},
	} {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project).Build()
		_, err := InternalDNSProjectIdentityResolver(cl)(context.Background(), project.Name)
		require.Error(t, err)
	}
	unavailable := errors.New("project API unavailable")
	reader := &unavailableDNSProjectReader{err: unavailable}
	_, err := InternalDNSProjectIdentityResolver(reader)(context.Background(), internalDNSTestDiscoveredProjectName)
	require.ErrorIs(t, err, unavailable)
}

type unavailableDNSProjectReader struct {
	client.Reader
	err error
}

func (r *unavailableDNSProjectReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return r.err
}
