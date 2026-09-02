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
)

// testCityPolicyLAX is the PropagationPolicy name for the test city when no
// runtime class is selected.
const testCityPolicyLAX = "city-lax"

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

// testCell returns a Karmada Cluster serving the given runtime classes in the
// given city. A cell is a point-of-presence cluster registered with the
// federation hub.
func testCell(name, cityCode string, classes ...string) *karmadaclusterv1alpha1.Cluster {
	cellLabels := map[string]string{cityCodeLabel: cityCode}
	for _, class := range classes {
		cellLabels[computev1alpha.RuntimeClassServedLabel(class)] = computev1alpha.RuntimeClassServedLabelValue
	}
	return &karmadaclusterv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: cellLabels},
	}
}

// hubSiblingDeployment returns a hub-namespace WorkloadDeployment other than
// the one under test. It carries the labels the federator stamps for the given
// city and runtime class.
func hubSiblingDeployment(cityCode, runtimeClass string) *computev1alpha.WorkloadDeployment {
	wdLabels := map[string]string{cityCodeLabel: cityCode}
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
			CityCode:      cityCode,
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
			wantPolicyName:    testCityPolicyLAX,
			wantWDLabel:       "",
			wantClusterLabels: map[string]string{cityCodeLabel: testCityCodeLAX},
		},
		{
			name:              "gate off, no class — propagates class-blind",
			classesEnabled:    false,
			specClass:         "",
			wantPolicyName:    testCityPolicyLAX,
			wantWDLabel:       "",
			wantClusterLabels: map[string]string{cityCodeLabel: testCityCodeLAX},
		},
		{
			name:              "gate on, no class — propagates class-blind",
			classesEnabled:    true,
			specClass:         "",
			wantPolicyName:    testCityPolicyLAX,
			wantWDLabel:       "",
			wantClusterLabels: map[string]string{cityCodeLabel: testCityCodeLAX},
		},
		{
			name:           "gate on, class selected — propagates to cells serving it",
			classesEnabled: true,
			specClass:      testClassBasalt,
			wantPolicyName: "city-lax-class-basalt",
			wantWDLabel:    testClassBasalt,
			wantClusterLabels: map[string]string{
				cityCodeLabel: testCityCodeLAX,
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
				testCell("lax-cell", testCityCodeLAX, testClassBasalt),
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
			assert.Equal(t, testCityCodeLAX, karmadaWD.Labels[cityCodeLabel])
			assert.Equal(t, tt.wantWDLabel, karmadaWD.Labels[computev1alpha.RuntimeClassLabel])

			var pp karmadapolicyv1alpha1.PropagationPolicy
			require.NoError(t, karmadaClient.Get(ctx, types.NamespacedName{
				Name:      tt.wantPolicyName,
				Namespace: testKarmadaNSStr,
			}, &pp), "PropagationPolicy %q should exist", tt.wantPolicyName)

			// The companion selectors ignore runtime class. Companions are
			// shared by every deployment in the namespace.
			require.Len(t, pp.Spec.ResourceSelectors, 3)
			wdSel := pp.Spec.ResourceSelectors[0]
			require.NotNil(t, wdSel.LabelSelector)
			assert.Equal(t, testCityCodeLAX, wdSel.LabelSelector.MatchLabels[cityCodeLabel])
			assert.Equal(t, tt.wantWDLabel, wdSel.LabelSelector.MatchLabels[computev1alpha.RuntimeClassLabel])

			require.NotNil(t, pp.Spec.Placement.ClusterAffinity)
			require.NotNil(t, pp.Spec.Placement.ClusterAffinity.LabelSelector)
			assert.Equal(t, tt.wantClusterLabels, pp.Spec.Placement.ClusterAffinity.LabelSelector.MatchLabels)
		})
	}
}

// TestCleanupPropagationPolicyIfUnused_PerCityAndClass verifies the policy is
// removed only when no deployment it propagates remains. A deployment in
// another runtime class must not keep a class policy alive, and a class-labeled
// deployment must not keep the no-class policy alive.
func TestCleanupPropagationPolicyIfUnused_PerCityAndClass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		classesEnabled bool
		cityCode       string
		runtimeClass   string
		remaining      []client.Object
		wantPPGone     bool
	}{
		{
			name:           "gate off, no siblings — removed",
			classesEnabled: false,
			cityCode:       testCityCodeLAX,
			wantPPGone:     true,
		},
		{
			name:           "gate off, city sibling — kept",
			classesEnabled: false,
			cityCode:       testCityCodeLAX,
			remaining:      []client.Object{hubSiblingDeployment(testCityCodeLAX, "")},
			wantPPGone:     false,
		},
		{
			name:           "same city and class — kept",
			classesEnabled: true,
			cityCode:       testCityCodeLAX,
			runtimeClass:   testClassAzurite,
			remaining:      []client.Object{hubSiblingDeployment(testCityCodeLAX, testClassAzurite)},
			wantPPGone:     false,
		},
		{
			name:           "same city, other class — removed",
			classesEnabled: true,
			cityCode:       testCityCodeLAX,
			runtimeClass:   testClassAzurite,
			remaining:      []client.Object{hubSiblingDeployment(testCityCodeLAX, testClassBasalt)},
			wantPPGone:     true,
		},
		{
			name:           "other city, same class — removed",
			classesEnabled: true,
			cityCode:       testCityCodeLAX,
			runtimeClass:   testClassAzurite,
			remaining:      []client.Object{hubSiblingDeployment("SEA", testClassAzurite)},
			wantPPGone:     true,
		},
		{
			name:           "class-blind policy, class-labeled sibling — removed",
			classesEnabled: true,
			cityCode:       testCityCodeLAX,
			runtimeClass:   "",
			remaining:      []client.Object{hubSiblingDeployment(testCityCodeLAX, testClassAzurite)},
			wantPPGone:     true,
		},
		{
			name:           "class-blind policy, unclassed sibling — kept",
			classesEnabled: true,
			cityCode:       testCityCodeLAX,
			runtimeClass:   "",
			remaining:      []client.Object{hubSiblingDeployment(testCityCodeLAX, "")},
			wantPPGone:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ppName := propagationPolicyNameFor(tt.cityCode, tt.runtimeClass)
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
			require.NoError(t, r.cleanupPropagationPolicyIfUnused(ctx, testKarmadaNSStr, tt.cityCode, tt.runtimeClass))

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
			name:           "no cell in the city serves the class",
			classesEnabled: true,
			specClass:      testClassBasalt,
			cells:          []client.Object{testCell("lax-cell", testCityCodeLAX, testClassAzurite)},
			wantReason:     computev1alpha.WorkloadDeploymentReasonRuntimeClassNotServed,
		},
		{
			name:           "the class is served elsewhere, not here",
			classesEnabled: true,
			specClass:      testClassBasalt,
			cells:          []client.Object{testCell("sea-cell", "SEA", testClassBasalt)},
			wantReason:     computev1alpha.WorkloadDeploymentReasonRuntimeClassNotServed,
		},
		{
			name:           "a cell serves the class",
			classesEnabled: true,
			specClass:      testClassBasalt,
			cells:          []client.Object{testCell("lax-cell", testCityCodeLAX, testClassBasalt)},
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
			assert.Contains(t, cond.Message, testCityCodeLAX)
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
