// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"

	"go.datum.net/compute/internal/controller/instancecontrol"
)

const (
	// holdingTestCurrentUID is the instance filling the slot now, and
	// holdingTestPreviousUID the one it replaced under the same name.
	holdingTestCurrentUID  types.UID = "instance-uid-gen-2"
	holdingTestPreviousUID types.UID = "instance-uid-gen-1"

	holdingTestFinalizer = "test.example.com/hold"
	holdingTestInterface = "nic-previous"
)

// newHoldingTestInstance builds the replacement instance filling slot 0.
func newHoldingTestInstance() *computev1alpha.Instance {
	instance := newClaimTestInstance(claimTestDeployment+"-0", computev1alpha.InstanceNetworkInterface{
		Network: networkingv1alpha.NetworkRef{Name: claimTestNetwork},
		Name:    defaultInterfaceName,
	})
	instance.UID = holdingTestCurrentUID
	return instance
}

// newPreparedClaimControlledBy builds a claim of the slot's name that holds its
// addresses, has its data plane prepared, and is controlled by the given
// object. A nil controller leaves the claim without one.
func newPreparedClaimControlledBy(instance *computev1alpha.Instance, controller *metav1.OwnerReference) *networkingv1alpha.NetworkInterfaceClaim {
	claim := &networkingv1alpha.NetworkInterfaceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: instance.Name + "-eth0", Namespace: claimTestNamespace},
		Status: networkingv1alpha.NetworkInterfaceClaimStatus{
			NetworkInterfaceRef: &networkingv1alpha.LocalNetworkInterfaceRef{Name: holdingTestInterface},
			Addresses: []networkingv1alpha.NetworkInterfaceAddress{
				{Family: networkingv1alpha.IPv4Protocol, Address: claimTestAddressCIDR, Primary: true},
			},
			Conditions: []metav1.Condition{
				claimCondition(networkingv1alpha.NetworkInterfaceClaimBound, metav1.ConditionTrue, "Bound"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimAllocated, metav1.ConditionTrue, "Allocated"),
				claimCondition(networkingv1alpha.NetworkInterfaceClaimPrepared, metav1.ConditionTrue, "Prepared"),
			},
		},
	}
	if controller != nil {
		claim.OwnerReferences = []metav1.OwnerReference{*controller}
	}
	return claim
}

func instanceController(name string, uid types.UID) *metav1.OwnerReference {
	return &metav1.OwnerReference{
		APIVersion: computev1alpha.GroupVersion.String(),
		Kind:       instanceKind,
		Name:       name,
		UID:        uid,
		Controller: new(true),
	}
}

func networkGateHeld(t *testing.T, cl client.Client, instance *computev1alpha.Instance) bool {
	t.Helper()

	var current computev1alpha.Instance
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(instance), &current))
	require.NotNil(t, current.Spec.Controller)
	for _, gate := range current.Spec.Controller.SchedulingGates {
		if gate.Name == instancecontrol.NetworkSchedulingGate.String() {
			return true
		}
	}
	return false
}

// TestNetworkGateHeldOnPreviousInstanceClaim is the regression test for a
// replacement instance opening its gate on the claim the instance it replaced
// still controls. That claim is Prepared, but its interface is deleted when
// garbage collection removes it, and the Pod would stay wired to it.
func TestNetworkGateHeldOnPreviousInstanceClaim(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	deployment := newClaimTestDeployment()
	instance := newHoldingTestInstance()
	stale := newPreparedClaimControlledBy(instance, instanceController(instance.Name, holdingTestPreviousUID))

	cl := fake.NewClientBuilder().
		WithScheme(newClaimTestScheme()).
		WithObjects(deployment, instance, stale).
		WithStatusSubresource(instance).
		Build()

	r := &WorkloadDeploymentReconciler{NetworkingEnabled: true}
	ready, waiting, err := r.reconcileNetworkInterfaceClaims(ctx, cl, deployment, []computev1alpha.Instance{*instance})
	require.NoError(t, err)
	assert.False(t, ready[instance.Name], "a claim another instance controls must not open the gate")
	assert.True(t, waiting)

	_, _, _, _, _, err = r.reconcileInstanceGates(ctx, cl, deployment, []computev1alpha.Instance{*instance}, ready)
	require.NoError(t, err)
	assert.True(t, networkGateHeld(t, cl, instance))

	err = cl.Get(ctx, client.ObjectKeyFromObject(stale), &networkingv1alpha.NetworkInterfaceClaim{})
	assert.True(t, apierrors.IsNotFound(err), "the previous instance's claim is released instead of waiting on garbage collection")

	// The next pass creates the replacement's own claim, which is not prepared yet.
	ready, waiting, err = r.reconcileNetworkInterfaceClaims(ctx, cl, deployment, []computev1alpha.Instance{*instance})
	require.NoError(t, err)
	assert.False(t, ready[instance.Name])
	assert.False(t, waiting, "the instance now waits on its own claim, which the claim watch reports")

	var replacement networkingv1alpha.NetworkInterfaceClaim
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(stale), &replacement))
	owner := metav1.GetControllerOf(&replacement)
	require.NotNil(t, owner)
	assert.Equal(t, holdingTestCurrentUID, owner.UID)
}

