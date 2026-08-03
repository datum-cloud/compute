// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	hpaTestCluster = "test-cluster"
	hpaTestUID     = "existing-hpa"
)

func hpaTestDeployment() *computev1alpha.WorkloadDeployment {
	averageUtilization := int32(75)
	maxReplicas := int32(10)
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
				MinReplicas: 2,
				MaxReplicas: new(maxReplicas),
				Metrics: []computev1alpha.MetricSpec{
					{
						Resource: &computev1alpha.ResourceMetricSource{
							Name: corev1.ResourceCPU,
							Target: computev1alpha.MetricTarget{
								AverageUtilization: new(averageUtilization),
							},
						},
					},
				},
				InstanceManagementPolicy: computev1alpha.OrderedReadyInstanceManagementPolicyType,
			},
		},
	}
}

func newHPAReconciler(cl client.Client) *WorkloadDeploymentHPAReconciler {
	return &WorkloadDeploymentHPAReconciler{
		mgr: newFakeMCManager(hpaTestCluster, newFakeCluster(cl)),
	}
}

func reconcileHPA(t *testing.T, r *WorkloadDeploymentHPAReconciler, deployment *computev1alpha.WorkloadDeployment) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), mcreconcile.Request{
		ClusterName: multicluster.ClusterName(hpaTestCluster),
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}},
	})
	require.NoError(t, err)
}

func TestWorkloadDeploymentHPAReconciler_CreatesHPA(t *testing.T) {
	t.Parallel()

	deployment := hpaTestDeployment()
	cl := newProjectFakeClient(deployment)
	reconcileHPA(t, newHPAReconciler(cl), deployment)

	var hpa autoscalingv2.HorizontalPodAutoscaler
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(deployment), &hpa))

	assert.Equal(t, computev1alpha.GroupVersion.String(), hpa.Spec.ScaleTargetRef.APIVersion)
	assert.Equal(t, "WorkloadDeployment", hpa.Spec.ScaleTargetRef.Kind)
	assert.Equal(t, deployment.Name, hpa.Spec.ScaleTargetRef.Name)
	require.NotNil(t, hpa.Spec.MinReplicas)
	assert.Equal(t, int32(2), *hpa.Spec.MinReplicas)
	assert.Equal(t, int32(10), hpa.Spec.MaxReplicas)

	require.Len(t, hpa.Spec.Metrics, 1)
	assert.Equal(t, autoscalingv2.ResourceMetricSourceType, hpa.Spec.Metrics[0].Type)
	require.NotNil(t, hpa.Spec.Metrics[0].Resource)
	assert.Equal(t, corev1.ResourceCPU, hpa.Spec.Metrics[0].Resource.Name)
	assert.Equal(t, autoscalingv2.UtilizationMetricType, hpa.Spec.Metrics[0].Resource.Target.Type)
	require.NotNil(t, hpa.Spec.Metrics[0].Resource.Target.AverageUtilization)
	assert.Equal(t, int32(75), *hpa.Spec.Metrics[0].Resource.Target.AverageUtilization)

	assert.Equal(t, map[string]string{
		labelServiceName: labelServiceValue,
		computev1alpha.WorkloadDeploymentUIDLabel:  string(deployment.UID),
		computev1alpha.WorkloadDeploymentNameLabel: deployment.Name,
		computev1alpha.WorkloadNameLabel:           deployment.Spec.WorkloadRef.Name,
		computev1alpha.PlacementNameLabel:          deployment.Spec.PlacementName,
		computev1alpha.CityCodeLabel:               deployment.Spec.CityCode,
	}, hpa.Labels)
	require.Len(t, hpa.OwnerReferences, 1)
	assert.Equal(t, deployment.Name, hpa.OwnerReferences[0].Name)
	assert.True(t, *hpa.OwnerReferences[0].Controller)
}

func TestWorkloadDeploymentHPAReconciler_UpdatesHPA(t *testing.T) {
	t.Parallel()

	deployment := hpaTestDeployment()
	existing := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployment.Name,
			Namespace: deployment.Namespace,
			UID:       hpaTestUID,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(deployment, computev1alpha.GroupVersion.WithKind("WorkloadDeployment")),
			},
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "old"},
			MinReplicas:    new(int32(1)),
			MaxReplicas:    1,
		},
	}
	cl := newProjectFakeClient(deployment, existing)
	reconcileHPA(t, newHPAReconciler(cl), deployment)

	var hpa autoscalingv2.HorizontalPodAutoscaler
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(deployment), &hpa))
	assert.Equal(t, computev1alpha.GroupVersion.String(), hpa.Spec.ScaleTargetRef.APIVersion)
	assert.Equal(t, "WorkloadDeployment", hpa.Spec.ScaleTargetRef.Kind)
	assert.Equal(t, int32(10), hpa.Spec.MaxReplicas)
}

