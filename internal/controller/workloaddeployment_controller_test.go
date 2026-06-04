// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

const (
	// wdControllerTestName / wdControllerTestNS / wdControllerTestUID are shared
	// fixtures for the WorkloadDeployment controller unit tests.
	wdControllerTestName = "test-wd"
	wdControllerTestNS   = "default"
	wdControllerTestUID  = "wd-uid-test"

	// cityCode is the shared CityCode fixture for WorkloadDeployment tests.
	cityCode = "DFW"

	// testMsgConfigMapNotFound is a representative terminal referenced-data
	// message used across the Available-rollup unit tests.
	testMsgConfigMapNotFound = `ConfigMap "app-config" not found in namespace "default"`
)

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

	// Build a deployment with a minimal template so ComputeHash produces a stable hash.
	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wdControllerTestName,
			Namespace: wdControllerTestNS,
			UID:       wdControllerTestUID,
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode:      cityCode,
			PlacementName: testDefaultPlacement,
			WorkloadRef:   computev1alpha.WorkloadReference{Name: rdTestWorkloadName},
			ScaleSettings: computev1alpha.HorizontalScaleSettings{
				MinReplicas: 2,
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

	templateHash := instancecontrol.ComputeHash(deployment.Spec.Template)

	// Instance A: Programmed=True but Status.Controller is nil (the panic case).
	// This instance must NOT be counted as current and must NOT cause a panic.
	instanceNilController := computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instance-nil-controller",
			Namespace: wdControllerTestNS,
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: wdControllerTestUID,
			},
		},
		Status: computev1alpha.InstanceStatus{
			Conditions: []metav1.Condition{
				{
					Type:               computev1alpha.InstanceProgrammed,
					Status:             metav1.ConditionTrue,
					Reason:             "Programmed",
					LastTransitionTime: metav1.Now(),
				},
			},
			// Status.Controller intentionally nil — this is the regression scenario.
			Controller: nil,
		},
	}

	// Instance B: Programmed=True with Status.Controller populated and matching hash.
	// This instance MUST be counted as current (currentReplicas == 1).
	instanceWithController := computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instance-with-controller",
			Namespace: wdControllerTestNS,
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: wdControllerTestUID,
			},
		},
		Status: computev1alpha.InstanceStatus{
			Conditions: []metav1.Condition{
				{
					Type:               computev1alpha.InstanceProgrammed,
					Status:             metav1.ConditionTrue,
					Reason:             "Programmed",
					LastTransitionTime: metav1.Now(),
				},
			},
			Controller: &computev1alpha.InstanceControllerStatus{
				ObservedTemplateHash: templateHash,
			},
		},
	}

	// Instance C: Ready=True (contributes to readyReplicas).
	instanceReady := computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instance-ready",
			Namespace: wdControllerTestNS,
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: wdControllerTestUID,
			},
		},
		Status: computev1alpha.InstanceStatus{
			Conditions: []metav1.Condition{
				{
					Type:               computev1alpha.InstanceReady,
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					LastTransitionTime: metav1.Now(),
				},
			},
			Controller: &computev1alpha.InstanceControllerStatus{
				ObservedTemplateHash: templateHash,
			},
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
// for the second nil-deref in reconcileInstanceGates: Spec.Controller is also a
// nilable pointer, and the network gate-clearing path dereferenced
// instance.Spec.Controller.SchedulingGates without a nil guard. When
// networkReady is true and an instance has no controller spec, the unguarded
// deref panicked the reconcile. This must not panic.
func TestReconcileInstanceGates_NilSpecController_DoesNotPanic(t *testing.T) {
	t.Parallel()

	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: wdControllerTestName, Namespace: wdControllerTestNS, UID: wdControllerTestUID},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode:      cityCode,
			PlacementName: testDefaultPlacement,
			WorkloadRef:   computev1alpha.WorkloadReference{Name: "test-workload"},
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
