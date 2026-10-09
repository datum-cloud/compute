// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const hpaTestScalingLimitReason = "TooManyReplicas"

func readyTestHPA() *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Generation: 3},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			ObservedGeneration: new(int64(3)),
			Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
				{Type: autoscalingv2.AbleToScale, Status: corev1.ConditionTrue, Reason: "ReadyForNewScale", Message: "ready"},
				{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionTrue, Reason: "ValidMetricFound", Message: "CPU metric available"},
				{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionFalse, Reason: "DesiredWithinRange", Message: "within range"},
			},
		},
	}
}

func TestDeploymentAutoscalingConditions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*autoscalingv2.HorizontalPodAutoscaler)
		status metav1.ConditionStatus
		reason string
	}{
		{name: "healthy", status: metav1.ConditionTrue, reason: "AutoscalerReady"},
		{name: "missing metrics", change: func(h *autoscalingv2.HorizontalPodAutoscaler) {
			h.Status.Conditions[1].Status = corev1.ConditionFalse
			h.Status.Conditions[1].Reason = "FailedGetResourceMetric"
		}, status: metav1.ConditionFalse, reason: "FailedGetResourceMetric"},
		{name: "scale target unavailable", change: func(h *autoscalingv2.HorizontalPodAutoscaler) {
			h.Status.Conditions[0].Status = corev1.ConditionFalse
			h.Status.Conditions[0].Reason = "FailedGetScale"
		}, status: metav1.ConditionFalse, reason: "FailedGetScale"},
		{name: "bounds are independent", change: func(h *autoscalingv2.HorizontalPodAutoscaler) {
			h.Status.Conditions[2].Status = corev1.ConditionTrue
			h.Status.Conditions[2].Reason = hpaTestScalingLimitReason
		}, status: metav1.ConditionTrue, reason: "AutoscalerReady"},
		{name: "old settings", change: func(h *autoscalingv2.HorizontalPodAutoscaler) { h.Generation++ }, status: metav1.ConditionUnknown, reason: "AwaitingAutoscaler"},
		{name: "native controller omits generation", change: func(h *autoscalingv2.HorizontalPodAutoscaler) { h.Status.ObservedGeneration = nil }, status: metav1.ConditionTrue, reason: "AutoscalerReady"},
		{name: "no observation yet", change: func(h *autoscalingv2.HorizontalPodAutoscaler) {
			h.Status.ObservedGeneration = nil
			h.Status.Conditions = nil
		}, status: metav1.ConditionUnknown, reason: "AwaitingAutoscaler"},
		{name: "incomplete status", change: func(h *autoscalingv2.HorizontalPodAutoscaler) { h.Status.Conditions = h.Status.Conditions[:1] }, status: metav1.ConditionUnknown, reason: "AwaitingAutoscaler"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hpa := readyTestHPA()
			if tc.change != nil {
				tc.change(hpa)
			}
			deployment := hpaTestDeployment()
			deployment.Generation = 7
			conditions := deploymentAutoscalingConditions(deployment, hpa, nil)
			condition := apimeta.FindStatusCondition(conditions, computev1alpha.AutoscalingReady)
			require.NotNil(t, condition)
			assert.Equal(t, tc.status, condition.Status)
			assert.Equal(t, tc.reason, condition.Reason)
			assert.Equal(t, int64(7), condition.ObservedGeneration)
			if tc.name == "bounds are independent" {
				assert.True(t, apimeta.IsStatusConditionTrue(conditions, computev1alpha.AutoscalingLimited))
			}
			if tc.name == "old settings" || tc.name == "no observation yet" {
				assert.Nil(t, apimeta.FindStatusCondition(conditions, computev1alpha.AutoscalingLimited))
			}
		})
	}
}

