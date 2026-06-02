// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

const (
	// wdControllerTestName / wdControllerTestNS / wdControllerTestUID are shared
	// fixtures for the WorkloadDeployment controller unit tests.
	wdControllerTestName = "test-wd"
	wdControllerTestNS   = "default"
	wdControllerTestUID  = "wd-uid-test"

	// wdControllerTestCityCode is the shared CityCode fixture for
	// WorkloadDeployment controller tests.
	wdControllerTestCityCode = "DFW"

	// wdControllerTestWorkload is the shared WorkloadRef fixture.
	wdControllerTestWorkload = "test-workload"

	// wdTestReasonProgrammed / wdTestReasonReady are condition Reason fixtures
	// matching what the infra provider writes on Instances.
	wdTestReasonProgrammed = "Programmed"
	wdTestReasonReady      = "Ready"
)

// wdControllerTestDeployment builds a WorkloadDeployment fixture shaped like a
// cell-local deployment after Karmada propagation: city code, placement, and a
// minimal instance template so ComputeHash produces a stable hash.
func wdControllerTestDeployment(minReplicas int32) *computev1alpha.WorkloadDeployment {
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wdControllerTestName,
			Namespace: wdControllerTestNS,
			UID:       wdControllerTestUID,
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode:      wdControllerTestCityCode,
			PlacementName: testDefaultPlacement,
			WorkloadRef:   computev1alpha.WorkloadReference{Name: wdControllerTestWorkload},
			ScaleSettings: computev1alpha.HorizontalScaleSettings{
				MinReplicas: minReplicas,
				// Always present in production: the API server defaults the policy
				// via kubebuilder, and the instance-control strategy emits no
				// create/wait actions without it.
				InstanceManagementPolicy: computev1alpha.OrderedReadyInstanceManagementPolicyType,
			},
			Template: computev1alpha.InstanceTemplateSpec{
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{},
					},
				},
			},
		},
	}
}

// wdControllerTestInstance builds an Instance fixture labeled the way the
// instance control strategy creates them (workload-deployment-uid label set).
func wdControllerTestInstance(name string) computev1alpha.Instance {
	return computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: wdControllerTestNS,
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: wdControllerTestUID,
			},
		},
	}
}

// TestReconcileInstanceGates_NilController_DoesNotPanic is a regression test for
// the case where an Instance has Programmed=True but Status.Controller is nil.
//
// Background: Status.Controller is a nilable pointer that the infra provider
// populates independently of setting the Programmed condition. Before the guard
// was added, reconcileInstanceGates would dereference Status.Controller while
// counting currentReplicas, causing a nil pointer panic that aborted the
// reconcile loop and froze WorkloadDeployment status.
//
// This test verifies that:
//  1. The reconcile does not panic when Status.Controller is nil.
//  2. Only instances with a non-nil Status.Controller whose ObservedTemplateHash
//     matches the deployment's current template hash are counted as current.
func TestReconcileInstanceGates_NilController_DoesNotPanic(t *testing.T) {
	t.Parallel()

	deployment := wdControllerTestDeployment(2)
	templateHash := instancecontrol.ComputeHash(deployment.Spec.Template)

	// Instance A: Programmed=True but Status.Controller is nil (the panic case).
	// This instance must NOT be counted as current and must NOT cause a panic.
	instanceNilController := wdControllerTestInstance("instance-nil-controller")
	instanceNilController.Status = computev1alpha.InstanceStatus{
		Conditions: []metav1.Condition{
			{
				Type:               computev1alpha.InstanceProgrammed,
				Status:             metav1.ConditionTrue,
				Reason:             wdTestReasonProgrammed,
				LastTransitionTime: metav1.Now(),
			},
		},
		// Status.Controller intentionally nil — this is the regression scenario.
		Controller: nil,
	}

	// Instance B: Programmed=True with Status.Controller populated and matching
	// hash. This instance MUST be counted as current (currentReplicas == 1).
	instanceWithController := wdControllerTestInstance("instance-with-controller")
	instanceWithController.Status = computev1alpha.InstanceStatus{
		Conditions: []metav1.Condition{
			{
				Type:               computev1alpha.InstanceProgrammed,
				Status:             metav1.ConditionTrue,
				Reason:             wdTestReasonProgrammed,
				LastTransitionTime: metav1.Now(),
			},
		},
		Controller: &computev1alpha.InstanceControllerStatus{
			ObservedTemplateHash: templateHash,
		},
	}

	// Instance C: Ready=True (contributes to readyReplicas).
	instanceReady := wdControllerTestInstance("instance-ready")
	instanceReady.Status = computev1alpha.InstanceStatus{
		Conditions: []metav1.Condition{
			{
				Type:               computev1alpha.InstanceReady,
				Status:             metav1.ConditionTrue,
				Reason:             wdTestReasonReady,
				LastTransitionTime: metav1.Now(),
			},
		},
		Controller: &computev1alpha.InstanceControllerStatus{
			ObservedTemplateHash: templateHash,
		},
	}

	instances := []computev1alpha.Instance{
		instanceNilController,
		instanceWithController,
		instanceReady,
	}

	// Use a fake client. networkReady=false avoids the gate-patch path that
	// would call CreateOrPatch, so the client is not exercised here.
	cl := newProjectFakeClient()
	r := &WorkloadDeploymentReconciler{}

	// The call must not panic — that is the primary regression assertion.
	currentReplicas, _, readyReplicas, quotaBlockedReplicas, referencedDataBlockedReplicas, err := r.reconcileInstanceGates(
		context.Background(),
		cl,
		deployment,
		instances,
		false, // networkReady=false: skip gate-patch path
	)

	require.NoError(t, err)

	// Only instanceWithController has Programmed=True AND a non-nil
	// Status.Controller with a matching hash — the nil-Controller instance must
	// not be counted. instanceReady also has a matching hash but no Programmed
	// condition, so it also does not increment currentReplicas.
	assert.Equal(t, 1, currentReplicas,
		"only the instance with a populated, matching Status.Controller counts as current; "+
			"the nil-Controller instance must not be counted (Status.Controller nil regression guard)")

	assert.Equal(t, 1, readyReplicas, "instanceReady must be counted as ready")
	assert.Equal(t, 0, quotaBlockedReplicas)
	assert.Equal(t, 0, referencedDataBlockedReplicas)
}

