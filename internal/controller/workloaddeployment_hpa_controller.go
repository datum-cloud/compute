// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// WorkloadDeploymentHPAReconciler manages cell-local HorizontalPodAutoscalers
// for WorkloadDeployments that opt into load-driven autoscaling.
type WorkloadDeploymentHPAReconciler struct {
	mgr mcmanager.Manager
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete

func (r *WorkloadDeploymentHPAReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	ctx = mccontext.WithCluster(ctx, req.ClusterName)

	var deployment computev1alpha.WorkloadDeployment
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &deployment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !deployment.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if !workloadDeploymentAutoscalingEnabled(&deployment) {
		return ctrl.Result{}, deleteWorkloadDeploymentHPA(ctx, cl.GetClient(), &deployment)
	}

	logger.Info("reconciling deployment HPA")
	defer logger.Info("deployment HPA reconcile complete")

	metrics, err := workloadDeploymentHPAMetrics(&deployment)
	if err != nil {
		return ctrl.Result{}, err
	}

	hpa := autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployment.Name,
			Namespace: deployment.Namespace,
		},
	}

	_, err = controllerutil.CreateOrPatch(ctx, cl.GetClient(), &hpa, func() error {
		if hpa.UID != "" && !metav1.IsControlledBy(&hpa, &deployment) {
			return fmt.Errorf("HPA %s/%s already exists and is not controlled by WorkloadDeployment %s/%s",
				hpa.Namespace, hpa.Name, deployment.Namespace, deployment.Name)
		}

		if err := controllerutil.SetControllerReference(&deployment, &hpa, cl.GetScheme()); err != nil {
			return err
		}

		hpa.Labels = workloadDeploymentHPALabels(&deployment)

		hpa.Spec = autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: computev1alpha.GroupVersion.String(),
				Kind:       "WorkloadDeployment",
				Name:       deployment.Name,
			},
			MinReplicas: new(deployment.Spec.ScaleSettings.MinReplicas),
			MaxReplicas: *deployment.Spec.ScaleSettings.MaxReplicas,
			Metrics:     metrics,
		}

		return nil
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed reconciling deployment HPA: %w", err)
	}

	return ctrl.Result{}, nil
}

func workloadDeploymentAutoscalingEnabled(deployment *computev1alpha.WorkloadDeployment) bool {
	return deployment.Spec.ScaleSettings.MaxReplicas != nil && len(deployment.Spec.ScaleSettings.Metrics) > 0
}

func deleteWorkloadDeploymentHPA(ctx context.Context, c client.Client, deployment *computev1alpha.WorkloadDeployment) error {
	var hpa autoscalingv2.HorizontalPodAutoscaler
	if err := c.Get(ctx, client.ObjectKey{Namespace: deployment.Namespace, Name: deployment.Name}, &hpa); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed fetching deployment HPA: %w", err)
	}
	if !metav1.IsControlledBy(&hpa, deployment) {
		return nil
	}

	if err := c.Delete(ctx, &hpa); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed deleting deployment HPA: %w", err)
	}

	return nil
}

func workloadDeploymentHPALabels(deployment *computev1alpha.WorkloadDeployment) map[string]string {
	return map[string]string{
		labelServiceName: labelServiceValue,
		computev1alpha.WorkloadDeploymentUIDLabel:  string(deployment.UID),
		computev1alpha.WorkloadDeploymentNameLabel: deployment.Name,
		computev1alpha.WorkloadNameLabel:           deployment.Spec.WorkloadRef.Name,
		computev1alpha.PlacementNameLabel:          deployment.Spec.PlacementName,
		computev1alpha.CityCodeLabel:               deployment.Spec.CityCode,
	}
}

func workloadDeploymentHPAMetrics(deployment *computev1alpha.WorkloadDeployment) ([]autoscalingv2.MetricSpec, error) {
	metrics := make([]autoscalingv2.MetricSpec, 0, len(deployment.Spec.ScaleSettings.Metrics))
	for i, metric := range deployment.Spec.ScaleSettings.Metrics {
		if metric.Resource == nil {
			return nil, fmt.Errorf("metric %d has no resource source", i)
		}

		if metric.Resource.Name != corev1.ResourceCPU && metric.Resource.Name != corev1.ResourceMemory {
			return nil, fmt.Errorf("metric %d uses unsupported resource %q", i, metric.Resource.Name)
		}

		target, err := workloadDeploymentHPAMetricTarget(metric.Resource.Target)
		if err != nil {
			return nil, fmt.Errorf("metric %d has invalid target: %w", i, err)
		}

		metrics = append(metrics, autoscalingv2.MetricSpec{
			Type: autoscalingv2.ResourceMetricSourceType,
			Resource: &autoscalingv2.ResourceMetricSource{
				Name:   metric.Resource.Name,
				Target: target,
			},
		})
	}

	return metrics, nil
}

func workloadDeploymentHPAMetricTarget(target computev1alpha.MetricTarget) (autoscalingv2.MetricTarget, error) {
	setTargets := 0
	if target.Value != nil {
		setTargets++
	}
	if target.AverageValue != nil {
		setTargets++
	}
	if target.AverageUtilization != nil {
		setTargets++
	}
	if setTargets != 1 {
		return autoscalingv2.MetricTarget{}, fmt.Errorf("exactly one target value must be set")
	}

	if target.Value != nil {
		value := target.Value.DeepCopy()
		return autoscalingv2.MetricTarget{Type: autoscalingv2.ValueMetricType, Value: &value}, nil
	}

	if target.AverageValue != nil {
		averageValue := target.AverageValue.DeepCopy()
		return autoscalingv2.MetricTarget{Type: autoscalingv2.AverageValueMetricType, AverageValue: &averageValue}, nil
	}

	return autoscalingv2.MetricTarget{
		Type:               autoscalingv2.UtilizationMetricType,
		AverageUtilization: new(*target.AverageUtilization),
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadDeploymentHPAReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr

	return mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.WorkloadDeployment{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Owns(&autoscalingv2.HorizontalPodAutoscaler{}, mcbuilder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