func TestWorkloadDeploymentHPAReconciler_StatusLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deployment := hpaTestDeployment()
	deployment.Generation = 7
	deployment.Status.Conditions = []metav1.Condition{{Type: computev1alpha.WorkloadDeploymentAvailable, Status: metav1.ConditionTrue, Reason: "Serving", LastTransitionTime: metav1.Now()}}
	cl := fake.NewClientBuilder().WithScheme(newProjectScheme()).WithObjects(deployment).
		WithStatusSubresource(&computev1alpha.WorkloadDeployment{}, &autoscalingv2.HorizontalPodAutoscaler{}).Build()
	r := newHPAReconciler(cl)
	reconcileHPA(t, r, deployment)
	var stored computev1alpha.WorkloadDeployment
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(deployment), &stored))
	assert.True(t, apimeta.IsStatusConditionPresentAndEqual(stored.Status.Conditions, computev1alpha.AutoscalingReady, metav1.ConditionUnknown))

	var hpa autoscalingv2.HorizontalPodAutoscaler
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(deployment), &hpa))
	hpa.Status = readyTestHPA().Status
	hpa.Status.ObservedGeneration = nil // The native HPA controller omits this optional field.
	hpa.Status.Conditions[1].Status = corev1.ConditionFalse
	hpa.Status.Conditions[1].Reason = "FailedGetResourceMetric"
	hpa.Status.Conditions[1].Message = "CPU metrics are unavailable"
	require.NoError(t, cl.Status().Update(ctx, &hpa))
	reconcileHPA(t, r, deployment)
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(deployment), &stored))
	failed := apimeta.FindStatusCondition(stored.Status.Conditions, computev1alpha.AutoscalingReady)
	require.NotNil(t, failed)
	assert.Equal(t, "FailedGetResourceMetric", failed.Reason)
	assert.True(t, apimeta.IsStatusConditionTrue(stored.Status.Conditions, computev1alpha.WorkloadDeploymentAvailable))

	hpa.Status.Conditions[1] = readyTestHPA().Status.Conditions[1]
	hpa.Status.Conditions[2].Status = corev1.ConditionTrue
	hpa.Status.Conditions[2].Reason = hpaTestScalingLimitReason
	require.NoError(t, cl.Status().Update(ctx, &hpa))
	reconcileHPA(t, r, deployment)
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(deployment), &stored))
	assert.True(t, apimeta.IsStatusConditionTrue(stored.Status.Conditions, computev1alpha.AutoscalingReady))
	assert.True(t, apimeta.IsStatusConditionTrue(stored.Status.Conditions, computev1alpha.AutoscalingLimited))
	resourceVersion := stored.ResourceVersion
	reconcileHPA(t, r, deployment)
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(deployment), &stored))
	assert.Equal(t, resourceVersion, stored.ResourceVersion, "unchanged status must not create a reconcile loop")

	hpa.Status.ObservedGeneration = nil
	hpa.Status.Conditions = nil
	require.NoError(t, cl.Status().Update(ctx, &hpa))
	reconcileHPA(t, r, deployment)
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(deployment), &stored))
	assert.True(t, apimeta.IsStatusConditionPresentAndEqual(stored.Status.Conditions, computev1alpha.AutoscalingReady, metav1.ConditionUnknown))
	assert.Nil(t, apimeta.FindStatusCondition(stored.Status.Conditions, computev1alpha.AutoscalingLimited))

	stored.Spec.ScaleSettings.MaxReplicas = nil
	stored.Generation++
	require.NoError(t, cl.Update(ctx, &stored))
	reconcileHPA(t, r, &stored)
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(deployment), &stored))
	assert.Nil(t, apimeta.FindStatusCondition(stored.Status.Conditions, computev1alpha.AutoscalingReady))
	assert.Nil(t, apimeta.FindStatusCondition(stored.Status.Conditions, computev1alpha.AutoscalingLimited))
	assert.True(t, apimeta.IsStatusConditionTrue(stored.Status.Conditions, computev1alpha.WorkloadDeploymentAvailable))
	assert.True(t, apierrors.IsNotFound(cl.Get(ctx, client.ObjectKeyFromObject(deployment), &hpa)))
}