// TestReconcileInstanceGates_NilSpecController_DoesNotPanic is a regression test
// for a nil-deref in reconcileInstanceGates: Spec.Controller is a nilable
// pointer, and the network gate-clearing path dereferenced
// instance.Spec.Controller.SchedulingGates without a nil guard. When
// networkReady is true and an instance has no controller spec, the unguarded
// deref panicked the reconcile. This must not panic.
func TestReconcileInstanceGates_NilSpecController_DoesNotPanic(t *testing.T) {
	t.Parallel()

	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: wdControllerTestName, Namespace: wdControllerTestNS, UID: wdControllerTestUID},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode:      wdControllerTestCityCode,
			PlacementName: testDefaultPlacement,
			WorkloadRef:   computev1alpha.WorkloadReference{Name: wdControllerTestWorkload},
			ScaleSettings: computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
			Template:      computev1alpha.InstanceTemplateSpec{},
		},
	}

	// Spec.Controller intentionally nil — the network gate-clearing path runs
	// (networkReady=true) and must skip this instance instead of panicking.
	instanceNilSpecController := computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "instance-nil-spec-controller", Namespace: wdControllerTestNS},
	}

	cl := newProjectFakeClient()
	r := &WorkloadDeploymentReconciler{}

	require.NotPanics(t, func() {
		_, _, _, _, _, err := r.reconcileInstanceGates(
			context.Background(),
			cl,
			deployment,
			[]computev1alpha.Instance{instanceNilSpecController},
			true, // networkReady=true exercises the Spec.Controller deref path
		)
		require.NoError(t, err)
	})
}

