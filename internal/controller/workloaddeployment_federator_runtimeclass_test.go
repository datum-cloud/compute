// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	karmadaclusterv1alpha1 "github.com/karmada-io/api/cluster/v1alpha1"
	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

// testLocationPolicy is the PropagationPolicy name for the test location when
// no runtime class is selected.
const testLocationPolicy = "location-us-west-2"

// These runtime class names are invented rather than the ones the platform
// ships. Propagation must key off whatever class the deployment carries, and
// shipped names would hide a class hard-coded in the federator.
const (
	testClassAzurite = "azurite"
	testClassBasalt  = "basalt"
)

func withRuntimeClass(class string) func(*computev1alpha.WorkloadDeployment) {
	return func(wd *computev1alpha.WorkloadDeployment) {
		wd.Spec.Template.Spec.Runtime.Class = class
	}
}

// testCell returns a Karmada Cluster serving the given runtime classes at the
// given location. A cell is a point-of-presence cluster registered with the
// federation hub.
func testCell(name, location string, classes ...string) *karmadaclusterv1alpha1.Cluster {
	cellLabels := map[string]string{locationLabel: location}
	for _, class := range classes {
		cellLabels[computev1alpha.RuntimeClassServedLabel(class)] = computev1alpha.RuntimeClassServedLabelValue
	}
	return &karmadaclusterv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: cellLabels},
	}
}

// hubSiblingDeployment returns a hub-namespace WorkloadDeployment other than
// the one under test. It carries the labels the federator stamps for the given
// location and runtime class.
func hubSiblingDeployment(location, runtimeClass string) *computev1alpha.WorkloadDeployment {
	wdLabels := map[string]string{locationLabel: location}
	if runtimeClass != "" {
		wdLabels[computev1alpha.RuntimeClassLabel] = runtimeClass
	}
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sibling-deployment",
			Namespace: testKarmadaNSStr,
			Labels:    wdLabels,
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			LocationRef:   locationsv1alpha1.LocationReference{Name: location},
			PlacementName: testDefaultPlacement,
			WorkloadRef:   computev1alpha.WorkloadReference{Name: rdTestWorkloadName},
			ScaleSettings: computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
		},
	}
}

// TestWorkloadDeploymentFederator_ClassAwarePropagation covers the hub copy and
// its PropagationPolicy in three states: the gate off, the gate on with no
// runtime class selected, and the gate on with a runtime class selected. The
// first two states must propagate without a class selector.
func TestWorkloadDeploymentFederator_ClassAwarePropagation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		classesEnabled    bool
		specClass         string
		wantPolicyName    string
		wantWDLabel       string
		wantClusterLabels map[string]string
	}{
		{
			name:              "gate off, class selected — propagates class-blind",
			classesEnabled:    false,
			specClass:         testClassBasalt,
			wantPolicyName:    testLocationPolicy,
			wantWDLabel:       "",
			wantClusterLabels: map[string]string{locationLabel: testFederatorLocation},
		},
		{
			name:              "gate off, no class — propagates class-blind",
			classesEnabled:    false,
			specClass:         "",
			wantPolicyName:    testLocationPolicy,
			wantWDLabel:       "",
			wantClusterLabels: map[string]string{locationLabel: testFederatorLocation},
		},
		{
			name:              "gate on, no class — propagates class-blind",
			classesEnabled:    true,
			specClass:         "",
			wantPolicyName:    testLocationPolicy,
			wantWDLabel:       "",
			wantClusterLabels: map[string]string{locationLabel: testFederatorLocation},
		},
		{
			name:           "gate on, class selected — propagates to cells serving it",
			classesEnabled: true,
			specClass:      testClassBasalt,
			wantPolicyName: "location-us-west-2-class-basalt",
			wantWDLabel:    testClassBasalt,
			wantClusterLabels: map[string]string{
				locationLabel: testFederatorLocation,
				computev1alpha.RuntimeClassServedLabel(testClassBasalt): computev1alpha.RuntimeClassServedLabelValue,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wd := testWorkloadDeployment(withFinalizer, withRuntimeClass(tt.specClass))
			projectClient := newProjectFakeClient(testProjectNamespace(), wd)
			karmadaClient := newKarmadaFakeClient(
				testCell("lax-cell", testFederatorLocation, testClassBasalt),
			)
			r := newTestFederator(projectClient, karmadaClient)
			r.RuntimeClassesEnabled = tt.classesEnabled

			ctx := context.Background()
			_, err := r.Reconcile(ctx, reconcileRequest())
			require.NoError(t, err)

			var karmadaWD computev1alpha.WorkloadDeployment
			require.NoError(t, karmadaClient.Get(ctx, types.NamespacedName{
				Name:      testWDName,
				Namespace: testKarmadaNSStr,
			}, &karmadaWD))
			assert.Equal(t, testFederatorLocation, karmadaWD.Labels[locationLabel])
			assert.Equal(t, tt.wantWDLabel, karmadaWD.Labels[computev1alpha.RuntimeClassLabel])

			var pp karmadapolicyv1alpha1.PropagationPolicy
			require.NoError(t, karmadaClient.Get(ctx, types.NamespacedName{
				Name:      tt.wantPolicyName,
				Namespace: testKarmadaNSStr,
			}, &pp), "PropagationPolicy %q should exist", tt.wantPolicyName)

			// Runtime class narrows which deployments the policy selects, and
			// nothing else. Companions are shared by every deployment in the
			// namespace regardless of class, so they are delivered as
			// dependencies rather than named here — two class policies in one
			// location would otherwise contend for the same companion.
			require.Len(t, pp.Spec.ResourceSelectors, 1)
			assert.True(t, pp.Spec.PropagateDeps)
			wdSel := pp.Spec.ResourceSelectors[0]
			require.NotNil(t, wdSel.LabelSelector)
			assert.Equal(t, testFederatorLocation, wdSel.LabelSelector.MatchLabels[locationLabel])
			assert.Equal(t, tt.wantWDLabel, wdSel.LabelSelector.MatchLabels[computev1alpha.RuntimeClassLabel])

			require.NotNil(t, pp.Spec.Placement.ClusterAffinity)
			require.NotNil(t, pp.Spec.Placement.ClusterAffinity.LabelSelector)
			assert.Equal(t, tt.wantClusterLabels, pp.Spec.Placement.ClusterAffinity.LabelSelector.MatchLabels)
		})
	}
}

