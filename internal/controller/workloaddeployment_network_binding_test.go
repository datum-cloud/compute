// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

const (
	testNetworkName  = "default"
	testLocationName = "dfw"
	testHubWDUID     = types.UID("hub-wd-uid-9999")
)

// testHubDeployment returns the hub copy of the test WorkloadDeployment, already
// carrying an interface on testNetworkName. Options adjust it further.
func testHubDeployment(opts ...func(*computev1alpha.WorkloadDeployment)) *computev1alpha.WorkloadDeployment {
	wd := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWDName,
			Namespace: testKarmadaNSStr,
			UID:       testHubWDUID,
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode: testCityCodeLAX,
			Template: computev1alpha.InstanceTemplateSpec{
				Spec: computev1alpha.InstanceSpec{
					NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{{
						Name:    "eth0",
						Network: networkingv1alpha.NetworkRef{Name: testNetworkName},
					}},
				},
			},
		},
	}
	for _, opt := range opts {
		opt(wd)
	}
	return wd
}

// withServingLocation records the location a cell reported for the deployment,
// which on the hub arrives through Karmada status aggregation.
func withServingLocation(name string) func(*computev1alpha.WorkloadDeployment) {
	return func(wd *computev1alpha.WorkloadDeployment) {
		wd.Status.Location = &networkingv1alpha.LocationReference{Name: name}
	}
}

// withInterfaceNetwork points the deployment's single interface at a network.
func withInterfaceNetwork(name string) func(*computev1alpha.WorkloadDeployment) {
	return func(wd *computev1alpha.WorkloadDeployment) {
		wd.Spec.Template.Spec.NetworkInterfaces[0].Network = networkingv1alpha.NetworkRef{Name: name}
	}
}

func getBinding(t *testing.T, cl client.Client) (*networkingv1alpha.NetworkBinding, error) {
	t.Helper()
	var binding networkingv1alpha.NetworkBinding
	err := cl.Get(context.Background(), types.NamespacedName{
		Namespace: testKarmadaNSStr,
		Name:      testWDName,
	}, &binding)
	return &binding, err
}

// TestEnsureNetworkBinding_DeclaresPresenceWhereDeploymentRuns verifies the
// binding a placed deployment produces: the network it asks for, the location a
// cell serves it from, a consumer record naming the deployment, and a real owner
// reference so the hub releases it with its owner.
func TestEnsureNetworkBinding_DeclaresPresenceWhereDeploymentRuns(t *testing.T) {
	t.Parallel()

	hubWD := testHubDeployment(withServingLocation(testLocationName))
	karmadaClient := newKarmadaFakeClient(hubWD)
	r := newTestFederator(newProjectFakeClient(), karmadaClient)

	binding, err := r.ensureNetworkBinding(context.Background(), hubWD)
	require.NoError(t, err)
	require.NotNil(t, binding)

	stored, err := getBinding(t, karmadaClient)
	require.NoError(t, err)

	assert.Equal(t, testNetworkName, stored.Spec.Network.Name)
	assert.Equal(t, testLocationName, stored.Spec.Location.Name)

	require.NotNil(t, stored.Spec.Consumer)
	assert.Equal(t, computev1alpha.GroupVersion.Group, stored.Spec.Consumer.APIGroup)
	assert.Equal(t, kindWorkloadDeployment, stored.Spec.Consumer.Kind)
	assert.Equal(t, testWDName, stored.Spec.Consumer.Name)

	require.Len(t, stored.OwnerReferences, 1)
	owner := stored.OwnerReferences[0]
	assert.Equal(t, kindWorkloadDeployment, owner.Kind)
	assert.Equal(t, testWDName, owner.Name)
	assert.Equal(t, testHubWDUID, owner.UID)
	require.NotNil(t, owner.Controller)
	assert.True(t, *owner.Controller)
	assert.Nil(t, owner.BlockOwnerDeletion,
		"blocking the owner's deletion needs finalizers access compute is not granted on the hub")

	assert.Equal(t, testNetworkName, stored.Labels[networkingv1alpha.NetworkLabel])
	assert.Equal(t, testLocationName, stored.Labels[networkingv1alpha.LocationLabel])
}