// TestReconcileInstanceGates_ReplicaCounting verifies how instances are bucketed
// into the replica counters:
//
//   - updatedReplicas: ObservedTemplateHash matches the desired template hash,
//     regardless of readiness — a stale hash must not count.
//   - currentReplicas: the Programmed=True subset of updated instances.
//   - readyReplicas: Ready=True regardless of revision.
//   - quotaBlockedReplicas: QuotaGranted=False.
func TestReconcileInstanceGates_ReplicaCounting(t *testing.T) {
	t.Parallel()

	deployment := wdControllerTestDeployment(4)
	templateHash := instancecontrol.ComputeHash(deployment.Spec.Template)

	// Updated + Programmed + Ready: counts toward updated, current, and ready.
	instanceUpdatedReady := wdControllerTestInstance("instance-updated-ready")
	instanceUpdatedReady.Status = computev1alpha.InstanceStatus{
		Conditions: []metav1.Condition{
			{Type: computev1alpha.InstanceProgrammed, Status: metav1.ConditionTrue, Reason: wdTestReasonProgrammed, LastTransitionTime: metav1.Now()},
			{Type: computev1alpha.InstanceReady, Status: metav1.ConditionTrue, Reason: wdTestReasonReady, LastTransitionTime: metav1.Now()},
		},
		Controller: &computev1alpha.InstanceControllerStatus{ObservedTemplateHash: templateHash},
	}

	// Stale revision but Programmed and Ready: counts toward ready only — a
	// rolling update must surface UpdatedReplicas < Replicas.
	instanceStale := wdControllerTestInstance("instance-stale")
	instanceStale.Status = computev1alpha.InstanceStatus{
		Conditions: []metav1.Condition{
			{Type: computev1alpha.InstanceProgrammed, Status: metav1.ConditionTrue, Reason: wdTestReasonProgrammed, LastTransitionTime: metav1.Now()},
			{Type: computev1alpha.InstanceReady, Status: metav1.ConditionTrue, Reason: wdTestReasonReady, LastTransitionTime: metav1.Now()},
		},
		Controller: &computev1alpha.InstanceControllerStatus{ObservedTemplateHash: "stale-hash"},
	}

	// Updated but not yet Programmed: counts toward updated only.
	instanceUpdatedPending := wdControllerTestInstance("instance-updated-pending")
	instanceUpdatedPending.Status = computev1alpha.InstanceStatus{
		Controller: &computev1alpha.InstanceControllerStatus{ObservedTemplateHash: templateHash},
	}

	// Quota-blocked: QuotaGranted=False as the instance quota controller writes it.
	instanceQuotaBlocked := wdControllerTestInstance("instance-quota-blocked")
	instanceQuotaBlocked.Status = computev1alpha.InstanceStatus{
		Conditions: []metav1.Condition{
			{
				Type:               computev1alpha.InstanceQuotaGranted,
				Status:             metav1.ConditionFalse,
				Reason:             computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
				Message:            "quota exceeded",
				LastTransitionTime: metav1.Now(),
			},
		},
	}

	cl := newProjectFakeClient()
	r := &WorkloadDeploymentReconciler{}

	currentReplicas, updatedReplicas, readyReplicas, quotaBlockedReplicas, _, err := r.reconcileInstanceGates(
		context.Background(),
		cl,
		deployment,
		[]computev1alpha.Instance{instanceUpdatedReady, instanceStale, instanceUpdatedPending, instanceQuotaBlocked},
		false,
	)
	require.NoError(t, err)

	assert.Equal(t, 2, updatedReplicas, "matching-hash instances count as updated; the stale-hash instance must not")
	assert.Equal(t, 1, currentReplicas, "only updated AND Programmed instances count as current")
	assert.Equal(t, 2, readyReplicas, "Ready=True counts regardless of revision")
	assert.Equal(t, 1, quotaBlockedReplicas, "QuotaGranted=False counts as quota-blocked")
}

// TestReconcileInstanceGates_ClearsNetworkSchedulingGate verifies the network
// gate-clearing path: once networking is ready, the Network scheduling gate is
// removed from gated instances while unrelated gates are preserved. When
// networking is not ready, gates are left untouched.
func TestReconcileInstanceGates_ClearsNetworkSchedulingGate(t *testing.T) {
	t.Parallel()

	deployment := wdControllerTestDeployment(1)

	newGatedInstance := func() *computev1alpha.Instance {
		instance := wdControllerTestInstance("instance-gated")
		// Gate order matches the stateful instance control strategy: Network
		// prepended ahead of Quota.
		instance.Spec.Controller = &computev1alpha.InstanceController{
			SchedulingGates: []computev1alpha.SchedulingGate{
				{Name: instancecontrol.NetworkSchedulingGate.String()},
				{Name: instancecontrol.QuotaSchedulingGate.String()},
			},
		}
		return &instance
	}

	t.Run("network ready removes only the Network gate", func(t *testing.T) {
		t.Parallel()

		instance := newGatedInstance()
		cl := newProjectFakeClient(instance)
		r := &WorkloadDeploymentReconciler{}

		_, _, _, _, _, err := r.reconcileInstanceGates(
			context.Background(),
			cl,
			deployment,
			[]computev1alpha.Instance{*instance},
			true,
		)
		require.NoError(t, err)

		var updated computev1alpha.Instance
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(instance), &updated))
		require.NotNil(t, updated.Spec.Controller)
		require.Len(t, updated.Spec.Controller.SchedulingGates, 1,
			"the Network gate must be removed and the Quota gate preserved")
		assert.Equal(t, instancecontrol.QuotaSchedulingGate.String(), updated.Spec.Controller.SchedulingGates[0].Name)
	})

	t.Run("network not ready leaves gates untouched", func(t *testing.T) {
		t.Parallel()

		instance := newGatedInstance()
		cl := newProjectFakeClient(instance)
		r := &WorkloadDeploymentReconciler{}

		_, _, _, _, _, err := r.reconcileInstanceGates(
			context.Background(),
			cl,
			deployment,
			[]computev1alpha.Instance{*instance},
			false,
		)
		require.NoError(t, err)

		var updated computev1alpha.Instance
		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(instance), &updated))
		require.NotNil(t, updated.Spec.Controller)
		assert.Len(t, updated.Spec.Controller.SchedulingGates, 2,
			"gates must not be cleared while networking is still provisioning")
	})
}

