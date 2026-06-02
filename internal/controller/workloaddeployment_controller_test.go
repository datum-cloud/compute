// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

const (
	// wdControllerTestName, wdControllerTestNS, wdControllerTestUID are the
	// stable identifiers used across the WorkloadDeployment controller tests.
	wdControllerTestName = "test-wd"
	wdControllerTestNS   = gcTestProjectNamespace // "default"
	wdControllerTestUID  = "wd-uid-test"
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
			CityCode:      wbTestCityCode,
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
			CityCode:      wbTestCityCode,
			PlacementName: testDefaultPlacement,
			WorkloadRef:   computev1alpha.WorkloadReference{Name: rdTestWorkloadName},
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

// makeWDForAvailTest constructs a WorkloadDeployment for Available-condition
// unit tests, optionally stamping a ReferencedDataReady condition on status.
func makeWDForAvailTest(generation int64, refDataStatus metav1.ConditionStatus, refDataReason, refDataMessage string) *computev1alpha.WorkloadDeployment {
	d := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       wdControllerTestName,
			Namespace:  wdControllerTestNS,
			Generation: generation,
		},
	}
	if refDataReason != "" {
		d.Status.Conditions = []metav1.Condition{
			{
				Type:               computev1alpha.ReferencedDataReady,
				Status:             refDataStatus,
				Reason:             refDataReason,
				Message:            refDataMessage,
				LastTransitionTime: metav1.Now(),
			},
		}
	}
	return d
}

// TestWDAvailableCondition_ReferencedDataSourceNotFound verifies that when the
// WD's ReferencedDataReady condition is False/SourceNotFound, the Available
// condition is set to False/ReferencedDataNotReady with the resolver message
// verbatim and ObservedGeneration equal to the deployment generation.
func TestWDAvailableCondition_ReferencedDataSourceNotFound(t *testing.T) {
	const (
		gen             = int64(5)
		desiredReplicas = int32(2)
		replicas        = 2
	)
	msg := testMsgConfigMapNotFound
	deployment := makeWDForAvailTest(gen, metav1.ConditionFalse,
		computev1alpha.ReferencedDataReasonSourceNotFound, msg)

	cond := selectWDBlockingCondition(deployment, true, 0, 1, replicas, desiredReplicas)

	assert.Equal(t, computev1alpha.WorkloadDeploymentAvailable, cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, cond.Reason)
	assert.Equal(t, msg, cond.Message, "message must be the resolver message verbatim")
	assert.Equal(t, gen, cond.ObservedGeneration, "ObservedGeneration must match deployment generation")
}

// TestWDAvailableCondition_QuotaNotGranted verifies that QuotaNotGranted is
// surfaced when replicas are quota-blocked and there is no resolver error.
func TestWDAvailableCondition_QuotaNotGranted(t *testing.T) {
	const (
		gen             = int64(2)
		desiredReplicas = int32(3)
		replicas        = 3
	)
	deployment := makeWDForAvailTest(gen, metav1.ConditionTrue, computev1alpha.ReferencedDataReasonReady, "all present")

	cond := selectWDBlockingCondition(deployment, true, 2, 0, replicas, desiredReplicas)

	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, cond.Reason)
	assert.Contains(t, cond.Message, "2 of")
	assert.Equal(t, gen, cond.ObservedGeneration)
}

// TestWDAvailableCondition_ReferencedDataWinsOverQuota verifies evaluate-all-then-pick:
// when both ReferencedDataReady=False and quotaBlockedReplicas > 0, referenced-data
// (priority 4) beats quota (priority 3).
func TestWDAvailableCondition_ReferencedDataWinsOverQuota(t *testing.T) {
	const (
		gen             = int64(1)
		desiredReplicas = int32(2)
		replicas        = 2
	)
	deployment := makeWDForAvailTest(gen, metav1.ConditionFalse,
		computev1alpha.ReferencedDataReasonSourceNotFound,
		`ConfigMap "X" not found in namespace "default"`)

	cond := selectWDBlockingCondition(deployment, true, 1, 1, replicas, desiredReplicas)

	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, cond.Reason,
		"ReferencedDataNotReady (priority 4) must beat QuotaNotGranted (priority 3)")
}

// TestWDAvailableCondition_NetworkProvisioningVsReferencedData verifies that when
// network is not ready AND the WD has ReferencedDataReady=False/SourceNotFound,
// referenced-data wins because priority 4 > priority 2. This is the key
// evaluate-all-then-pick test: the old code would have short-circuited at
// network-not-ready and returned NetworkProvisioning.
func TestWDAvailableCondition_NetworkProvisioningVsReferencedData(t *testing.T) {
	const (
		gen             = int64(1)
		desiredReplicas = int32(1)
		replicas        = 1
	)
	deployment := makeWDForAvailTest(gen, metav1.ConditionFalse,
		computev1alpha.ReferencedDataReasonSourceNotFound,
		`ConfigMap "X" not found`)

	cond := selectWDBlockingCondition(deployment, false /* !networkReady */, 0, 1, replicas, desiredReplicas)

	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, cond.Reason,
		"ReferencedDataNotReady (priority 4) must beat NetworkProvisioning (priority 2)")
}

