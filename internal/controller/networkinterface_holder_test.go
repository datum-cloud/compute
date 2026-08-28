// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const holderTestInterface = "holder-test-eth0"

// newHolderTestInstance builds an instance already publishing one bound
// interface, which is the shape every instance reaches once its claim binds.
func newHolderTestInstance(available *metav1.Condition) *computev1alpha.Instance {
	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "holder-test-instance",
			Namespace: claimTestNamespace,
		},
		Status: computev1alpha.InstanceStatus{
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterfaceStatus{{
				Name: defaultInterfaceName,
				NetworkInterfaceRef: &networkingv1alpha.LocalNetworkInterfaceRef{
					Name: holderTestInterface,
				},
			}},
		},
	}
	if available != nil {
		apimeta.SetStatusCondition(&instance.Status.Conditions, *available)
	}
	return instance
}

func newHolderTestInterface() *networkingv1alpha.NetworkInterface {
	return &networkingv1alpha.NetworkInterface{
		ObjectMeta: metav1.ObjectMeta{
			Name:       holderTestInterface,
			Namespace:  claimTestNamespace,
			Generation: 4,
		},
	}
}

func holderTestClient(objects ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(newClaimTestScheme()).
		WithObjects(objects...).
		WithStatusSubresource(&networkingv1alpha.NetworkInterface{}, &computev1alpha.Instance{}).
		Build()
}

func getHolderCondition(t *testing.T, cl client.Client) *metav1.Condition {
	t.Helper()
	var networkInterface networkingv1alpha.NetworkInterface
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{
		Namespace: claimTestNamespace,
		Name:      holderTestInterface,
	}, &networkInterface))
	return apimeta.FindStatusCondition(networkInterface.Status.Conditions, networkingv1alpha.NetworkInterfaceHolderAvailable)
}

// TestHolderAvailableCondition covers the translation from the instance's own
// Available condition, including the states that must not read as healthy.
func TestHolderAvailableCondition(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		available   *metav1.Condition
		wantStatus  metav1.ConditionStatus
		wantReason  string
		wantMessage string
	}{
		{
			name: "available instance vouches for its interfaces",
			available: &metav1.Condition{
				Type:   computev1alpha.InstanceAvailable,
				Status: metav1.ConditionTrue,
				Reason: computev1alpha.InstanceAvailableReasonAvailable,
			},
			wantStatus:  metav1.ConditionTrue,
			wantReason:  computev1alpha.InstanceAvailableReasonAvailable,
			wantMessage: msgInstanceAvailable,
		},
		{
			name: "stopped instance carries its own reason",
			available: &metav1.Condition{
				Type:    computev1alpha.InstanceAvailable,
				Status:  metav1.ConditionFalse,
				Reason:  computev1alpha.InstanceAvailableReasonStopped,
				Message: "Instance is stopped",
			},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  computev1alpha.InstanceAvailableReasonStopped,
			wantMessage: "Instance is stopped",
		},
		{
			name: "unknown availability does not read as healthy",
			available: &metav1.Condition{
				Type:    computev1alpha.InstanceAvailable,
				Status:  metav1.ConditionUnknown,
				Reason:  computev1alpha.InstanceReadyReasonImageUnavailable,
				Message: "Image could not be pulled",
			},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  computev1alpha.InstanceReadyReasonImageUnavailable,
			wantMessage: "Image could not be pulled",
		},
		{
			name:        "instance that has reported nothing is distinguishable",
			available:   nil,
			wantStatus:  metav1.ConditionFalse,
			wantReason:  holderReasonNotReported,
			wantMessage: msgHolderNotReported,
		},
		{
			name: "unavailable without a message still says something",
			available: &metav1.Condition{
				Type:   computev1alpha.InstanceAvailable,
				Status: metav1.ConditionFalse,
				Reason: computev1alpha.InstanceAvailableReasonStarting,
			},
			wantStatus:  metav1.ConditionFalse,
			wantReason:  computev1alpha.InstanceAvailableReasonStarting,
			wantMessage: `Instance "holder-test-instance" is not available`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			condition := holderAvailableCondition(newHolderTestInstance(tc.available))

			assert.Equal(t, networkingv1alpha.NetworkInterfaceHolderAvailable, condition.Type)
			assert.Equal(t, tc.wantStatus, condition.Status)
			assert.Equal(t, tc.wantReason, condition.Reason)
			assert.Equal(t, tc.wantMessage, condition.Message)
		})
	}
}

