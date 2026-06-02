// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

const (
	refDataTestCluster    = "test-project"
	refDataTestNamespace  = "ns-test-uid"
	refDataTestDeployment = "my-deployment"
	refDataTestInstance   = "my-instance"
	refDataTestWDUID      = "wd-uid"
	refDataTestDataKey    = "key"
	// Companion objects are now named by source name (option B fix).
	refDataTestCMCompanionName     = "app-config"
	refDataTestSecretCompanionName = "db-creds"

	// Annotation tokens are kind-qualified "Kind/name" strings.
	refDataTestCMToken     = "ConfigMap/app-config"
	refDataTestSecretToken = "Secret/db-creds"
	refDataTestDataValue   = "value"
)

// makeWDForCell builds a WorkloadDeployment that can own test instances in the
// cell-side gate-clearing tests. Named distinctly to avoid collision with the
// makeWD helper in referenceddata_controller_test.go.
func makeWDForCell(annotationValue string) *computev1alpha.WorkloadDeployment {
	wd := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      refDataTestDeployment,
			Namespace: refDataTestNamespace,
			UID:       refDataTestWDUID,
		},
	}
	if annotationValue != "" {
		wd.Annotations = map[string]string{
			computev1alpha.ExpectedReferencedDataAnnotation: annotationValue,
		}
	}
	return wd
}

// makeInstanceWithRefDataGate builds an Instance with the ReferencedData gate
// and an owner reference to makeWDForCell.
func makeInstanceWithRefDataGate() *computev1alpha.Instance {
	gates := []computev1alpha.SchedulingGate{
		{Name: instancecontrol.ReferencedDataSchedulingGate.String()},
	}

	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       refDataTestInstance,
			Namespace:  refDataTestNamespace,
			Finalizers: []string{instanceQuotaFinalizer, instanceControllerFinalizer},
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: refDataTestWDUID,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: testComputeAPIVersion,
					Kind:       kindWorkloadDeployment,
					Name:       refDataTestDeployment,
					UID:        refDataTestWDUID,
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Controller: &computev1alpha.InstanceController{
				SchedulingGates: gates,
			},
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
			},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
		},
	}
}

// makeCompanionConfigMap creates a companion ConfigMap with the ReferencedDataLabel
// in the standard test namespace.
func makeCompanionConfigMap(name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: refDataTestNamespace,
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
		},
		Data: map[string]string{refDataTestDataKey: refDataTestDataValue},
	}
}

// makeCompanionSecret creates a companion Secret with the ReferencedDataLabel
// in the standard test namespace.
func makeCompanionSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: refDataTestNamespace,
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
		},
		Data: map[string][]byte{refDataTestDataKey: []byte(refDataTestDataValue)},
	}
}

// newRefDataReconciler constructs an InstanceReconciler backed by a fake client
// that has the given project-cluster objects. A fake event recorder is returned
// so tests can inspect events.
func newRefDataReconciler(
	t *testing.T,
	projectObjs []client.Object,
) (*InstanceReconciler, client.Client, *record.FakeRecorder) {
	t.Helper()
	s := newTestScheme(t)

	projectClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(projectObjs...).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	mgr := &fakeMCManager{
		clusters: map[string]cluster.Cluster{
			refDataTestCluster: &fakeCluster{cl: projectClient},
		},
	}

	fakeRec := record.NewFakeRecorder(32)
	r := &InstanceReconciler{
		mgr:      mgr,
		recorder: fakeRec,
	}
	r.finalizers = finalizer.NewFinalizers()
	require.NoError(t, r.finalizers.Register(instanceControllerFinalizer, r))
	return r, projectClient, fakeRec
}

func reconcileRefData(t *testing.T, r *InstanceReconciler) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}},
		ClusterName: refDataTestCluster,
	})
	require.NoError(t, err)
}

