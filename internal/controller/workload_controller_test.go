// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// newTestLocationBinding builds the projection a project control plane holds
// today for a location it may place workloads at.
func newTestLocationBinding(name, cityCode string) *networkingv1alpha.LocationBinding {
	return &networkingv1alpha.LocationBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: networkingv1alpha.LocationBindingSpec{
			LocationRef: corev1.LocalObjectReference{Name: name},
			Topology:    map[string]string{networkingv1alpha.TopologyCityCodeKey: cityCode},
		},
	}
}

// makeWorkload builds a Workload with the given generation for use in
// reconcileWorkloadStatus unit tests.
func makeWorkload(generation int64) *computev1alpha.Workload {
	return &computev1alpha.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:       rdTestWorkloadName,
			Namespace:  testDefaultNamespace,
			Generation: generation,
		},
	}
}

// makeWDWithAvailCond builds a WorkloadDeployment whose Available condition
// reflects the supplied status, reason, and message. Used to construct
// the placementDeployments map for reconcileWorkloadStatus tests.
func makeWDWithAvailCond(name string, status metav1.ConditionStatus, reason, message string) computev1alpha.WorkloadDeployment {
	d := computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testDefaultNamespace,
		},
	}
	apimeta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{
		Type:               workloadConditionTypeAvailable,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
	return d
}

// runReconcileWorkloadStatus is a minimal harness that calls reconcileWorkloadStatus
// directly, using a fake client, and returns the resulting Available condition from
// the workload's updated status.
//
// The workload is stored in the fake client via Create, then fetched back so that
// the reconciler's Status().Update call has a matching resourceVersion. The
// in-memory workload is updated in-place so callers can inspect all fields after
// the call returns.
func runReconcileWorkloadStatus(t *testing.T, workload *computev1alpha.Workload, placements map[string][]computev1alpha.WorkloadDeployment) *metav1.Condition {
	t.Helper()
	ctx := context.Background()
	s := newTestScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&computev1alpha.Workload{}).
		Build()

	// Store via Create so the fake client assigns a resourceVersion.
	require.NoError(t, cl.Create(ctx, workload.DeepCopy()))

	// Fetch back to get the server-assigned resourceVersion.
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(workload), workload))

	r := &WorkloadReconciler{}
	err := r.reconcileWorkloadStatus(ctx, cl, workload, placements)
	require.NoError(t, err, "reconcileWorkloadStatus must not return an error")

	cond := apimeta.FindStatusCondition(workload.Status.Conditions, workloadConditionTypeAvailable)
	return cond
}

func TestGetDeploymentsForWorkload_InitializesReplicas(t *testing.T) {
	t.Parallel()

	workload := &computev1alpha.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rdTestWorkloadName,
			Namespace: testDefaultNamespace,
			UID:       types.UID("workload-uid"),
		},
		Spec: computev1alpha.WorkloadSpec{
			Placements: []computev1alpha.WorkloadPlacement{
				{
					Name:      testDefaultPlacement,
					CityCodes: []string{"DFW"},
					ScaleSettings: computev1alpha.HorizontalScaleSettings{
						MinReplicas: 2,
					},
				},
			},
		},
	}
	location := newTestLocationBinding("dfw", "DFW")

	s := newNetworkingScheme()
	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(location).
		WithIndex(&computev1alpha.WorkloadDeployment{}, deploymentWorkloadUIDIndex, deploymentWorkloadUIDIndexFunc).
		Build()
	r := &WorkloadReconciler{}

	desired, orphaned, err := r.getDeploymentsForWorkload(context.Background(), cl, workload)
	require.NoError(t, err)
	require.Empty(t, orphaned)
	require.Len(t, desired, 1)
	require.NotNil(t, desired[0].Spec.Replicas)
	assert.Equal(t, int32(2), *desired[0].Spec.Replicas)
}