// TestReconcileHolderAvailability_BecomesAvailable covers the transition an
// existing interface must pick up on an ordinary reconcile pass.
func TestReconcileHolderAvailability_BecomesAvailable(t *testing.T) {
	t.Parallel()

	instance := newHolderTestInstance(&metav1.Condition{
		Type:   computev1alpha.InstanceAvailable,
		Status: metav1.ConditionTrue,
		Reason: computev1alpha.InstanceAvailableReasonAvailable,
	})
	cl := holderTestClient(instance, newHolderTestInterface())

	r := &InstanceReconciler{NetworkingEnabled: true}
	require.NoError(t, r.reconcileHolderAvailability(context.Background(), cl, instance))

	condition := getHolderCondition(t, cl)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionTrue, condition.Status)
	assert.Equal(t, computev1alpha.InstanceAvailableReasonAvailable, condition.Reason)
	assert.Equal(t, int64(4), condition.ObservedGeneration,
		"the interface's generation, not the instance's")
}

// TestReconcileHolderAvailability_LeavesAvailable covers the drain path: an
// interface already vouched for must be told when the holder stops serving.
func TestReconcileHolderAvailability_LeavesAvailable(t *testing.T) {
	t.Parallel()

	instance := newHolderTestInstance(&metav1.Condition{
		Type:   computev1alpha.InstanceAvailable,
		Status: metav1.ConditionTrue,
		Reason: computev1alpha.InstanceAvailableReasonAvailable,
	})
	cl := holderTestClient(instance, newHolderTestInterface())

	r := &InstanceReconciler{NetworkingEnabled: true}
	require.NoError(t, r.reconcileHolderAvailability(context.Background(), cl, instance))
	require.Equal(t, metav1.ConditionTrue, getHolderCondition(t, cl).Status)

	apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
		Type:    computev1alpha.InstanceAvailable,
		Status:  metav1.ConditionFalse,
		Reason:  computev1alpha.InstanceAvailableReasonStopping,
		Message: "Instance is stopping",
	})
	require.NoError(t, r.reconcileHolderAvailability(context.Background(), cl, instance))

	condition := getHolderCondition(t, cl)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, computev1alpha.InstanceAvailableReasonStopping, condition.Reason)
	assert.Equal(t, "Instance is stopping", condition.Message)
}

// TestReconcileHolderAvailability_Stable covers convergence: a pass that
// changes nothing must not write, so a steady fleet produces no API traffic and
// no transition timestamp churn.
func TestReconcileHolderAvailability_Stable(t *testing.T) {
	t.Parallel()

	instance := newHolderTestInstance(&metav1.Condition{
		Type:   computev1alpha.InstanceAvailable,
		Status: metav1.ConditionTrue,
		Reason: computev1alpha.InstanceAvailableReasonAvailable,
	})
	cl := holderTestClient(instance, newHolderTestInterface())

	r := &InstanceReconciler{NetworkingEnabled: true}
	require.NoError(t, r.reconcileHolderAvailability(context.Background(), cl, instance))

	var first networkingv1alpha.NetworkInterface
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{
		Namespace: claimTestNamespace, Name: holderTestInterface,
	}, &first))

	require.NoError(t, r.reconcileHolderAvailability(context.Background(), cl, instance))

	var second networkingv1alpha.NetworkInterface
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{
		Namespace: claimTestNamespace, Name: holderTestInterface,
	}, &second))

	assert.Equal(t, first.ResourceVersion, second.ResourceVersion,
		"a no-op pass must not write the interface")
	assert.Equal(t, first.Status.Conditions, second.Status.Conditions)
}