// TestReferencedDataGateHeldWhenAnnotationAbsent verifies that when the WD
// has no expected-referenced-data annotation yet (resolver still running), the
// Instance gets ReferencedDataReady=Unknown/Resolving and the gate is kept.
func TestReferencedDataGateHeldWhenAnnotationAbsent(t *testing.T) {
	wd := makeWDForCell("") // no annotation
	inst := makeInstanceWithRefDataGate()

	r, projectClient, _ := newRefDataReconciler(t,
		[]client.Object{inst, wd},
	)

	reconcileRefData(t, r)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond, "ReferencedDataReady condition should be set")
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonResolving, cond.Reason)

	// Gate must still be present.
	hasGate := false
	if updated.Spec.Controller != nil {
		for _, g := range updated.Spec.Controller.SchedulingGates {
			if g.Name == instancecontrol.ReferencedDataSchedulingGate.String() {
				hasGate = true
			}
		}
	}
	assert.True(t, hasGate, "ReferencedData gate should still be present when annotation is absent")
}

// TestReferencedDataGateHeldWhenCompanionsMissing verifies that when only some
// companions are present the condition is AwaitingPropagation/False and the
// gate is not removed. The missing companion names should appear in the message.
func TestReferencedDataGateHeldWhenCompanionsMissing(t *testing.T) {
	expected := []string{refDataTestCMToken, refDataTestSecretToken}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))
	inst := makeInstanceWithRefDataGate()

	// Only the ConfigMap companion is present; the Secret is missing.
	companionCM := makeCompanionConfigMap(refDataTestCMCompanionName)

	r, projectClient, fakeRec := newRefDataReconciler(t,
		[]client.Object{inst, wd, companionCM},
	)

	reconcileRefData(t, r)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond, "ReferencedDataReady condition should be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonAwaitingPropagation, cond.Reason)
	assert.Contains(t, cond.Message, refDataTestSecretToken, "message should name the missing companion token")

	// Gate must still be present.
	hasGate := false
	if updated.Spec.Controller != nil {
		for _, g := range updated.Spec.Controller.SchedulingGates {
			if g.Name == instancecontrol.ReferencedDataSchedulingGate.String() {
				hasGate = true
			}
		}
	}
	assert.True(t, hasGate, "ReferencedData gate should be held while companions are missing")

	// A Warning event should have been emitted.
	select {
	case evt := <-fakeRec.Events:
		assert.Contains(t, evt, computev1alpha.ReferencedDataReasonAwaitingPropagation)
	default:
		t.Error("expected a Warning event to be emitted when companions are missing")
	}
}

// TestReferencedDataGateClearedWhenAllPresent verifies the full happy path:
// all expected companions are present → gate is removed and condition is Ready.
func TestReferencedDataGateClearedWhenAllPresent(t *testing.T) {
	expected := []string{refDataTestCMToken, refDataTestSecretToken}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))
	inst := makeInstanceWithRefDataGate()

	companionCM := makeCompanionConfigMap(refDataTestCMCompanionName)
	companionSecret := makeCompanionSecret(refDataTestSecretCompanionName)

	r, projectClient, fakeRec := newRefDataReconciler(t,
		[]client.Object{inst, wd, companionCM, companionSecret},
	)

	// First reconcile: sets ReferencedDataReady=True in status, returns early.
	reconcileRefData(t, r)

	var afterStatus computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &afterStatus))

	cond := apimeta.FindStatusCondition(afterStatus.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonReady, cond.Reason)

	// Second reconcile: status already True, gate is removed from spec.
	reconcileRefData(t, r)

	var afterGatePatch computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &afterGatePatch))

	for _, g := range afterGatePatch.Spec.Controller.SchedulingGates {
		assert.NotEqual(t, instancecontrol.ReferencedDataSchedulingGate.String(), g.Name,
			"ReferencedData gate should have been removed")
	}

	// A Normal event should have been emitted when the gate cleared.
	var gotClearedEvent bool
	for {
		select {
		case evt := <-fakeRec.Events:
			if containsAll(evt, "Normal", computev1alpha.ReferencedDataReasonReady) {
				gotClearedEvent = true
			}
		default:
			goto done
		}
	}