// TestNetworkGateHeldOnTerminatingClaim covers a claim that is already being
// deleted. Its interface goes with it, so the instance waits, and nothing is
// written to the claim while it does.
func TestNetworkGateHeldOnTerminatingClaim(t *testing.T) {
	t.Parallel()

	for name, controller := range map[string]*metav1.OwnerReference{
		"controlled by the instance": instanceController(claimTestDeployment+"-0", holdingTestCurrentUID),
		// Nothing maps such a claim back to the deployment when it goes, so the
		// requeue is the only thing that wakes the instance.
		"no controller": nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			deployment := newClaimTestDeployment()
			instance := newHoldingTestInstance()
			terminating := newPreparedClaimControlledBy(instance, controller)
			terminating.Finalizers = []string{holdingTestFinalizer}
			terminating.DeletionTimestamp = new(metav1.Now())

			cl := fake.NewClientBuilder().
				WithScheme(newClaimTestScheme()).
				WithObjects(deployment, instance, terminating).
				WithStatusSubresource(instance).
				Build()

			r := &WorkloadDeploymentReconciler{NetworkingEnabled: true}
			ready, waiting, err := r.reconcileNetworkInterfaceClaims(ctx, cl, deployment, []computev1alpha.Instance{*instance})
			require.NoError(t, err)
			assert.False(t, ready[instance.Name], "a claim being deleted must not open the gate")
			assert.True(t, waiting, "the deployment is requeued while the claim goes away")

			_, _, _, _, _, err = r.reconcileInstanceGates(ctx, cl, deployment, []computev1alpha.Instance{*instance}, ready)
			require.NoError(t, err)
			assert.True(t, networkGateHeld(t, cl, instance))

			var current networkingv1alpha.NetworkInterfaceClaim
			require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(terminating), &current))
			assert.Empty(t, current.Labels, "a terminating claim is left alone")
		})
	}
}

// TestNetworkGateHeldOnForeignClaim covers a claim of the slot's name that
// something else controls: another kind, or an Instance of a different name
// whose derived claim name collides with this one. Either holder may be live,
// so the claim is waited on and never deleted.
func TestNetworkGateHeldOnForeignClaim(t *testing.T) {
	t.Parallel()

	for name, controller := range map[string]*metav1.OwnerReference{
		"another kind": {
			APIVersion: "v1", Kind: "Pod", Name: claimTestDeployment + "-0", UID: "pod-uid", Controller: new(true),
		},
		"instance with a colliding name": instanceController(claimTestDeployment+"-0-eth0", "instance-uid-other"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			deployment := newClaimTestDeployment()
			instance := newHoldingTestInstance()
			foreign := newPreparedClaimControlledBy(instance, controller)

			cl := fake.NewClientBuilder().
				WithScheme(newClaimTestScheme()).
				WithObjects(deployment, instance, foreign).
				Build()

			r := &WorkloadDeploymentReconciler{NetworkingEnabled: true}
			ready, waiting, err := r.reconcileNetworkInterfaceClaims(ctx, cl, deployment, []computev1alpha.Instance{*instance})
			require.NoError(t, err)
			assert.False(t, ready[instance.Name])
			assert.True(t, waiting)
			require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(foreign), &networkingv1alpha.NetworkInterfaceClaim{}),
				"a claim a possibly live holder controls is never deleted")
		})
	}
}

// TestStaleClaimReleaseToleratesPreconditionFailures covers a stale claim that
// changed or went away between the read and the delete, as a lagging cache
// produces. Neither is an error, and the gate stays held.
func TestStaleClaimReleaseToleratesPreconditionFailures(t *testing.T) {
	t.Parallel()

	for name, deleteErr := range map[string]error{
		"conflict":  apierrors.NewConflict(networkingv1alpha.GroupVersion.WithResource("networkinterfaceclaims").GroupResource(), "claim", nil),
		"not found": apierrors.NewNotFound(networkingv1alpha.GroupVersion.WithResource("networkinterfaceclaims").GroupResource(), "claim"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			deployment := newClaimTestDeployment()
			instance := newHoldingTestInstance()
			stale := newPreparedClaimControlledBy(instance, instanceController(instance.Name, holdingTestPreviousUID))

			var deletes int
			cl := fake.NewClientBuilder().
				WithScheme(newClaimTestScheme()).
				WithObjects(deployment, instance, stale).
				WithInterceptorFuncs(interceptor.Funcs{
					Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
						deletes++
						return deleteErr
					},
				}).
				Build()

			r := &WorkloadDeploymentReconciler{NetworkingEnabled: true}
			ready, waiting, err := r.reconcileNetworkInterfaceClaims(ctx, cl, deployment, []computev1alpha.Instance{*instance})
			require.NoError(t, err)
			assert.Equal(t, 1, deletes)
			assert.False(t, ready[instance.Name])
			assert.True(t, waiting)
		})
	}
}