func TestAutoscalingStatusPreservesConcurrentConditions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deployment := hpaTestDeployment()
	writes := 0
	cl := fake.NewClientBuilder().WithScheme(newProjectScheme()).WithObjects(deployment).
		WithStatusSubresource(deployment).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, subresource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			writes++
			if writes == 1 {
				var concurrent computev1alpha.WorkloadDeployment
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(deployment), &concurrent))
				apimeta.SetStatusCondition(&concurrent.Status.Conditions, metav1.Condition{Type: computev1alpha.WorkloadDeploymentReplicasReady, Status: metav1.ConditionFalse, Reason: "QuotaExceeded", Message: "waiting on quota"})
				require.NoError(t, c.Status().Update(ctx, &concurrent))
				return apierrors.NewConflict(schema.GroupResource{Group: computev1alpha.GroupVersion.Group, Resource: "workloaddeployments"}, deployment.Name, errors.New("concurrent status update"))
			}
			return c.SubResource(subresource).Update(ctx, obj, opts...)
		}}).Build()
	require.NoError(t, updateDeploymentAutoscalingStatus(ctx, cl, deployment, readyTestHPA(), nil))
	var stored computev1alpha.WorkloadDeployment
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(deployment), &stored))
	assert.Equal(t, 2, writes)
	assert.True(t, apimeta.IsStatusConditionTrue(stored.Status.Conditions, computev1alpha.AutoscalingReady))
	assert.Equal(t, "QuotaExceeded", apimeta.FindStatusCondition(stored.Status.Conditions, computev1alpha.WorkloadDeploymentReplicasReady).Reason)

	stored.Generation++
	require.NoError(t, cl.Update(ctx, &stored))
	require.NoError(t, updateDeploymentAutoscalingStatus(ctx, cl, deployment, nil, errors.New("obsolete failure")))
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(deployment), &stored))
	assert.True(t, apimeta.IsStatusConditionTrue(stored.Status.Conditions, computev1alpha.AutoscalingReady), "a reconcile for an older spec must not overwrite newer status")
}

func TestHPAStatusPredicate(t *testing.T) {
	t.Parallel()
	old := readyTestHPA()
	updated := old.DeepCopy()
	updated.Status.Conditions[1].Status = corev1.ConditionFalse
	assert.True(t, hpaStatusPredicate().Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}), "status-only metric failures must reconcile")
	updated = old.DeepCopy()
	updated.Status.ObservedGeneration = nil
	assert.True(t, hpaStatusPredicate().Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}))
	updated = old.DeepCopy()
	updated.Status.CurrentReplicas++
	assert.False(t, hpaStatusPredicate().Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}), "routine readings do not change autoscaling health")
	updated = old.DeepCopy()
	updated.Generation++
	assert.True(t, hpaStatusPredicate().Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}))
}