// newTestWDReconciler builds a WorkloadDeploymentReconciler wired to a fake
// project cluster with the controller finalizer pre-registered, mirroring
// SetupWithManager. Networking is disabled so Reconcile treats the network as
// immediately ready without touching networking CRDs.
func newTestWDReconciler(projectClient client.Client) *WorkloadDeploymentReconciler {
	r := &WorkloadDeploymentReconciler{
		mgr:               newFakeMCManager(testCluster, newFakeCluster(projectClient)),
		NetworkingEnabled: false,
	}
	feds := finalizer.NewFinalizers()
	if err := feds.Register(workloadControllerFinalizer, r); err != nil {
		panic("failed to register test finalizer: " + err.Error())
	}
	r.finalizers = feds
	return r
}

// TestWorkloadDeploymentReconcile_FinalizerAddRequeues verifies the first
// reconcile of a brand-new WorkloadDeployment: the finalizer is added and the
// reconciler requeues explicitly, since the metadata-only finalizer Update may
// be filtered by event predicates or handlers and would otherwise strand the
// deployment unreconciled.
func TestWorkloadDeploymentReconcile_FinalizerAddRequeues(t *testing.T) {
	t.Parallel()

	deployment := wdControllerTestDeployment(1) // no finalizer yet
	cl := newProjectFakeClient(deployment)
	r := newTestWDReconciler(cl)

	req := mcreconcile.Request{
		ClusterName: testCluster,
		Request: ctrl.Request{
			NamespacedName: types.NamespacedName{Name: wdControllerTestName, Namespace: wdControllerTestNS},
		},
	}

	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{Requeue: true}, result,
		"finalizer-add reconcile must requeue explicitly; the metadata-only update may not re-enqueue via watches")

	var updated computev1alpha.WorkloadDeployment
	require.NoError(t, cl.Get(context.Background(), req.NamespacedName, &updated))
	assert.Contains(t, updated.Finalizers, workloadControllerFinalizer)

	// Second reconcile (post-requeue) proceeds past the finalizer branch and
	// publishes status: ObservedGeneration tracks the deployment generation and
	// DesiredReplicas reflects scale settings.
	result, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	require.NoError(t, cl.Get(context.Background(), req.NamespacedName, &updated))
	assert.Equal(t, updated.Generation, updated.Status.ObservedGeneration)
	assert.Equal(t, int32(1), updated.Status.DesiredReplicas)
	assert.True(t, apimeta.IsStatusConditionTrue(updated.Status.Conditions, computev1alpha.WorkloadDeploymentReplicasReady),
		"no instances are quota-blocked, so ReplicasReady must be true")
}

// NOTE (split): the Available-condition unit tests that exercised
// selectWDBlockingCondition and wdBlockingReasonPriority (TestWDAvailableCondition_*,
// TestWDBlockingReasonPriority_WD, and the makeWDForAvailTest helper) were moved to
// layer E (split/refdata-blocking-reason), where those functions are introduced.
// They were authored against the E-refactored WD controller and cannot compile in
// this layer, which still carries the inline blocking-reason switch.

// ─── wdRefDataCondChanged tests ───────────────────────────────────────────────

