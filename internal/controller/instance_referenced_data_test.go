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
)

// makeWDForCell builds a WorkloadDeployment that can own test instances in the
// cell-side gate-clearing tests. Named distinctly to avoid collision with the
// makeWD helper in referenceddata_controller_test.go.
func makeWDForCell(annotationValue string) *computev1alpha.WorkloadDeployment {
	wd := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      refDataTestDeployment,
			Namespace: refDataTestNamespace,
			UID:       "wd-uid",
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
			Finalizers: []string{instanceQuotaFinalizer},
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: "wd-uid",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "compute.datumapis.com/v1alpha",
					Kind:       "WorkloadDeployment",
					Name:       refDataTestDeployment,
					UID:        "wd-uid",
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Controller: &computev1alpha.InstanceController{
				SchedulingGates: gates,
			},
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: "d1-standard-2"},
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
				computev1alpha.ReferencedDataLabel: "true",
			},
		},
		Data: map[string]string{"key": "value"},
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
				computev1alpha.ReferencedDataLabel: "true",
			},
		},
		Data: map[string][]byte{"key": []byte("value")},
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

	mgmtClient := fake.NewClientBuilder().
		WithScheme(s).
		Build()

	mgr := &fakeMCManager{
		clusters: map[string]cluster.Cluster{
			refDataTestCluster: &fakeCluster{client: projectClient, scheme: s},
		},
	}

	fakeRec := record.NewFakeRecorder(32)
	r := &InstanceReconciler{
		mgr:               mgr,
		managementCluster: &fakeCluster{client: mgmtClient, scheme: s},
		recorder:          fakeRec,
	}
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
	expected := []string{"configmap.app-config", "secret.db-creds"}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))
	inst := makeInstanceWithRefDataGate()

	// Only the ConfigMap companion is present; the Secret is missing.
	companionCM := makeCompanionConfigMap("configmap.app-config")

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
	assert.Contains(t, cond.Message, "secret.db-creds", "message should name the missing companion")

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
	expected := []string{"configmap.app-config", "secret.db-creds"}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))
	inst := makeInstanceWithRefDataGate()

	companionCM := makeCompanionConfigMap("configmap.app-config")
	companionSecret := makeCompanionSecret("secret.db-creds")

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
	expected := []string{"configmap.app-config"}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))

	// Instance with no gate (already cleared) and Ready condition already set.
	inst := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       refDataTestInstance,
			Namespace:  refDataTestNamespace,
			Finalizers: []string{instanceQuotaFinalizer},
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: "wd-uid",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "compute.datumapis.com/v1alpha",
					Kind:       "WorkloadDeployment",
					Name:       refDataTestDeployment,
					UID:        "wd-uid",
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Controller: &computev1alpha.InstanceController{
				SchedulingGates: []computev1alpha.SchedulingGate{},
			},
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: "d1-standard-2"},
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

	companionCM := makeCompanionConfigMap("configmap.app-config")

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
	expected := []string{"configmap.cfg-a", "configmap.cfg-b", "secret.sec-x"}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))
	inst := makeInstanceWithRefDataGate()

	// Only the first ConfigMap is present.
	companionA := makeCompanionConfigMap("configmap.cfg-a")

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

	// Both missing companions should be mentioned in the message.
	assert.Contains(t, cond.Message, "configmap.cfg-b")
	assert.Contains(t, cond.Message, "secret.sec-x")
	// The present one should NOT be mentioned as missing.
	assert.NotContains(t, cond.Message, "configmap.cfg-a")
}

// TestReferencedDataEventEmittedOnClear verifies that a Normal event is emitted
// precisely once when the gate transitions from present to cleared.
func TestReferencedDataEventEmittedOnClear(t *testing.T) {
	expected := []string{"configmap.my-config"}
	annoVal, _ := json.Marshal(expected)
	wd := makeWDForCell(string(annoVal))
	inst := makeInstanceWithRefDataGate()
	companion := makeCompanionConfigMap("configmap.my-config")

	r, projectClient, fakeRec := newRefDataReconciler(t,
		[]client.Object{inst, wd, companion},
	)

	// Pass 1: set condition to Ready, status updated, return early — no gate patch yet.
	reconcileRefData(t, r)

	var afterStatus computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &afterStatus))
	cond := apimeta.FindStatusCondition(afterStatus.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	// Drain any events from pass 1 (there shouldn't be any before the gate patch).
	drainEvents(fakeRec.Events)

	// Pass 2: status already True → patch the gate away → emit event.
	reconcileRefData(t, r)

	var cleared computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Namespace: refDataTestNamespace, Name: refDataTestInstance}, &cleared))

	hasGate := false
	for _, g := range cleared.Spec.Controller.SchedulingGates {
		if g.Name == instancecontrol.ReferencedDataSchedulingGate.String() {
			hasGate = true
		}
	}
	assert.False(t, hasGate, "gate should be cleared on second pass")

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

// drainEvents consumes all pending events from the channel without blocking.
func drainEvents(ch <-chan string) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
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