// TestWDBlockingReasonPriority_WD exhaustively verifies every entry in the
// wdBlockingReasonPriority switch statement.
func TestWDBlockingReasonPriority_WD(t *testing.T) {
	tests := []struct {
		reason   string
		wantPrio int
	}{
		{"", 0},
		{"Unknown", 0},
		{computev1alpha.WorkloadDeploymentReasonInstancesProvisioning, 1},
		{computev1alpha.WorkloadDeploymentReasonNetworkProvisioning, 2},
		{computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, 3},
		{computev1alpha.InstanceProgrammedReasonPendingQuota, 3},
		{computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, 4},
		{computev1alpha.ReferencedDataReasonAwaitingPropagation, 4},
		{computev1alpha.ReferencedDataReasonResolving, 4},
		{computev1alpha.ReferencedDataReasonSourceNotFound, 5},
		{computev1alpha.ReferencedDataReasonSourceTooLarge, 5},
		{computev1alpha.ReferencedDataReasonSourceUnauthorized, 5},
		{computev1alpha.WorkloadReasonNetworkNotFound, 6},
		{reasonNetworkFailedToCreate, 7},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			assert.Equal(t, tt.wantPrio, wdBlockingReasonPriority(tt.reason))
		})
	}
}

// TestWDAvailableCondition_ObservedGeneration verifies that the Available
// condition emitted by selectWDBlockingCondition always carries ObservedGeneration
// equal to the deployment's current generation.
func TestWDAvailableCondition_ObservedGeneration(t *testing.T) {
	const gen = int64(42)
	deployment := makeWDForAvailTest(gen, "", "", "")

	cond := selectWDBlockingCondition(deployment, true, 0, 0, 0, 1)

	assert.Equal(t, gen, cond.ObservedGeneration, "ObservedGeneration must match deployment generation")
	// Verify the condition is also reachable via apimeta.FindStatusCondition (field
	// names match what the API machinery expects).
	conditions := []metav1.Condition{cond}
	found := apimeta.FindStatusCondition(conditions, computev1alpha.WorkloadDeploymentAvailable)
	require.NotNil(t, found)
	assert.Equal(t, gen, found.ObservedGeneration)
}

// makeWDWithAnnotation builds a WorkloadDeployment carrying the given
// ReferencedDataErrorAnnotation value but no status conditions, simulating a
// cell WD copy that received the annotation from the hub via Karmada propagation
// but has no locally-written resolver conditions.
func makeWDWithAnnotation(generation int64, annotValue string) *computev1alpha.WorkloadDeployment {
	d := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       wdControllerTestName,
			Namespace:  wdControllerTestNS,
			Generation: generation,
			Annotations: map[string]string{
				computev1alpha.ReferencedDataErrorAnnotation: annotValue,
			},
		},
	}
	return d
}

// mustEncodeTerminalError encodes a terminal error annotation value for use in
// tests. It panics on encoding failure, which should never happen in tests.
func mustEncodeTerminalError(reason, message string) string {
	v, err := encodeTerminalError(reason, message)
	if err != nil {
		panic(err)
	}
	return v
}

// TestWDAvailableCondition_AnnotationSourceNotFound verifies that a cell WD
// carrying the ReferencedDataErrorAnnotation (set by the hub-side resolver and
// propagated by Karmada) promotes Available to False/SourceNotFound even when no
// ReferencedDataReady status condition is present on the cell WD.
//
// This is the federation bridge introduced by task #40: the annotation is the
// only terminal-error signal visible on the cell WD copy.
func TestWDAvailableCondition_AnnotationSourceNotFound(t *testing.T) {
	const (
		gen             = int64(3)
		desiredReplicas = int32(2)
		replicas        = 2
	)
	annot := mustEncodeTerminalError(
		computev1alpha.ReferencedDataReasonSourceNotFound,
		testMsgConfigMapNotFound,
	)
	deployment := makeWDWithAnnotation(gen, annot)

	cond := selectWDBlockingCondition(deployment, true, 0, 1, replicas, desiredReplicas)

	assert.Equal(t, computev1alpha.WorkloadDeploymentAvailable, cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	// The annotation carries the raw terminal reason (SourceNotFound, priority 5),
	// which beats InstancesProvisioning (priority 1) and ReferencedDataNotReady (priority 4).
	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceNotFound, cond.Reason)
	assert.Equal(t, testMsgConfigMapNotFound, cond.Message, "message must be the resolver message verbatim")
	assert.Equal(t, gen, cond.ObservedGeneration)
}