func TestReconcileWorkloadStatus_Autoscaling(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*computev1alpha.Workload, map[string][]computev1alpha.WorkloadDeployment)
		want   metav1.ConditionStatus
	}{
		{name: "healthy", want: metav1.ConditionTrue},
		{name: "recovery", want: metav1.ConditionTrue, change: func(w *computev1alpha.Workload, ds map[string][]computev1alpha.WorkloadDeployment) {
			failed := metav1.Condition{Type: computev1alpha.AutoscalingReady, Status: metav1.ConditionFalse, Reason: "FailedGetResourceMetric", Message: "old failure"}
			w.Status.Conditions = []metav1.Condition{failed}
			w.Status.Placements = []computev1alpha.WorkloadPlacementStatus{{Name: "primary", Conditions: []metav1.Condition{failed}}}
		}},
		{name: "failure in another location", want: metav1.ConditionFalse, change: func(w *computev1alpha.Workload, ds map[string][]computev1alpha.WorkloadDeployment) {
			failed := *ds["primary"][0].DeepCopy()
			failed.Name = "broken"
			failed.Spec.LocationRef.Name = "other-region"
			apimeta.SetStatusCondition(&failed.Status.Conditions, metav1.Condition{Type: computev1alpha.AutoscalingReady, Status: metav1.ConditionFalse, Reason: "FailedGetResourceMetric", Message: "metrics unavailable", ObservedGeneration: failed.Generation})
			ds["primary"] = append(ds["primary"], failed)
		}},
		{name: "no deployments", want: metav1.ConditionUnknown, change: func(w *computev1alpha.Workload, ds map[string][]computev1alpha.WorkloadDeployment) {
			ds["primary"] = nil
		}},
		{name: "no observed health", want: metav1.ConditionUnknown, change: func(w *computev1alpha.Workload, ds map[string][]computev1alpha.WorkloadDeployment) {
			ds["primary"][0].Status.Conditions = ds["primary"][0].Status.Conditions[:1]
		}},
		{name: "new customer settings", want: metav1.ConditionUnknown, change: func(w *computev1alpha.Workload, ds map[string][]computev1alpha.WorkloadDeployment) {
			w.Spec.Placements[0].ScaleSettings.MaxReplicas = new(int32(12))
		}},
		{name: "new cell generation", want: metav1.ConditionUnknown, change: func(w *computev1alpha.Workload, ds map[string][]computev1alpha.WorkloadDeployment) {
			ds["primary"][0].Status.ObservedGeneration++
		}},
		{name: "disabled before deployment catches up", change: func(w *computev1alpha.Workload, ds map[string][]computev1alpha.WorkloadDeployment) {
			w.Spec.Placements[0].ScaleSettings.MaxReplicas = nil
			previous := ds["primary"][0].Status.Conditions
			w.Status.Conditions = append([]metav1.Condition(nil), previous...)
			w.Status.Placements = []computev1alpha.WorkloadPlacementStatus{{Name: "primary", Conditions: append([]metav1.Condition(nil), previous...)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := hpaTestDeployment()
			deployment.Generation = 7
			deployment.Status.ObservedGeneration = 7
			deployment.Status.Conditions = []metav1.Condition{{Type: computev1alpha.WorkloadDeploymentAvailable, Status: metav1.ConditionTrue, Reason: "Serving"}}
			hpa := readyTestHPA()
			hpa.Status.Conditions[2].Status = corev1.ConditionTrue
			hpa.Status.Conditions[2].Reason = hpaTestScalingLimitReason
			deployment.Status.Conditions = append(deployment.Status.Conditions, deploymentAutoscalingConditions(deployment, hpa, nil)...)
			workload := makeWorkload(11)
			workload.Spec.Placements = []computev1alpha.WorkloadPlacement{{Name: "primary", ScaleSettings: deployment.Spec.ScaleSettings}}
			deployments := map[string][]computev1alpha.WorkloadDeployment{"primary": {*deployment}}
			if tc.change != nil {
				tc.change(workload, deployments)
			}
			available := runReconcileWorkloadStatus(t, workload, deployments)
			condition := apimeta.FindStatusCondition(workload.Status.Conditions, computev1alpha.AutoscalingReady)
			if tc.want == "" {
				assert.Nil(t, condition)
				assert.Nil(t, apimeta.FindStatusCondition(workload.Status.Placements[0].Conditions, computev1alpha.AutoscalingReady))
				assert.Nil(t, apimeta.FindStatusCondition(workload.Status.Conditions, computev1alpha.AutoscalingLimited))
				assert.Nil(t, apimeta.FindStatusCondition(workload.Status.Placements[0].Conditions, computev1alpha.AutoscalingLimited))
				return
			}
			require.NotNil(t, condition)
			assert.Equal(t, tc.want, condition.Status)
			assert.Equal(t, int64(11), condition.ObservedGeneration)
			assert.Equal(t, tc.want, apimeta.FindStatusCondition(workload.Status.Placements[0].Conditions, computev1alpha.AutoscalingReady).Status)
			if tc.name != "no deployments" {
				assert.Equal(t, metav1.ConditionTrue, available.Status, "autoscaling must not redefine availability")
			}
			if tc.name == "healthy" {
				assert.True(t, apimeta.IsStatusConditionTrue(workload.Status.Conditions, computev1alpha.AutoscalingLimited))
			}
			if tc.name == "failure in another location" {
				assert.Contains(t, condition.Message, "primary")
				assert.Contains(t, condition.Message, "other-region")
				assert.Contains(t, condition.Message, "metrics unavailable")
			}
		})
	}
}