// TestCleanupPropagationPolicyIfUnused_PerLocationAndClass verifies the policy is
// removed only when no deployment it propagates remains. A deployment in
// another runtime class must not keep a class policy alive, and a class-labeled
// deployment must not keep the no-class policy alive.
func TestCleanupPropagationPolicyIfUnused_PerLocationAndClass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		classesEnabled bool
		location       string
		runtimeClass   string
		remaining      []client.Object
		wantPPGone     bool
	}{
		{
			name:           "gate off, no siblings — removed",
			classesEnabled: false,
			location:       testFederatorLocation,
			wantPPGone:     true,
		},
		{
			name:           "gate off, location sibling — kept",
			classesEnabled: false,
			location:       testFederatorLocation,
			remaining:      []client.Object{hubSiblingDeployment(testFederatorLocation, "")},
			wantPPGone:     false,
		},
		{
			name:           "same location and class — kept",
			classesEnabled: true,
			location:       testFederatorLocation,
			runtimeClass:   testClassAzurite,
			remaining:      []client.Object{hubSiblingDeployment(testFederatorLocation, testClassAzurite)},
			wantPPGone:     false,
		},
		{
			name:           "same location, other class — removed",
			classesEnabled: true,
			location:       testFederatorLocation,
			runtimeClass:   testClassAzurite,
			remaining:      []client.Object{hubSiblingDeployment(testFederatorLocation, testClassBasalt)},
			wantPPGone:     true,
		},
		{
			name:           "other location, same class — removed",
			classesEnabled: true,
			location:       testFederatorLocation,
			runtimeClass:   testClassAzurite,
			remaining:      []client.Object{hubSiblingDeployment(testWestLocationName, testClassAzurite)},
			wantPPGone:     true,
		},
		{
			name:           "class-blind policy, class-labeled sibling — removed",
			classesEnabled: true,
			location:       testFederatorLocation,
			runtimeClass:   "",
			remaining:      []client.Object{hubSiblingDeployment(testFederatorLocation, testClassAzurite)},
			wantPPGone:     true,
		},
		{
			name:           "class-blind policy, unclassed sibling — kept",
			classesEnabled: true,
			location:       testFederatorLocation,
			runtimeClass:   "",
			remaining:      []client.Object{hubSiblingDeployment(testFederatorLocation, "")},
			wantPPGone:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ppName := propagationPolicyNameFor(tt.location, tt.runtimeClass)
			objs := []client.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testKarmadaNSStr}},
				&karmadapolicyv1alpha1.PropagationPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: ppName, Namespace: testKarmadaNSStr},
				},
			}
			objs = append(objs, tt.remaining...)
			karmadaClient := newKarmadaFakeClient(objs...)

			r := newTestFederator(newProjectFakeClient(testProjectNamespace()), karmadaClient)
			r.RuntimeClassesEnabled = tt.classesEnabled

			ctx := context.Background()
			require.NoError(t, r.cleanupPropagationPolicyIfUnused(ctx, testKarmadaNSStr, tt.location, tt.runtimeClass))

			var pp karmadapolicyv1alpha1.PropagationPolicy
			err := karmadaClient.Get(ctx, types.NamespacedName{Name: ppName, Namespace: testKarmadaNSStr}, &pp)
			if tt.wantPPGone {
				assert.True(t, apierrors.IsNotFound(err), "PropagationPolicy %q should be deleted", ppName)
			} else {
				assert.NoError(t, err, "PropagationPolicy %q should be kept", ppName)
			}
		})
	}
}