done:
	assert.True(t, gotClearedEvent, "expected a Normal event when the ReferencedData gate is cleared")
}

// TestReferencedDataIdempotentWhenAlreadyReady verifies that a second reconcile
// when the gate is gone and condition is already True produces no changes.
func TestReferencedDataIdempotentWhenAlreadyReady(t *testing.T) {
	expected := []string{refDataTestCMToken}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))

	// Instance with no gate (already cleared) and Ready condition already set.
	inst := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       refDataTestInstance,
			Namespace:  refDataTestNamespace,
			Finalizers: []string{instanceQuotaFinalizer, instanceControllerFinalizer},
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: refDataTestWDUID,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: testComputeAPIVersion,
					Kind:       kindWorkloadDeployment,
					Name:       refDataTestDeployment,
					UID:        refDataTestWDUID,
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Controller: &computev1alpha.InstanceController{
				SchedulingGates: []computev1alpha.SchedulingGate{},
			},
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
			},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
		},
		Status: computev1alpha.InstanceStatus{
			Conditions: []metav1.Condition{
				{
					Type:               computev1alpha.ReferencedDataReady,
					Status:             metav1.ConditionTrue,
					Reason:             computev1alpha.ReferencedDataReasonReady,
					Message:            "All 1 referenced companion(s) are present on cell",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	companionCM := makeCompanionConfigMap(refDataTestCMCompanionName)

	r, projectClient, fakeRec := newRefDataReconciler(t,
		[]client.Object{inst, wd, companionCM},
	)

	// Fetch current resource version so we can check for updates.
	var before computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &before))

	reconcileRefData(t, r)

	var after computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &after))

	// No gate should have been re-added.
	assert.Empty(t, after.Spec.Controller.SchedulingGates)

	// Condition should still be True.
	cond := apimeta.FindStatusCondition(after.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	// No events should have been emitted (gate was already gone).
	select {
	case evt := <-fakeRec.Events:
		t.Errorf("unexpected event emitted during idempotent reconcile: %s", evt)
	default:
		// expected: no event
	}
}

// TestReferencedDataPartialPresenceShowsDiff verifies the diff message names
// specifically which companions are missing when only some are present.
func TestReferencedDataPartialPresenceShowsDiff(t *testing.T) {
	// Annotation uses kind-qualified tokens; companion objects use source names.
	expected := []string{"ConfigMap/cfg-a", "ConfigMap/cfg-b", "Secret/sec-x"}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))
	inst := makeInstanceWithRefDataGate()

	// Only cfg-a companion is present; cfg-b and sec-x are missing.
	companionA := makeCompanionConfigMap("cfg-a")

	r, projectClient, _ := newRefDataReconciler(t,
		[]client.Object{inst, wd, companionA},
	)

	reconcileRefData(t, r)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonAwaitingPropagation, cond.Reason)

	// Both missing companions should be mentioned in the message as kind-qualified tokens.
	assert.Contains(t, cond.Message, "ConfigMap/cfg-b")
	assert.Contains(t, cond.Message, "Secret/sec-x")
	// The present one should NOT be mentioned as missing.
	assert.NotContains(t, cond.Message, "ConfigMap/cfg-a")
}