// TestEnsureNetworkBinding_NothingToDeclareYet verifies that a deployment with no
// location, or with no interface, produces no binding at all: an unplaced
// deployment has no location to name, and a binding cannot be created without one.
func TestEnsureNetworkBinding_NothingToDeclareYet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		hubWD *computev1alpha.WorkloadDeployment
	}{
		{
			name:  "no serving location",
			hubWD: testHubDeployment(),
		},
		{
			name: "no network interfaces",
			hubWD: testHubDeployment(withServingLocation(testLocationName), func(wd *computev1alpha.WorkloadDeployment) {
				wd.Spec.Template.Spec.NetworkInterfaces = nil
			}),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			karmadaClient := newKarmadaFakeClient(tc.hubWD)
			r := newTestFederator(newProjectFakeClient(), karmadaClient)

			binding, err := r.ensureNetworkBinding(context.Background(), tc.hubWD)
			require.NoError(t, err)
			assert.Nil(t, binding)

			var bindings networkingv1alpha.NetworkBindingList
			require.NoError(t, karmadaClient.List(context.Background(), &bindings))
			assert.Empty(t, bindings.Items)
		})
	}
}

// TestEnsureNetworkBinding_PreservesNSOOwnedLabels is the regression guard for
// the write loop this design is built to avoid. NSO's presence controller stamps
// the network's UID on the binding, and its garbage collection keys on that
// label. A reconcile that assigned the label map wholesale would strip it every
// pass and put the two controllers in a fight neither one settles.
func TestEnsureNetworkBinding_PreservesNSOOwnedLabels(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	hubWD := testHubDeployment(withServingLocation(testLocationName))
	karmadaClient := newKarmadaFakeClient(hubWD)
	r := newTestFederator(newProjectFakeClient(), karmadaClient)

	_, err := r.ensureNetworkBinding(ctx, hubWD)
	require.NoError(t, err)

	// NSO stamps the network UID, and drops a label compute never wrote so the
	// test also catches the case of compute pruning keys it does not own.
	stored, err := getBinding(t, karmadaClient)
	require.NoError(t, err)
	patch := client.MergeFrom(stored.DeepCopy())
	stored.Labels[networkingv1alpha.NetworkUIDLabel] = "network-uid-from-nso"
	stored.Labels["networking.datumapis.com/some-other-key"] = "keep-me"
	// Also drop one of compute's own labels, so the reconcile below has a reason
	// to write and cannot pass by doing nothing at all.
	delete(stored.Labels, networkingv1alpha.LocationLabel)
	require.NoError(t, karmadaClient.Patch(ctx, stored, patch))

	_, err = r.ensureNetworkBinding(ctx, hubWD)
	require.NoError(t, err)

	after, err := getBinding(t, karmadaClient)
	require.NoError(t, err)
	assert.Equal(t, "network-uid-from-nso", after.Labels[networkingv1alpha.NetworkUIDLabel],
		"NSO's network-uid label must survive a compute reconcile")
	assert.Equal(t, "keep-me", after.Labels["networking.datumapis.com/some-other-key"])
	assert.Equal(t, testLocationName, after.Labels[networkingv1alpha.LocationLabel],
		"compute's own labels are restored")
}

// TestEnsureNetworkBinding_NoWriteWhenSettled verifies the steady state is quiet:
// a binding that already declares the right presence is not rewritten, so the
// binding watch does not feed itself.
func TestEnsureNetworkBinding_NoWriteWhenSettled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	hubWD := testHubDeployment(withServingLocation(testLocationName))
	karmadaClient := newKarmadaFakeClient(hubWD)
	r := newTestFederator(newProjectFakeClient(), karmadaClient)

	_, err := r.ensureNetworkBinding(ctx, hubWD)
	require.NoError(t, err)
	first, err := getBinding(t, karmadaClient)
	require.NoError(t, err)

	_, err = r.ensureNetworkBinding(ctx, hubWD)
	require.NoError(t, err)
	second, err := getBinding(t, karmadaClient)
	require.NoError(t, err)

	assert.Equal(t, first.ResourceVersion, second.ResourceVersion,
		"a settled binding must not be written again")
}