// TestWDAvailableCondition_AnnotationAndConditionBothPresent verifies that when
// both the annotation and the ReferencedDataReady=False status condition are
// present (e.g. single-cluster mode, or a race during federation rollout), the
// annotation path is taken at the same effective priority because both encode the
// same terminal reason.
func TestWDAvailableCondition_AnnotationAndConditionBothPresent(t *testing.T) {
	const (
		gen             = int64(7)
		desiredReplicas = int32(1)
		replicas        = 1
	)
	annot := mustEncodeTerminalError(
		computev1alpha.ReferencedDataReasonSourceNotFound,
		testMsgConfigMapNotFound,
	)
	// Stamp both the annotation and the status condition with matching reason+message.
	deployment := makeWDWithAnnotation(gen, annot)
	deployment.Status.Conditions = []metav1.Condition{
		{
			Type:               computev1alpha.ReferencedDataReady,
			Status:             metav1.ConditionFalse,
			Reason:             computev1alpha.ReferencedDataReasonSourceNotFound,
			Message:            testMsgConfigMapNotFound,
			LastTransitionTime: metav1.Now(),
		},
	}

	cond := selectWDBlockingCondition(deployment, true, 0, 1, replicas, desiredReplicas)

	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	// Both paths arrive at the same terminal reason; the winner is stable regardless
	// of evaluation order since both carry priority 5.
	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceNotFound, cond.Reason)
	assert.Equal(t, testMsgConfigMapNotFound, cond.Message)
}

// TestWDAvailableCondition_AnnotationWinsOverQuota verifies that a terminal
// annotation reason (priority 5) beats quotaBlockedReplicas (priority 3) even
// when quota is also blocking.
func TestWDAvailableCondition_AnnotationWinsOverQuota(t *testing.T) {
	const (
		gen             = int64(2)
		desiredReplicas = int32(2)
		replicas        = 2
	)
	annot := mustEncodeTerminalError(
		computev1alpha.ReferencedDataReasonSourceNotFound,
		testMsgConfigMapNotFound,
	)
	deployment := makeWDWithAnnotation(gen, annot)

	// quotaBlockedReplicas=1 would normally surface QuotaNotGranted (priority 3).
	cond := selectWDBlockingCondition(deployment, true, 1, 0, replicas, desiredReplicas)

	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceNotFound, cond.Reason,
		"SourceNotFound (priority 5) must beat QuotaNotGranted (priority 3)")
}

// TestWDAvailableCondition_NoAnnotationPropagationLag verifies that the existing
// propagation-lag path (referencedDataBlockedReplicas > 0, no annotation, no
// ReferencedDataReady condition) is unaffected by the annotation bridge change.
func TestWDAvailableCondition_NoAnnotationPropagationLag(t *testing.T) {
	const (
		gen             = int64(1)
		desiredReplicas = int32(2)
		replicas        = 2
	)
	// No annotation, no ReferencedDataReady condition: companions still propagating.
	deployment := makeWDForAvailTest(gen, "", "", "")

	cond := selectWDBlockingCondition(deployment, true, 0, 1, replicas, desiredReplicas)

	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, cond.Reason,
		"propagation-lag path must still fire when annotation is absent")
	assert.Contains(t, cond.Message, "waiting for companion propagation")
}

// TestWDAvailableCondition_AnnotationEmptyString verifies that an empty annotation
// value is treated as absent and falls through to the existing logic.
func TestWDAvailableCondition_AnnotationEmptyString(t *testing.T) {
	const (
		gen             = int64(1)
		desiredReplicas = int32(1)
		replicas        = 1
	)
	deployment := makeWDWithAnnotation(gen, "")

	cond := selectWDBlockingCondition(deployment, true, 0, 0, replicas, desiredReplicas)

	// No real blockers; falls through to InstancesProvisioning.
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonInstancesProvisioning, cond.Reason)
}

// TestWDAvailableCondition_AnnotationMalformedJSON verifies that a malformed
// annotation value is silently ignored (no panic) and the function falls through
// to the next applicable blocking reason.
func TestWDAvailableCondition_AnnotationMalformedJSON(t *testing.T) {
	const (
		gen             = int64(1)
		desiredReplicas = int32(1)
		replicas        = 1
	)
	deployment := makeWDWithAnnotation(gen, "not-valid-json{{")

	// Should not panic; malformed annotation is skipped.
	cond := selectWDBlockingCondition(deployment, true, 0, 0, replicas, desiredReplicas)

	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonInstancesProvisioning, cond.Reason,
		"malformed annotation must be silently ignored; fallback to InstancesProvisioning")
}