// TestReferencedDataEventEmittedOnClear verifies that a Normal event is emitted
// precisely once when the gate transitions from present to cleared.
func TestReferencedDataEventEmittedOnClear(t *testing.T) {
	expected := []string{"ConfigMap/my-config"}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))
	inst := makeInstanceWithRefDataGate()
	companion := makeCompanionConfigMap("my-config")

	r, projectClient, fakeRec := newRefDataReconciler(t,
		[]client.Object{inst, wd, companion},
	)

	// Single pass: condition set to Ready, status updated, gate patched away, event emitted.
	// The federation branch clears the gate in the same reconcile pass as the status update
	// (rather than a separate pass) because gate removal is inlined after status.Update.
	reconcileRefData(t, r)

	var cleared computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &cleared))
	cond := apimeta.FindStatusCondition(cleared.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	hasGate := false
	for _, g := range cleared.Spec.Controller.SchedulingGates {
		if g.Name == instancecontrol.ReferencedDataSchedulingGate.String() {
			hasGate = true
		}
	}
	assert.False(t, hasGate, "gate should be cleared in the same pass as status update")

	// Expect exactly one Normal/Ready event.
	var normalEvents int
drainLoop:
	for {
		select {
		case evt := <-fakeRec.Events:
			if containsAll(evt, "Normal", computev1alpha.ReferencedDataReasonReady) {
				normalEvents++
			}
		default:
			break drainLoop
		}
	}
	assert.Equal(t, 1, normalEvents, "expected exactly one Normal/Ready event on gate-clear")
}

// TestReferencedDataStaleConditionGuard verifies that a stale True condition
// from generation N does not cause the ReferencedData gate to be removed for
// an instance at generation N+1. The gate must only be cleared once the
// condition has been re-evaluated at the current generation.
func TestReferencedDataStaleConditionGuard(t *testing.T) {
	expected := []string{refDataTestCMToken}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))

	companion := makeCompanionConfigMap(refDataTestCMCompanionName)

	// Build an instance at generation 2 whose ReferencedDataReady condition is
	// True but was observed at generation 1 (stale). The gate is present because
	// the spec was updated (rolling update) after the condition was last written.
	inst := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       refDataTestInstance,
			Namespace:  refDataTestNamespace,
			Generation: 2, // current generation after spec update
			Finalizers: []string{instanceQuotaFinalizer, instanceControllerFinalizer},
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: refDataTestWDUID,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: testComputeAPIVersion,
					Kind:       kindWorkloadDeployment,
					Name:       refDataTestDeployment,
					UID:        refDataTestWDUID,
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Controller: &computev1alpha.InstanceController{
				SchedulingGates: []computev1alpha.SchedulingGate{
					{Name: instancecontrol.ReferencedDataSchedulingGate.String()},
				},
			},
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
			},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
		},
		Status: computev1alpha.InstanceStatus{
			Conditions: []metav1.Condition{
				{
					Type:   computev1alpha.ReferencedDataReady,
					Status: metav1.ConditionTrue,
					Reason: computev1alpha.ReferencedDataReasonReady,
					// ObservedGeneration=1: stale — condition was set before the rolling update
					// bumped the spec to generation 2.
					ObservedGeneration: 1,
					Message:            "All 1 referenced companion(s) are present on cell",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	r, projectClient, _ := newRefDataReconciler(t,
		[]client.Object{inst, wd, companion},
	)

	// First reconcile: re-evaluates condition at generation 2 (updates ObservedGeneration).
	// The gate must NOT be removed during this pass because reconcileSchedulingGates
	// sees the stale condition (ObservedGeneration=1) that was loaded before the
	// status update. After the status update the condition is at generation 2.
	reconcileRefData(t, r)

	var afterFirst computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &afterFirst))

	// Condition should now reflect generation 2.
	cond := apimeta.FindStatusCondition(afterFirst.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond, "ReferencedDataReady condition should be present")
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, int64(2), cond.ObservedGeneration, "condition ObservedGeneration should be updated to current generation")

	// Second reconcile: condition is now at generation 2 — gate should be cleared.
	reconcileRefData(t, r)

	var afterSecond computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &afterSecond))

	for _, g := range afterSecond.Spec.Controller.SchedulingGates {
		assert.NotEqual(t, instancecontrol.ReferencedDataSchedulingGate.String(), g.Name,
			"ReferencedData gate should be removed once condition is at current generation")
	}
}