// TestWorkloadDeploymentFederator_UnservedRuntimeClassCondition verifies that a
// deployment no cell can serve reports the reason on its own status, rather
// than only on a hub object the customer cannot read.
func TestWorkloadDeploymentFederator_UnservedRuntimeClassCondition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		classesEnabled bool
		specClass      string
		cells          []client.Object
		wantReason     string
	}{
		{
			name:           "no cell at the location serves the class",
			classesEnabled: true,
			specClass:      testClassBasalt,
			cells:          []client.Object{testCell("lax-cell", testFederatorLocation, testClassAzurite)},
			wantReason:     computev1alpha.WorkloadDeploymentReasonRuntimeClassNotServed,
		},
		{
			name:           "the class is served elsewhere, not here",
			classesEnabled: true,
			specClass:      testClassBasalt,
			cells:          []client.Object{testCell("sea-cell", testWestLocationName, testClassBasalt)},
			wantReason:     computev1alpha.WorkloadDeploymentReasonRuntimeClassNotServed,
		},
		{
			name:           "a cell serves the class",
			classesEnabled: true,
			specClass:      testClassBasalt,
			cells:          []client.Object{testCell("lax-cell", testFederatorLocation, testClassBasalt)},
			wantReason:     "",
		},
		{
			name:           "gate off — cells advertise nothing and nothing is refused",
			classesEnabled: false,
			specClass:      testClassBasalt,
			cells:          nil,
			wantReason:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wd := testWorkloadDeployment(withFinalizer, withRuntimeClass(tt.specClass))
			projectClient := newProjectFakeClient(testProjectNamespace(), wd)
			karmadaClient := newKarmadaFakeClient(tt.cells...)
			r := newTestFederator(projectClient, karmadaClient)
			r.RuntimeClassesEnabled = tt.classesEnabled

			ctx := context.Background()
			_, err := r.Reconcile(ctx, reconcileRequest())
			require.NoError(t, err)

			var updated computev1alpha.WorkloadDeployment
			require.NoError(t, projectClient.Get(ctx,
				types.NamespacedName{Name: testWDName, Namespace: testProjNS}, &updated))

			cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.WorkloadDeploymentAvailable)
			if tt.wantReason == "" {
				if cond != nil {
					assert.NotEqual(t, computev1alpha.WorkloadDeploymentReasonRuntimeClassNotServed, cond.Reason)
				}
				return
			}

			require.NotNil(t, cond, "an unplaceable deployment must carry an Available condition")
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
			assert.Contains(t, cond.Message, testFederatorLocation)
			assert.Contains(t, cond.Message, tt.specClass)
			assert.NotContains(t, cond.Message, "Pod")
		})
	}
}

// TestApplyPlacementRefusal_KeepsAvailableDeployment verifies that a deployment
// whose instances are running keeps its existing condition. Observed instance
// state takes precedence over a refusal raised because a cell stopped
// advertising a runtime class.
func TestApplyPlacementRefusal_KeepsAvailableDeployment(t *testing.T) {
	t.Parallel()

	status := &computev1alpha.WorkloadDeploymentStatus{
		Conditions: []metav1.Condition{{
			Type:               computev1alpha.WorkloadDeploymentAvailable,
			Status:             metav1.ConditionTrue,
			Reason:             "InstancesReady",
			LastTransitionTime: metav1.Now(),
		}},
	}
	refusal := &metav1.Condition{
		Type:   computev1alpha.WorkloadDeploymentAvailable,
		Status: metav1.ConditionFalse,
		Reason: computev1alpha.WorkloadDeploymentReasonRuntimeClassNotServed,
	}

	applyPlacementRefusal(status, refusal, 3)

	cond := apimeta.FindStatusCondition(status.Conditions, computev1alpha.WorkloadDeploymentAvailable)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "InstancesReady", cond.Reason)
}