// TestReconcileWorkloadStatus_AllDeploymentsSameReason verifies that when all
// deployments share the same blocking reason, that reason is propagated to the
// Workload Available condition with ObservedGeneration set correctly.
func TestReconcileWorkloadStatus_AllDeploymentsSameReason(t *testing.T) {
	const gen = int64(3)
	workload := makeWorkload(gen)
	msg := testMsgConfigMapNotFound
	placements := map[string][]computev1alpha.WorkloadDeployment{
		testPlacementA: {
			makeWDWithAvailCond("wd-1", metav1.ConditionFalse,
				computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, msg),
			makeWDWithAvailCond("wd-2", metav1.ConditionFalse,
				computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, msg),
		},
	}

	cond := runReconcileWorkloadStatus(t, workload, placements)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, cond.Reason)
	assert.Equal(t, msg, cond.Message)
	assert.Equal(t, gen, cond.ObservedGeneration, "ObservedGeneration must match workload generation")
}

// TestReconcileWorkloadStatus_MixedReasons verifies that when deployments have
// different blocking reasons the highest-priority one is surfaced.
func TestReconcileWorkloadStatus_MixedReasons(t *testing.T) {
	workload := makeWorkload(1)
	placements := map[string][]computev1alpha.WorkloadDeployment{
		testPlacementA: {
			makeWDWithAvailCond("wd-low", metav1.ConditionFalse,
				computev1alpha.WorkloadDeploymentReasonInstancesProvisioning, // priority 1
				"Instances are being provisioned"),
			makeWDWithAvailCond("wd-high", metav1.ConditionFalse,
				computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, // priority 3
				"1 of 2 desired instances pending quota"),
		},
	}

	cond := runReconcileWorkloadStatus(t, workload, placements)
	require.NotNil(t, cond)
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, cond.Reason,
		"QuotaNotGranted (priority 3) must beat InstancesProvisioning (priority 1)")
}

// TestReconcileWorkloadStatus_OneAvailableDeployment verifies that when at
// least one deployment is Available=True, the Workload reports Available=True
// regardless of other deployments' blocking reasons.
func TestReconcileWorkloadStatus_OneAvailableDeployment(t *testing.T) {
	workload := makeWorkload(1)
	placements := map[string][]computev1alpha.WorkloadDeployment{
		testPlacementA: {
			makeWDWithAvailCond("wd-blocked", metav1.ConditionFalse,
				computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady,
				`ConfigMap "X" not found`),
			makeWDWithAvailCond("wd-ready", metav1.ConditionTrue,
				computev1alpha.WorkloadDeploymentReasonStableInstanceFound,
				"1/1 instances are ready"),
		},
	}

	cond := runReconcileWorkloadStatus(t, workload, placements)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status,
		"one available deployment makes the workload available")
	assert.Equal(t, "AvailablePlacementFound", cond.Reason)
}

// TestReconcileWorkloadStatus_NoDeployments verifies that an empty
// placementDeployments map produces NoAvailablePlacements.
func TestReconcileWorkloadStatus_NoDeployments(t *testing.T) {
	workload := makeWorkload(1)
	cond := runReconcileWorkloadStatus(t, workload, map[string][]computev1alpha.WorkloadDeployment{})
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.WorkloadReasonNoAvailablePlacements, cond.Reason)
}

// TestReconcileWorkloadStatus_TiebreakerByName verifies that when two deployments
// have the same blocking reason and equal priority, the lexicographically earlier
// name wins (deterministic tie-break).
func TestReconcileWorkloadStatus_TiebreakerByName(t *testing.T) {
	workload := makeWorkload(1)
	placements := map[string][]computev1alpha.WorkloadDeployment{
		testPlacementA: {
			// "wd-b" sorts after "wd-a"; "wd-a" wins as the lex-first match.
			makeWDWithAvailCond("wd-b", metav1.ConditionFalse,
				computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady,
				"message from wd-b"),
			makeWDWithAvailCond("wd-a", metav1.ConditionFalse,
				computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady,
				"message from wd-a"),
		},
	}

	cond := runReconcileWorkloadStatus(t, workload, placements)
	require.NotNil(t, cond)
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, cond.Reason)
	assert.Equal(t, "message from wd-a", cond.Message,
		"lexicographically earlier deployment name wins the tie-break")
}