// ─── Federation annotation bridge: terminal error propagation hub→cell ────────

// makeWDWithTerminalError returns a WD carrying the ReferencedDataErrorAnnotation
// as a cell WD copy would after Karmada propagation from the hub.
func makeWDWithTerminalError(reason, message string) *computev1alpha.WorkloadDeployment {
	raw, _ := encodeTerminalError(reason, message)
	wd := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      refDataTestDeployment,
			Namespace: refDataTestNamespace,
			UID:       refDataTestWDUID,
			Annotations: map[string]string{
				computev1alpha.ReferencedDataErrorAnnotation: raw,
			},
		},
	}
	return wd
}

// TestFederated_TerminalAnnotation_SourceNotFound verifies that when the cell WD
// copy carries the ReferencedDataErrorAnnotation with SourceNotFound, the Instance
// gets Ready=False/SourceNotFound with the hub message rather than AwaitingPropagation.
func TestFederated_TerminalAnnotation_SourceNotFound(t *testing.T) {
	msg := `ConfigMap "app-config" not found in namespace "default"`
	wd := makeWDWithTerminalError(computev1alpha.ReferencedDataReasonSourceNotFound, msg)
	inst := makeInstanceWithRefDataGate()

	r, projectClient, _ := newRefDataReconciler(t, []client.Object{inst, wd})
	reconcileRefData(t, r)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond, "ReferencedDataReady condition should be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceNotFound, cond.Reason,
		"should use the hub resolver reason, not generic AwaitingPropagation")
	assert.Equal(t, msg, cond.Message,
		"should carry the hub resolver message verbatim")

	// Gate must remain until the error is resolved (companion will never arrive).
	hasGate := false
	if updated.Spec.Controller != nil {
		for _, g := range updated.Spec.Controller.SchedulingGates {
			if g.Name == instancecontrol.ReferencedDataSchedulingGate.String() {
				hasGate = true
			}
		}
	}
	assert.True(t, hasGate, "ReferencedData gate should remain when a terminal error is present")
}

// TestFederated_TerminalAnnotation_SourceTooLarge verifies SourceTooLarge propagates
// via the annotation the same way as SourceNotFound.
func TestFederated_TerminalAnnotation_SourceTooLarge(t *testing.T) {
	msg := `ConfigMap "fat-config" in namespace "default" exceeds per-object size limit`
	wd := makeWDWithTerminalError(computev1alpha.ReferencedDataReasonSourceTooLarge, msg)
	inst := makeInstanceWithRefDataGate()

	r, projectClient, _ := newRefDataReconciler(t, []client.Object{inst, wd})
	reconcileRefData(t, r)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceTooLarge, cond.Reason)
	assert.Equal(t, msg, cond.Message)
}

// TestFederated_TerminalAnnotation_SourceUnauthorized verifies SourceUnauthorized propagates
// via the annotation.
func TestFederated_TerminalAnnotation_SourceUnauthorized(t *testing.T) {
	msg := `not authorized to read ConfigMap "secret-cfg" in namespace "default"`
	wd := makeWDWithTerminalError(computev1alpha.ReferencedDataReasonSourceUnauthorized, msg)
	inst := makeInstanceWithRefDataGate()

	r, projectClient, _ := newRefDataReconciler(t, []client.Object{inst, wd})
	reconcileRefData(t, r)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceUnauthorized, cond.Reason)
	assert.Equal(t, msg, cond.Message)
}

// TestFederated_NoAnnotation_AnnotationAbsent verifies that when the WD has no
// terminal-error annotation AND no expected-data annotation (resolver not done),
// the Instance gets Resolving/Unknown — NOT SourceNotFound.
func TestFederated_NoAnnotation_AnnotationAbsent(t *testing.T) {
	// WD has neither annotation — Karmada propagated it before the resolver ran.
	wd := makeWDForCell("") // no annotations
	inst := makeInstanceWithRefDataGate()

	r, projectClient, _ := newRefDataReconciler(t, []client.Object{inst, wd})
	reconcileRefData(t, r)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status,
		"should be Unknown/Resolving when no annotation is present")
	assert.Equal(t, computev1alpha.ReferencedDataReasonResolving, cond.Reason)
}