func TestWorkloadDeploymentHPAReconciler_DoesNotAdoptUnownedHPA(t *testing.T) {
	t.Parallel()

	deployment := hpaTestDeployment()
	existing := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: deployment.Name, Namespace: deployment.Namespace, UID: hpaTestUID},
	}
	cl := newProjectFakeClient(deployment, existing)
	r := newHPAReconciler(cl)

	_, err := r.Reconcile(context.Background(), mcreconcile.Request{
		ClusterName: multicluster.ClusterName(hpaTestCluster),
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}},
	})
	require.Error(t, err)

	var hpa autoscalingv2.HorizontalPodAutoscaler
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(deployment), &hpa))
	assert.Empty(t, hpa.OwnerReferences)
}

func TestWorkloadDeploymentHPAReconciler_DeletesHPAWhenAutoscalingDisabled(t *testing.T) {
	t.Parallel()

	deployment := hpaTestDeployment()
	deployment.Spec.ScaleSettings.MaxReplicas = nil
	existing := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployment.Name,
			Namespace: deployment.Namespace,
			UID:       hpaTestUID,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(deployment, computev1alpha.GroupVersion.WithKind("WorkloadDeployment")),
			},
		},
	}
	cl := newProjectFakeClient(deployment, existing)
	reconcileHPA(t, newHPAReconciler(cl), deployment)

	var hpa autoscalingv2.HorizontalPodAutoscaler
	err := cl.Get(context.Background(), client.ObjectKeyFromObject(deployment), &hpa)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestWorkloadDeploymentHPAReconciler_DoesNotDeleteUnownedHPA(t *testing.T) {
	t.Parallel()

	deployment := hpaTestDeployment()
	deployment.Spec.ScaleSettings.MaxReplicas = nil
	existing := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: deployment.Name, Namespace: deployment.Namespace, UID: hpaTestUID},
	}
	cl := newProjectFakeClient(deployment, existing)
	reconcileHPA(t, newHPAReconciler(cl), deployment)

	var hpa autoscalingv2.HorizontalPodAutoscaler
	require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(deployment), &hpa))
	assert.Empty(t, hpa.OwnerReferences)
}

func TestWorkloadDeploymentHPAMetrics(t *testing.T) {
	t.Parallel()

	value := resource.MustParse("100m")
	averageValue := resource.MustParse("256Mi")
	averageUtilization := int32(80)

	tests := []struct {
		name       string
		metrics    []computev1alpha.MetricSpec
		wantType   autoscalingv2.MetricTargetType
		wantErr    bool
		wantMetric corev1.ResourceName
	}{
		{
			name: "value",
			metrics: []computev1alpha.MetricSpec{{Resource: &computev1alpha.ResourceMetricSource{
				Name:   corev1.ResourceCPU,
				Target: computev1alpha.MetricTarget{Value: &value},
			}}},
			wantType:   autoscalingv2.ValueMetricType,
			wantMetric: corev1.ResourceCPU,
		},
		{
			name: "average value",
			metrics: []computev1alpha.MetricSpec{{Resource: &computev1alpha.ResourceMetricSource{
				Name:   corev1.ResourceMemory,
				Target: computev1alpha.MetricTarget{AverageValue: &averageValue},
			}}},
			wantType:   autoscalingv2.AverageValueMetricType,
			wantMetric: corev1.ResourceMemory,
		},
		{
			name: "average utilization",
			metrics: []computev1alpha.MetricSpec{{Resource: &computev1alpha.ResourceMetricSource{
				Name:   corev1.ResourceCPU,
				Target: computev1alpha.MetricTarget{AverageUtilization: &averageUtilization},
			}}},
			wantType:   autoscalingv2.UtilizationMetricType,
			wantMetric: corev1.ResourceCPU,
		},
		{
			name:    "missing resource",
			metrics: []computev1alpha.MetricSpec{{}},
			wantErr: true,
		},
		{
			name: "unsupported resource",
			metrics: []computev1alpha.MetricSpec{{Resource: &computev1alpha.ResourceMetricSource{
				Name:   corev1.ResourceEphemeralStorage,
				Target: computev1alpha.MetricTarget{AverageUtilization: &averageUtilization},
			}}},
			wantErr: true,
		},
		{
			name: "multiple targets",
			metrics: []computev1alpha.MetricSpec{{Resource: &computev1alpha.ResourceMetricSource{
				Name: corev1.ResourceCPU,
				Target: computev1alpha.MetricTarget{
					Value:              &value,
					AverageUtilization: &averageUtilization,
				},
			}}},
			wantErr: true,
		},
		{
			name: "missing target",
			metrics: []computev1alpha.MetricSpec{{Resource: &computev1alpha.ResourceMetricSource{
				Name: corev1.ResourceCPU,
			}}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deployment := hpaTestDeployment()
			deployment.Spec.ScaleSettings.Metrics = tt.metrics

			metrics, err := workloadDeploymentHPAMetrics(deployment)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Len(t, metrics, 1)
			require.NotNil(t, metrics[0].Resource)
			assert.Equal(t, tt.wantMetric, metrics[0].Resource.Name)
			assert.Equal(t, tt.wantType, metrics[0].Resource.Target.Type)
		})
	}
}