// TestReconcileHolderAvailability_NoInterface covers the two ways there is
// nothing to write to, neither of which is an error.
func TestReconcileHolderAvailability_NoInterface(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		instance func(*computev1alpha.Instance)
		objects  []client.Object
	}{
		{
			name: "interface object does not exist",
		},
		{
			name: "status entry carries no interface reference",
			instance: func(i *computev1alpha.Instance) {
				i.Status.NetworkInterfaces[0].NetworkInterfaceRef = nil
			},
			objects: []client.Object{newHolderTestInterface()},
		},
		{
			name: "instance publishes no interfaces at all",
			instance: func(i *computev1alpha.Instance) {
				i.Status.NetworkInterfaces = nil
			},
			objects: []client.Object{newHolderTestInterface()},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			instance := newHolderTestInstance(&metav1.Condition{
				Type:   computev1alpha.InstanceAvailable,
				Status: metav1.ConditionTrue,
				Reason: computev1alpha.InstanceAvailableReasonAvailable,
			})
			if tc.instance != nil {
				tc.instance(instance)
			}

			cl := holderTestClient(append([]client.Object{instance}, tc.objects...)...)

			r := &InstanceReconciler{NetworkingEnabled: true}
			assert.NoError(t, r.reconcileHolderAvailability(context.Background(), cl, instance))

			if len(tc.objects) > 0 {
				assert.Nil(t, getHolderCondition(t, cl))
			}
		})
	}
}

// TestReconcileHolderAvailability_NetworkingDisabled covers a cell without the
// networking CRDs, where the interface kind is not even served.
func TestReconcileHolderAvailability_NetworkingDisabled(t *testing.T) {
	t.Parallel()

	instance := newHolderTestInstance(&metav1.Condition{
		Type:   computev1alpha.InstanceAvailable,
		Status: metav1.ConditionTrue,
		Reason: computev1alpha.InstanceAvailableReasonAvailable,
	})
	cl := holderTestClient(instance, newHolderTestInterface())

	r := &InstanceReconciler{NetworkingEnabled: false}
	require.NoError(t, r.reconcileHolderAvailability(context.Background(), cl, instance))

	assert.Nil(t, getHolderCondition(t, cl))
}

// TestHolderAvailableCondition_Terminating covers the case the instance's own
// Available condition cannot express: nothing clears it on the way out, so a
// terminating instance still reports itself available.
func TestHolderAvailableCondition_Terminating(t *testing.T) {
	t.Parallel()

	instance := newHolderTestInstance(&metav1.Condition{
		Type:   computev1alpha.InstanceAvailable,
		Status: metav1.ConditionTrue,
		Reason: computev1alpha.InstanceAvailableReasonAvailable,
	})
	deleting := metav1.Now()
	instance.DeletionTimestamp = &deleting

	condition := holderAvailableCondition(instance)

	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, holderReasonTerminating, condition.Reason)
	assert.Equal(t, msgHolderTerminating, condition.Message)
}

// TestReconcileDeletion_Drains covers the drain path that carries every
// scale-down, rolling update, and redeploy: a terminating instance must stop
// being routed to before its cleanup runs.
func TestReconcileDeletion_Drains(t *testing.T) {
	t.Parallel()

	instance := newHolderTestInstance(&metav1.Condition{
		Type:   computev1alpha.InstanceAvailable,
		Status: metav1.ConditionTrue,
		Reason: computev1alpha.InstanceAvailableReasonAvailable,
	})

	networkInterface := newHolderTestInterface()
	apimeta.SetStatusCondition(&networkInterface.Status.Conditions, metav1.Condition{
		Type:   networkingv1alpha.NetworkInterfaceHolderAvailable,
		Status: metav1.ConditionTrue,
		Reason: computev1alpha.InstanceAvailableReasonAvailable,
	})

	deleting := metav1.Now()
	instance.DeletionTimestamp = &deleting
	instance.Finalizers = []string{instanceQuotaFinalizer}

	cl := holderTestClient(instance, networkInterface)

	r := &InstanceReconciler{NetworkingEnabled: true}
	require.NoError(t, r.reconcileDeletion(context.Background(), cl, "test-cluster", instance))

	condition := getHolderCondition(t, cl)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, holderReasonTerminating, condition.Reason)
}