// TestWdRefDataCondChanged_BothNil verifies that two nil conditions are treated
// as unchanged (no predicate trigger).
func TestWdRefDataCondChanged_BothNil(t *testing.T) {
	assert.False(t, wdRefDataCondChanged(nil, nil),
		"both nil conditions must not be treated as a change")
}

// TestWdRefDataCondChanged_AddedCondition verifies that a nil→non-nil transition
// (condition first appears on the WD) is treated as a change.
func TestWdRefDataCondChanged_AddedCondition(t *testing.T) {
	newCond := &metav1.Condition{
		Type:   computev1alpha.ReferencedDataReady,
		Status: metav1.ConditionFalse,
		Reason: computev1alpha.ReferencedDataReasonSourceNotFound,
	}
	assert.True(t, wdRefDataCondChanged(nil, newCond),
		"nil→non-nil must be treated as a change")
}

// TestWdRefDataCondChanged_RemovedCondition verifies that a non-nil→nil transition
// (condition removed) is treated as a change.
func TestWdRefDataCondChanged_RemovedCondition(t *testing.T) {
	oldCond := &metav1.Condition{
		Type:   computev1alpha.ReferencedDataReady,
		Status: metav1.ConditionTrue,
		Reason: computev1alpha.ReferencedDataReasonReady,
	}
	assert.True(t, wdRefDataCondChanged(oldCond, nil),
		"non-nil→nil must be treated as a change")
}

// TestWdRefDataCondChanged_StatusChange verifies that a change in the condition's
// Status field is detected.
func TestWdRefDataCondChanged_StatusChange(t *testing.T) {
	old := &metav1.Condition{
		Type:   computev1alpha.ReferencedDataReady,
		Status: metav1.ConditionUnknown,
		Reason: computev1alpha.ReferencedDataReasonResolving,
	}
	new := &metav1.Condition{
		Type:   computev1alpha.ReferencedDataReady,
		Status: metav1.ConditionFalse,
		Reason: computev1alpha.ReferencedDataReasonSourceNotFound,
	}
	assert.True(t, wdRefDataCondChanged(old, new),
		"status change must be detected")
}

// TestWdRefDataCondChanged_MessageChange verifies that a change in Message is
// detected, e.g. when the resolver updates the missing-object name.
func TestWdRefDataCondChanged_MessageChange(t *testing.T) {
	old := &metav1.Condition{
		Type:    computev1alpha.ReferencedDataReady,
		Status:  metav1.ConditionFalse,
		Reason:  computev1alpha.ReferencedDataReasonSourceNotFound,
		Message: `ConfigMap "a" not found in namespace "default"`,
	}
	new := &metav1.Condition{
		Type:    computev1alpha.ReferencedDataReady,
		Status:  metav1.ConditionFalse,
		Reason:  computev1alpha.ReferencedDataReasonSourceNotFound,
		Message: `ConfigMap "b" not found in namespace "default"`,
	}
	assert.True(t, wdRefDataCondChanged(old, new),
		"message change must be detected")
}

// TestWdRefDataCondChanged_Identical verifies that an identical condition (same
// Status/Reason/Message, differing only in LastTransitionTime) is NOT treated as
// a change. This is the key no-self-trigger property: the reconciler's own
// Status().Update does not re-enqueue via the For() predicate.
func TestWdRefDataCondChanged_Identical(t *testing.T) {
	t1 := metav1.Now()
	t2 := metav1.Now()
	old := &metav1.Condition{
		Type:               computev1alpha.ReferencedDataReady,
		Status:             metav1.ConditionFalse,
		Reason:             computev1alpha.ReferencedDataReasonSourceNotFound,
		Message:            testMsgConfigMapNotFound,
		LastTransitionTime: t1,
	}
	new := &metav1.Condition{
		Type:               computev1alpha.ReferencedDataReady,
		Status:             metav1.ConditionFalse,
		Reason:             computev1alpha.ReferencedDataReasonSourceNotFound,
		Message:            testMsgConfigMapNotFound,
		LastTransitionTime: t2, // different timestamp, same content
	}
	assert.False(t, wdRefDataCondChanged(old, new),
		"identical conditions with differing LastTransitionTime must not be treated as a change")
}

// ─── wdReferencedDataChangedPredicate tests ───────────────────────────────────