// TestEnsureNetworkBinding_RecreatesOnDivergence covers both ways the declared
// pair can stop matching reality: a user edits the workload's network, and a cell
// serves the deployment from a different location. Both fields are immutable on a
// NetworkBinding, so the old declaration is removed and the next pass makes the
// new one.
func TestEnsureNetworkBinding_RecreatesOnDivergence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		changed      func(*computev1alpha.WorkloadDeployment)
		wantNetwork  string
		wantLocation string
	}{
		{
			name:         "network changed",
			changed:      withInterfaceNetwork("other-network"),
			wantNetwork:  "other-network",
			wantLocation: testLocationName,
		},
		{
			name:         "serving location changed",
			changed:      withServingLocation("ord"),
			wantNetwork:  testNetworkName,
			wantLocation: "ord",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			hubWD := testHubDeployment(withServingLocation(testLocationName))
			karmadaClient := newKarmadaFakeClient(hubWD)
			r := newTestFederator(newProjectFakeClient(), karmadaClient)

			_, err := r.ensureNetworkBinding(ctx, hubWD)
			require.NoError(t, err)

			tc.changed(hubWD)

			// The diverged declaration goes away first, and nothing is returned to
			// report on while it does.
			binding, err := r.ensureNetworkBinding(ctx, hubWD)
			require.NoError(t, err)
			assert.Nil(t, binding)
			_, err = getBinding(t, karmadaClient)
			assert.True(t, apierrors.IsNotFound(err), "diverged binding should be deleted, got %v", err)

			// The next pass declares the new presence.
			_, err = r.ensureNetworkBinding(ctx, hubWD)
			require.NoError(t, err)
			recreated, err := getBinding(t, karmadaClient)
			require.NoError(t, err)

			assert.Equal(t, tc.wantNetwork, recreated.Spec.Network.Name)
			assert.Equal(t, tc.wantLocation, recreated.Spec.Location.Name)
		})
	}
}

// TestEnsureNetworkBinding_LeavesForeignBindingAlone verifies compute refuses to
// rewrite or delete a binding under the same name that it does not own. Deleting
// another consumer's declaration would take away a presence compute never asked
// for.
func TestEnsureNetworkBinding_LeavesForeignBindingAlone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	hubWD := testHubDeployment(withServingLocation(testLocationName))
	foreign := &networkingv1alpha.NetworkBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWDName,
			Namespace: testKarmadaNSStr,
		},
		Spec: networkingv1alpha.NetworkBindingSpec{
			Network:  networkingv1alpha.NetworkRef{Name: "someone-elses-network"},
			Location: networkingv1alpha.LocationReference{Name: testLocationName},
		},
	}
	karmadaClient := newKarmadaFakeClient(hubWD, foreign)
	r := newTestFederator(newProjectFakeClient(), karmadaClient)

	_, err := r.ensureNetworkBinding(ctx, hubWD)
	require.Error(t, err)

	stored, err := getBinding(t, karmadaClient)
	require.NoError(t, err)
	assert.Equal(t, "someone-elses-network", stored.Spec.Network.Name)
}

// TestWorkloadDeploymentFederator_CreatesNetworkBindingOnReconcile verifies the
// binding is produced by an ordinary federation pass once the hub deployment
// carries the location a cell reported.
func TestWorkloadDeploymentFederator_CreatesNetworkBindingOnReconcile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	wd := testWorkloadDeployment(withFinalizer, func(wd *computev1alpha.WorkloadDeployment) {
		wd.Spec.Template.Spec.NetworkInterfaces = []computev1alpha.InstanceNetworkInterface{{
			Name:    "eth0",
			Network: networkingv1alpha.NetworkRef{Name: testNetworkName},
		}}
	})
	projectClient := newProjectFakeClient(testProjectNamespace(), wd)
	// The hub copy already exists carrying the aggregated serving location, which
	// is the only place the location is known.
	karmadaClient := newKarmadaFakeClient(testHubDeployment(withServingLocation(testLocationName)))
	r := newTestFederator(projectClient, karmadaClient)

	_, err := r.Reconcile(ctx, reconcileRequest())
	require.NoError(t, err)

	stored, err := getBinding(t, karmadaClient)
	require.NoError(t, err)
	assert.Equal(t, testNetworkName, stored.Spec.Network.Name)
	assert.Equal(t, testLocationName, stored.Spec.Location.Name)
}

