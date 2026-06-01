// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
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
			Name:      "test-wd",
			Namespace: "default",
			UID:       "wd-uid-test",
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode:      "DFW",
			PlacementName: testDefaultPlacement,
			WorkloadRef:   computev1alpha.WorkloadReference{Name: "test-workload"},
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
			Namespace: "default",
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: "wd-uid-test",
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
			Namespace: "default",
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: "wd-uid-test",
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
			Namespace: "default",
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: "wd-uid-test",
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
	currentReplicas, readyReplicas, quotaBlockedReplicas, referencedDataBlockedReplicas, err := r.reconcileInstanceGates(
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