// makeWDPair builds two empty WorkloadDeployment objects (no conditions) for
// predicate Update tests. Callers mutate the returned objects before constructing
// the event.UpdateEvent.
func makeWDPair(gen int64) (old, new *computev1alpha.WorkloadDeployment) {
	old = &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       wdControllerTestName,
			Namespace:  wdControllerTestNS,
			Generation: gen,
		},
	}
	new = &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       wdControllerTestName,
			Namespace:  wdControllerTestNS,
			Generation: gen,
		},
	}
	return old, new
}

// TestWDPredicate_ReferencedDataReadyAdded verifies that the predicate passes
// when the resolver writes ReferencedDataReady=False for the first time. This is
// the primary gap-closure scenario: the WD had no ReferencedDataReady condition
// (reconciler wrote Available=InstancesProvisioning), the resolver adds
// ReferencedDataReady=False/SourceNotFound, and the predicate must fire so the
// reconciler re-runs and promotes Available to ReferencedDataNotReady.
func TestWDPredicate_ReferencedDataReadyAdded(t *testing.T) {
	pred := wdReferencedDataChangedPredicate()

	oldWD, newWD := makeWDPair(1)
	apimeta.SetStatusCondition(&newWD.Status.Conditions, metav1.Condition{
		Type:               computev1alpha.ReferencedDataReady,
		Status:             metav1.ConditionFalse,
		Reason:             computev1alpha.ReferencedDataReasonSourceNotFound,
		Message:            testMsgConfigMapNotFound,
		LastTransitionTime: metav1.Now(),
	})

	e := event.UpdateEvent{ObjectOld: oldWD, ObjectNew: newWD}
	assert.True(t, pred.Update(e),
		"predicate must pass when ReferencedDataReady is added (nil→False/SourceNotFound)")
}

// TestWDPredicate_ReferencedDataReadyCleared verifies that the predicate passes
// when the resolver sets ReferencedDataReady=True (source ConfigMap was created).
// The WD reconciler must re-run so Available can be promoted from
// ReferencedDataNotReady to StableInstanceFound (or InstancesProvisioning).
func TestWDPredicate_ReferencedDataReadyCleared(t *testing.T) {
	pred := wdReferencedDataChangedPredicate()

	oldWD, newWD := makeWDPair(1)
	apimeta.SetStatusCondition(&oldWD.Status.Conditions, metav1.Condition{
		Type:               computev1alpha.ReferencedDataReady,
		Status:             metav1.ConditionFalse,
		Reason:             computev1alpha.ReferencedDataReasonSourceNotFound,
		Message:            testMsgConfigMapNotFound,
		LastTransitionTime: metav1.Now(),
	})
	apimeta.SetStatusCondition(&newWD.Status.Conditions, metav1.Condition{
		Type:               computev1alpha.ReferencedDataReady,
		Status:             metav1.ConditionTrue,
		Reason:             computev1alpha.ReferencedDataReasonReady,
		Message:            "All companions are ready",
		LastTransitionTime: metav1.Now(),
	})

	e := event.UpdateEvent{ObjectOld: oldWD, ObjectNew: newWD}
	assert.True(t, pred.Update(e),
		"predicate must pass when ReferencedDataReady flips from False to True")
}

// TestWDPredicate_GenerationChanged verifies that the predicate passes when the
// WD's generation changes (spec update), even if the ReferencedDataReady condition
// did not change.
func TestWDPredicate_GenerationChanged(t *testing.T) {
	pred := wdReferencedDataChangedPredicate()

	oldWD, newWD := makeWDPair(1)
	newWD.Generation = 2

	e := event.UpdateEvent{ObjectOld: oldWD, ObjectNew: newWD}
	assert.True(t, pred.Update(e),
		"predicate must pass when metadata.generation increases")
}