// TestMapNetworkBindingToRequest verifies the binding-to-deployment mapping: the
// deployment name comes from the binding's controller owner, and the project
// namespace and cluster from the hub namespace this controller stamped. A binding
// another consumer wrote maps to nothing.
func TestMapNetworkBindingToRequest(t *testing.T) {
	t.Parallel()

	hubNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testKarmadaNSStr,
			Labels: map[string]string{
				downstreamclient.UpstreamOwnerClusterNameLabel: EncodeClusterName(testCluster),
				downstreamclient.UpstreamOwnerNamespaceLabel:   testProjNS,
			},
		},
	}
	unlabelledNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: testKarmadaNSStr},
	}

	owned := &networkingv1alpha.NetworkBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWDName,
			Namespace: testKarmadaNSStr,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: computev1alpha.GroupVersion.String(),
				Kind:       kindWorkloadDeployment,
				Name:       testWDName,
				UID:        testHubWDUID,
				Controller: func() *bool { b := true; return &b }(),
			}},
		},
	}
	foreign := &networkingv1alpha.NetworkBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "someone-else", Namespace: testKarmadaNSStr},
	}

	tests := []struct {
		name    string
		ns      *corev1.Namespace
		binding *networkingv1alpha.NetworkBinding
		want    []mcreconcile.Request
	}{
		{
			name:    "maps to the declaring deployment",
			ns:      hubNS,
			binding: owned,
			want: []mcreconcile.Request{{
				ClusterName: testCluster,
				Request: ctrl.Request{
					NamespacedName: types.NamespacedName{Namespace: testProjNS, Name: testWDName},
				},
			}},
		},
		{
			name:    "another consumer's binding is not ours",
			ns:      hubNS,
			binding: foreign,
			want:    nil,
		},
		{
			name:    "hub namespace without identity labels is dropped",
			ns:      unlabelledNS,
			binding: owned,
			want:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			karmadaClient := newKarmadaFakeClient(tc.ns)
			r := newTestFederator(newProjectFakeClient(), karmadaClient)
			r.FederationCluster = newFakeCluster(karmadaClient)

			got := r.mapNetworkBindingToRequest(context.Background(), tc.binding)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestNetworkBindingRefusalReporting verifies which of NSO's answers reach the
// deployment's Available condition. Only refusals a person can act on are
// reported; the ordinary not-ready state says nothing about the deployment and
// would otherwise mark every deployment unavailable forever, since nothing marks
// a network context programmed today.
func TestNetworkBindingRefusalReporting(t *testing.T) {
	t.Parallel()

	binding := func(status metav1.ConditionStatus, reason string) *networkingv1alpha.NetworkBinding {
		return &networkingv1alpha.NetworkBinding{
			Status: networkingv1alpha.NetworkBindingStatus{
				Conditions: []metav1.Condition{{
					Type:               networkingv1alpha.NetworkBindingReady,
					Status:             status,
					Reason:             reason,
					Message:            "from NSO",
					LastTransitionTime: metav1.Now(),
				}},
			},
		}
	}

	tests := []struct {
		name       string
		binding    *networkingv1alpha.NetworkBinding
		wantReason string
	}{
		{name: "no binding yet", binding: nil},
		{
			name:       "location not available",
			binding:    binding(metav1.ConditionFalse, networkingv1alpha.NetworkBindingReasonLocationNotAvailable),
			wantReason: networkingv1alpha.NetworkBindingReasonLocationNotAvailable,
		},
		{
			name:       "network not found",
			binding:    binding(metav1.ConditionFalse, networkingv1alpha.NetworkBindingReasonNetworkNotFound),
			wantReason: networkingv1alpha.NetworkBindingReasonNetworkNotFound,
		},
		{
			name:       "project unresolved",
			binding:    binding(metav1.ConditionFalse, networkingv1alpha.NetworkBindingReasonProjectUnresolved),
			wantReason: networkingv1alpha.NetworkBindingReasonProjectUnresolved,
		},
		{
			name:    "context not ready is the ordinary state",
			binding: binding(metav1.ConditionFalse, networkingv1alpha.NetworkBindingReasonNetworkContextNotReady),
		},
		{
			name:    "pending is not an answer yet",
			binding: binding(metav1.ConditionUnknown, networkingv1alpha.NetworkBindingReasonPending),
		},
		{
			name:    "ready",
			binding: binding(metav1.ConditionTrue, networkingv1alpha.NetworkBindingReasonNetworkContextReady),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := networkBindingRefusal(tc.binding)
			if tc.wantReason == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, computev1alpha.WorkloadDeploymentAvailable, got.Type)
			assert.Equal(t, metav1.ConditionFalse, got.Status)
			assert.Equal(t, tc.wantReason, got.Reason)
			assert.Equal(t, "from NSO", got.Message)
		})
	}
}