// TestReconcileDeletion_DrainFailureDoesNotWedge covers the ordering rule: an
// instance that cannot reach its interfaces must still finish deleting, or a
// transient API error strands it in Terminating forever.
func TestReconcileDeletion_DrainFailureDoesNotWedge(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		client  func(client.Client) client.Client
		objects []client.Object
	}{
		{
			name:    "interface is already gone",
			objects: nil,
		},
		{
			name:    "the status write fails",
			objects: []client.Object{newHolderTestInterface()},
			client: func(cl client.Client) client.Client {
				return &holderFailingStatusClient{Client: cl}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			instance := newHolderTestInstance(&metav1.Condition{
				Type:   computev1alpha.InstanceAvailable,
				Status: metav1.ConditionTrue,
				Reason: computev1alpha.InstanceAvailableReasonAvailable,
			})
			deleting := metav1.Now()
			instance.DeletionTimestamp = &deleting
			instance.Finalizers = []string{instanceQuotaFinalizer}

			cl := holderTestClient(append([]client.Object{instance}, tc.objects...)...)
			if tc.client != nil {
				cl = tc.client(cl)
			}

			r := &InstanceReconciler{NetworkingEnabled: true}
			require.NoError(t, r.reconcileDeletion(context.Background(), cl, "test-cluster", instance),
				"a drain failure must not block deletion")

			assert.NotContains(t, instance.Finalizers, instanceQuotaFinalizer,
				"the finalizer must still be released")
		})
	}
}

// TestReconcileHolderAvailability_StaleTrueNotInherited covers a reclaimPolicy
// Retain interface outliving the instance that held it: the next instance in
// the slot must not begin life looking healthy on a predecessor's condition.
func TestReconcileHolderAvailability_StaleTrueNotInherited(t *testing.T) {
	t.Parallel()

	retained := newHolderTestInterface()
	apimeta.SetStatusCondition(&retained.Status.Conditions, metav1.Condition{
		Type:    networkingv1alpha.NetworkInterfaceHolderAvailable,
		Status:  metav1.ConditionTrue,
		Reason:  computev1alpha.InstanceAvailableReasonAvailable,
		Message: msgInstanceAvailable,
	})

	// The successor has rebound the interface but has not started serving, which
	// on a fresh instance means no Available condition at all.
	successor := newHolderTestInstance(nil)
	successor.Name = "holder-test-instance-successor"

	cl := holderTestClient(successor, retained)

	r := &InstanceReconciler{NetworkingEnabled: true}
	require.NoError(t, r.reconcileHolderAvailability(context.Background(), cl, successor))

	condition := getHolderCondition(t, cl)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, holderReasonNotReported, condition.Reason)
}

// holderFailingStatusClient fails every status write, standing in for an
// interface the API server will not let the instance update on its way out.
type holderFailingStatusClient struct {
	client.Client
}

func (c *holderFailingStatusClient) Status() client.SubResourceWriter {
	return &holderFailingStatusWriter{SubResourceWriter: c.Client.Status()}
}

type holderFailingStatusWriter struct {
	client.SubResourceWriter
}

func (w *holderFailingStatusWriter) Update(_ context.Context, _ client.Object, _ ...client.SubResourceUpdateOption) error {
	return errors.New("status update refused")
}