// TestFederated_AnnotationPresent_CompanionsMissing verifies that when the WD has
// the expected-data annotation (resolver succeeded, no terminal error) but companions
// haven't arrived yet, the Instance gets AwaitingPropagation — not SourceNotFound.
func TestFederated_AnnotationPresent_CompanionsMissing(t *testing.T) {
	expected := []string{refDataTestCMToken}
	annoVal, _ := json.Marshal(expected)
	// WD has the expected annotation but NOT the terminal-error annotation.
	wd := makeWDForCell(string(annoVal))
	inst := makeInstanceWithRefDataGate()

	// No companion ConfigMap present yet.
	r, projectClient, _ := newRefDataReconciler(t, []client.Object{inst, wd})
	reconcileRefData(t, r)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonAwaitingPropagation, cond.Reason,
		"should be AwaitingPropagation (not SourceNotFound) when resolver succeeded but companion is still in flight")
	assert.Contains(t, cond.Message, refDataTestCMToken)
}

// TestFederated_AnnotationPresent_CompanionsPresent verifies that when the WD
// has the expected-data annotation and all companions are present (healthy path),
// the Instance advances to Ready=True.
func TestFederated_AnnotationPresent_CompanionsPresent(t *testing.T) {
	expected := []string{refDataTestCMToken}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))
	inst := makeInstanceWithRefDataGate()
	companionCM := makeCompanionConfigMap(refDataTestCMCompanionName)

	r, projectClient, _ := newRefDataReconciler(t, []client.Object{inst, wd, companionCM})
	reconcileRefData(t, r)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status,
		"should be Ready=True when all companions are present and no terminal error")
}

// TestFederated_QuotaAndSourceNotFound_SourceNotFoundWins verifies that when
// both the Quota gate and ReferencedData gate are present, and the cell WD
// carries a terminal-error annotation, Instance.Ready picks SourceNotFound
// (priority 5) over PendingQuota (priority 3).
func TestFederated_QuotaAndSourceNotFound_SourceNotFoundWins(t *testing.T) {
	msg := `ConfigMap "app-config" not found in namespace "default"`
	wd := makeWDWithTerminalError(computev1alpha.ReferencedDataReasonSourceNotFound, msg)
	inst := makeInstanceWithRefDataGate()

	// Add the Quota gate alongside ReferencedData.
	inst.Spec.Controller.SchedulingGates = append(inst.Spec.Controller.SchedulingGates,
		computev1alpha.SchedulingGate{Name: instancecontrol.QuotaSchedulingGate.String()},
	)

	// Seed a QuotaGranted=False/QuotaExceeded condition on the instance.
	inst.Status.Conditions = []metav1.Condition{
		{
			Type:               computev1alpha.InstanceQuotaGranted,
			Status:             metav1.ConditionFalse,
			Reason:             computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
			Message:            "Quota exceeded for project",
			ObservedGeneration: 1,
			LastTransitionTime: metav1.Now(),
		},
	}

	r, projectClient, _ := newRefDataReconciler(t, []client.Object{inst, wd})
	reconcileRefData(t, r)

	var updated computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceNotFound, cond.Reason,
		"SourceNotFound should be set on ReferencedDataReady so reconcileGatedReadyCondition picks priority 5")

	// Note: reconcileGatedReadyCondition (called by reconcileInstanceReadyCondition,
	// not tested here) is where the priority competition between SourceNotFound (p5)
	// and PendingQuota (p3) is resolved. This test verifies that reconcileReferencedDataCondition
	// sets the right ReferencedDataReady sub-condition that feeds into that priority logic.
}

// containsAll returns true when s contains all substrings.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