// TestApplyNetworkBindingRefusal_KeepsAnAvailableDeploymentAvailable verifies a
// refusal never contradicts a deployment whose instances are observed running.
func TestApplyNetworkBindingRefusal_KeepsAnAvailableDeploymentAvailable(t *testing.T) {
	t.Parallel()

	status := &computev1alpha.WorkloadDeploymentStatus{
		Conditions: []metav1.Condition{{
			Type:               computev1alpha.WorkloadDeploymentAvailable,
			Status:             metav1.ConditionTrue,
			Reason:             "InstancesAvailable",
			LastTransitionTime: metav1.Now(),
		}},
	}
	refused := &networkingv1alpha.NetworkBinding{
		Status: networkingv1alpha.NetworkBindingStatus{
			Conditions: []metav1.Condition{{
				Type:               networkingv1alpha.NetworkBindingReady,
				Status:             metav1.ConditionFalse,
				Reason:             networkingv1alpha.NetworkBindingReasonNetworkNotFound,
				LastTransitionTime: metav1.Now(),
			}},
		},
	}

	applyNetworkBindingRefusal(status, refused, 1)

	assert.Equal(t, "InstancesAvailable", status.Conditions[0].Reason)
	assert.Equal(t, metav1.ConditionTrue, status.Conditions[0].Status)
}

// TestNetworkBindingStateDoesNotGateInstances pins the decision that a binding is
// reported on and never acted on. Nothing marks a network context programmed
// today, so a binding sits not-ready indefinitely; gating on it would stop every
// instance from ever starting, and tearing down on it would take running
// instances apart and release their addresses. Instances remain gated on their
// own interface claim holding what it asked for.
func TestNetworkBindingStateDoesNotGateInstances(t *testing.T) {
	t.Parallel()

	claim := &networkingv1alpha.NetworkInterfaceClaim{
		Status: networkingv1alpha.NetworkInterfaceClaimStatus{
			Conditions: []metav1.Condition{
				claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionTrue, "Bound"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimAllocated, metav1.ConditionTrue, "Allocated"),
			},
		},
	}
	assert.True(t, networkInterfaceClaimSatisfied(claim),
		"claim satisfaction is decided by the claim alone")

	notReady := &networkingv1alpha.NetworkBinding{
		Status: networkingv1alpha.NetworkBindingStatus{
			Conditions: []metav1.Condition{{
				Type:               networkingv1alpha.NetworkBindingReady,
				Status:             metav1.ConditionFalse,
				Reason:             networkingv1alpha.NetworkBindingReasonNetworkContextNotReady,
				LastTransitionTime: metav1.Now(),
			}},
		},
	}
	assert.Nil(t, networkBindingRefusal(notReady),
		"a binding waiting on a context must not even be reported, let alone acted on")
	assert.True(t, networkInterfaceClaimSatisfied(claim),
		"binding state is not an input to instance gating")
}