// TestClaimWatchMapsPreviousInstanceClaimToReplacement pins the wake-up the
// replacement relies on: a claim the previous instance controls maps, by the
// slot's name, to the deployment of the instance filling the slot now.
func TestClaimWatchMapsPreviousInstanceClaimToReplacement(t *testing.T) {
	t.Parallel()

	instance := newHoldingTestInstance()
	stale := newPreparedClaimControlledBy(instance, instanceController(instance.Name, holdingTestPreviousUID))

	cl := fake.NewClientBuilder().
		WithScheme(newClaimTestScheme()).
		WithObjects(instance).
		Build()

	requests := enqueueWorkloadDeploymentForClaim(context.Background(), cl, "", stale)
	require.Len(t, requests, 1)
	assert.Equal(t, claimTestDeployment, requests[0].Name)
	assert.Equal(t, claimTestNamespace, requests[0].Namespace)
}

// TestNetworkGateReleasedOnHeldClaim keeps the paths that already worked: a
// prepared claim the instance controls, or one nothing controls, opens the gate.
func TestNetworkGateReleasedOnHeldClaim(t *testing.T) {
	t.Parallel()

	for name, controller := range map[string]*metav1.OwnerReference{
		"controlled by the instance": instanceController(claimTestDeployment+"-0", holdingTestCurrentUID),
		"no controller":              nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			deployment := newClaimTestDeployment()
			instance := newHoldingTestInstance()
			claim := newPreparedClaimControlledBy(instance, controller)

			cl := fake.NewClientBuilder().
				WithScheme(newClaimTestScheme()).
				WithObjects(deployment, instance, claim).
				Build()

			r := &WorkloadDeploymentReconciler{NetworkingEnabled: true}
			ready, _, err := r.reconcileNetworkInterfaceClaims(context.Background(), cl, deployment,
				[]computev1alpha.Instance{*instance})
			require.NoError(t, err)
			assert.True(t, ready[instance.Name])
		})
	}
}

// TestInstanceIgnoresClaimItDoesNotHold verifies a replacement instance neither
// publishes the previous claim's interface nor reports its rejection.
func TestInstanceIgnoresClaimItDoesNotHold(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*networkingv1alpha.NetworkInterfaceClaim){
		"previous instance": func(claim *networkingv1alpha.NetworkInterfaceClaim) {
			claim.OwnerReferences = []metav1.OwnerReference{*instanceController(claimTestDeployment+"-0", holdingTestPreviousUID)}
		},
		"terminating": func(claim *networkingv1alpha.NetworkInterfaceClaim) {
			claim.Finalizers = []string{holdingTestFinalizer}
			claim.DeletionTimestamp = new(metav1.Now())
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			instance := newHoldingTestInstance()
			claim := newPreparedClaimControlledBy(instance, instanceController(instance.Name, holdingTestCurrentUID))
			claim.Status.Conditions = []metav1.Condition{{
				Type:    networkingv1alpha.NetworkInterfaceClaimAllocated,
				Status:  metav1.ConditionFalse,
				Reason:  "AddressPoolExhausted",
				Message: "no addresses left",
			}}
			mutate(claim)

			cl := fake.NewClientBuilder().
				WithScheme(newClaimTestScheme()).
				WithObjects(instance, claim).
				Build()

			r := &InstanceReconciler{NetworkingEnabled: true}

			_, err := r.reconcileNetworkInterfaceStatus(ctx, cl, instance)
			require.NoError(t, err)
			require.Len(t, instance.Status.NetworkInterfaces, 1)
			published := instance.Status.NetworkInterfaces[0]
			assert.Equal(t, defaultInterfaceName, published.Name)
			assert.Nil(t, published.NetworkInterfaceRef, "the previous interface must not reach a provider")
			assert.Empty(t, published.Addresses)

			failed, _, err := r.checkForNetworkCreationFailure(ctx, cl, instance)
			require.NoError(t, err)
			assert.False(t, failed, "another claim's rejection says nothing about this instance")
		})
	}
}