// TestWDPredicate_AvailableOnlyChange verifies that the predicate DROPS updates
// where only the Available condition changed. This is the self-trigger prevention:
// after the WD reconciler writes Available=ReferencedDataNotReady, the predicate
// must not re-enqueue the same reconciler via the For() watch.
func TestWDPredicate_AvailableOnlyChange(t *testing.T) {
	pred := wdReferencedDataChangedPredicate()

	// Both old and new have the SAME ReferencedDataReady condition.
	refDataCond := metav1.Condition{
		Type:               computev1alpha.ReferencedDataReady,
		Status:             metav1.ConditionFalse,
		Reason:             computev1alpha.ReferencedDataReasonSourceNotFound,
		Message:            testMsgConfigMapNotFound,
		LastTransitionTime: metav1.Now(),
	}
	oldWD, newWD := makeWDPair(1)
	apimeta.SetStatusCondition(&oldWD.Status.Conditions, refDataCond)
	apimeta.SetStatusCondition(&newWD.Status.Conditions, refDataCond)

	// The WD reconciler wrote Available=InstancesProvisioning (old) and then
	// Available=ReferencedDataNotReady (new). ReferencedDataReady is unchanged.
	// NOTE (split): the named reason constants land in layer E; literals are used
	// here because the predicate only cares that the Available reason changed.
	apimeta.SetStatusCondition(&oldWD.Status.Conditions, metav1.Condition{
		Type:    computev1alpha.WorkloadDeploymentAvailable,
		Status:  metav1.ConditionFalse,
		Reason:  "InstancesProvisioning",
		Message: "Instances are being provisioned",
	})
	apimeta.SetStatusCondition(&newWD.Status.Conditions, metav1.Condition{
		Type:    computev1alpha.WorkloadDeploymentAvailable,
		Status:  metav1.ConditionFalse,
		Reason:  "ReferencedDataNotReady",
		Message: testMsgConfigMapNotFound,
	})

	e := event.UpdateEvent{ObjectOld: oldWD, ObjectNew: newWD}
	assert.False(t, pred.Update(e),
		"predicate must drop update when only Available changed; "+
			"ReferencedDataReady is unchanged so the reconciler's own write must not re-enqueue itself")
}

// TestWDPredicate_ReplicasReadyOnlyChange verifies that the predicate DROPS updates
// where only the ReplicasReady condition changed (also written by this reconciler),
// for the same self-trigger prevention reason as the Available-only case.
func TestWDPredicate_ReplicasReadyOnlyChange(t *testing.T) {
	pred := wdReferencedDataChangedPredicate()

	refDataCond := metav1.Condition{
		Type:               computev1alpha.ReferencedDataReady,
		Status:             metav1.ConditionTrue,
		Reason:             computev1alpha.ReferencedDataReasonReady,
		Message:            "all ready",
		LastTransitionTime: metav1.Now(),
	}
	oldWD, newWD := makeWDPair(2)
	apimeta.SetStatusCondition(&oldWD.Status.Conditions, refDataCond)
	apimeta.SetStatusCondition(&newWD.Status.Conditions, refDataCond)

	// ReplicasReady changed (more instances became ready) but ReferencedDataReady is identical.
	apimeta.SetStatusCondition(&oldWD.Status.Conditions, metav1.Condition{
		Type:    computev1alpha.WorkloadDeploymentReplicasReady,
		Status:  metav1.ConditionFalse,
		Reason:  reasonReplicasAvailable,
		Message: "0/2 replicas available",
	})
	apimeta.SetStatusCondition(&newWD.Status.Conditions, metav1.Condition{
		Type:    computev1alpha.WorkloadDeploymentReplicasReady,
		Status:  metav1.ConditionTrue,
		Reason:  reasonReplicasAvailable,
		Message: "2/2 replicas available",
	})

	e := event.UpdateEvent{ObjectOld: oldWD, ObjectNew: newWD}
	assert.False(t, pred.Update(e),
		"predicate must drop update when only ReplicasReady changed")
}

// TestWDPredicate_CreateAlwaysPasses verifies that Create events always trigger
// the reconciler regardless of conditions.
func TestWDPredicate_CreateAlwaysPasses(t *testing.T) {
	pred := wdReferencedDataChangedPredicate()
	e := event.CreateEvent{Object: &computev1alpha.WorkloadDeployment{}}
	assert.True(t, pred.Create(e))
}

// TestWDPredicate_DeleteAlwaysPasses verifies that Delete events always trigger
// the reconciler.
func TestWDPredicate_DeleteAlwaysPasses(t *testing.T) {
	pred := wdReferencedDataChangedPredicate()
	e := event.DeleteEvent{Object: &computev1alpha.WorkloadDeployment{}}
	assert.True(t, pred.Delete(e))
}

// TestWDPredicate_GenericAlwaysPasses verifies that Generic events (e.g. from
// external sources) always trigger the reconciler.
func TestWDPredicate_GenericAlwaysPasses(t *testing.T) {
	pred := wdReferencedDataChangedPredicate()
	e := event.GenericEvent{Object: &computev1alpha.WorkloadDeployment{}}
	assert.True(t, pred.Generic(e))
}