// TestReconcileWorkloadStatus_ObservedGeneration verifies that the Available
// condition on Workload carries ObservedGeneration equal to workload.Generation.
func TestReconcileWorkloadStatus_ObservedGeneration(t *testing.T) {
	const gen = int64(7)
	workload := makeWorkload(gen)
	placements := map[string][]computev1alpha.WorkloadDeployment{
		"p": {
			makeWDWithAvailCond("wd", metav1.ConditionFalse,
				computev1alpha.WorkloadDeploymentReasonInstancesProvisioning,
				"Instances are being provisioned"),
		},
	}

	cond := runReconcileWorkloadStatus(t, workload, placements)
	require.NotNil(t, cond)
	assert.Equal(t, gen, cond.ObservedGeneration,
		"Available condition ObservedGeneration must equal workload.Generation")
}

// TestMergeDeploymentMetadata_PreservesForeignAnnotation verifies that merging
// the controller-owned desired metadata onto an existing WorkloadDeployment
// writes the desired keys while preserving peer-owned metadata — in particular
// the referenced-data controller's expected-referenced-data annotation. A blind
// map overwrite here strips that annotation and drives the reconcile hot-loop
// described in issue #191, so this test fails if the merge is regressed to an
// overwrite.
func TestMergeDeploymentMetadata_PreservesForeignAnnotation(t *testing.T) {
	const foreignVal = `["ConfigMap/app-config"]`

	// The live object as it exists after the referenced-data controller has
	// stamped its annotation and Karmada has stamped a bookkeeping label.
	existing := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wd-1",
			Namespace: testDefaultNamespace,
			Labels: map[string]string{
				"work.karmada.io/managed": "true",
			},
			Annotations: map[string]string{
				computev1alpha.ExpectedReferencedDataAnnotation: foreignVal,
			},
		},
	}

	// The desired object the workload controller builds carries only its own
	// ownership label and no annotations.
	desired := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				computev1alpha.WorkloadUIDLabel: "workload-uid-123",
			},
		},
	}

	mergeDeploymentMetadata(existing, desired)

	// Desired controller-owned key is written.
	assert.Equal(t, "workload-uid-123", existing.Labels[computev1alpha.WorkloadUIDLabel],
		"desired WorkloadUIDLabel must be written")

	// Peer-owned annotation and label survive.
	assert.Equal(t, foreignVal, existing.Annotations[computev1alpha.ExpectedReferencedDataAnnotation],
		"expected-referenced-data annotation must be preserved across merge")
	assert.Equal(t, "true", existing.Labels["work.karmada.io/managed"],
		"federation-hub bookkeeping label must be preserved across merge")
}

// TestWorkloadBlockingReasonPriority exhaustively verifies every entry in the
// workloadBlockingReasonPriority switch statement.
func TestWorkloadBlockingReasonPriority(t *testing.T) {
	tests := []struct {
		reason   string
		wantPrio int
	}{
		// Priority 0: default / fallback
		{"", 0},
		{"Unknown", 0},
		{computev1alpha.WorkloadReasonNoAvailablePlacements, 0},
		{computev1alpha.WorkloadReasonNoAvailableDeployments, 0},
		// Priority 1
		{computev1alpha.WorkloadDeploymentReasonInstancesProvisioning, 1},
		// Priority 2
		{computev1alpha.WorkloadDeploymentReasonNetworkProvisioning, 2},
		// Priority 3
		{computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, 3},
		{computev1alpha.InstanceProgrammedReasonPendingQuota, 3},
		// Priority 4
		{computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, 4},
		{computev1alpha.ReferencedDataReasonAwaitingPropagation, 4},
		{computev1alpha.ReferencedDataReasonResolving, 4},
		// Priority 5
		{computev1alpha.ReferencedDataReasonSourceNotFound, 5},
		{computev1alpha.ReferencedDataReasonSourceTooLarge, 5},
		{computev1alpha.ReferencedDataReasonSourceUnauthorized, 5},
		// Priority 6
		{computev1alpha.WorkloadReasonNetworkNotFound, 6},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			assert.Equal(t, tt.wantPrio, workloadBlockingReasonPriority(tt.reason),
				"unexpected priority for reason %q", tt.reason)
		})
	}
}
