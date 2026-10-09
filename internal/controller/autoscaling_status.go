// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"fmt"
	"sort"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	reasonAwaitingAutoscaler = "AwaitingAutoscaler"
	reasonAutoscalerReady    = "AutoscalerReady"
)

var autoscalingConditionTypes = []string{
	computev1alpha.AutoscalingReady,
	computev1alpha.AutoscalingLimited,
}

func pendingAutoscalingCondition(generation int64) metav1.Condition {
	return metav1.Condition{
		Type: computev1alpha.AutoscalingReady, Status: metav1.ConditionUnknown,
		Reason: reasonAwaitingAutoscaler, Message: "Waiting for the autoscaler to evaluate the current scaling settings",
		ObservedGeneration: generation,
	}
}

func deploymentAutoscalingConditions(deployment *computev1alpha.WorkloadDeployment, hpa *autoscalingv2.HorizontalPodAutoscaler, reconcileErr error) []metav1.Condition {
	summary := pendingAutoscalingCondition(deployment.Generation)
	if reconcileErr != nil {
		summary.Status = metav1.ConditionFalse
		summary.Reason = "AutoscalerReconcileFailed"
		summary.Message = reconcileErr.Error()
		return []metav1.Condition{summary}
	}
	// Kubernetes' HPA controller normally omits observedGeneration. Treat an
	// explicitly stale observation as pending, but accept reported conditions
	// when this optional field is absent; empty conditions still remain pending.
	if hpa == nil || (hpa.Status.ObservedGeneration != nil && *hpa.Status.ObservedGeneration < hpa.Generation) {
		return []metav1.Condition{summary}
	}

	conditions := []metav1.Condition{}
	byType := map[autoscalingv2.HorizontalPodAutoscalerConditionType]autoscalingv2.HorizontalPodAutoscalerCondition{}
	for _, condition := range hpa.Status.Conditions {
		byType[condition.Type] = condition
		if condition.Type == autoscalingv2.ScalingLimited {
			conditions = append(conditions, metav1.Condition{
				Type: computev1alpha.AutoscalingLimited, Status: metav1.ConditionStatus(condition.Status),
				Reason: condition.Reason, Message: condition.Message,
				ObservedGeneration: deployment.Generation,
			})
		}
	}

	// Failure wins over pending. Bounds and stabilization can limit a healthy
	// autoscaler, so ScalingLimited is reported separately rather than a failure.
	for _, conditionType := range []autoscalingv2.HorizontalPodAutoscalerConditionType{autoscalingv2.AbleToScale, autoscalingv2.ScalingActive} {
		if condition, exists := byType[conditionType]; exists && condition.Status == corev1.ConditionFalse {
			summary.Status = metav1.ConditionFalse
			summary.Reason = condition.Reason
			summary.Message = condition.Message
			return append(conditions, summary)
		}
	}
	able, ableExists := byType[autoscalingv2.AbleToScale]
	active, activeExists := byType[autoscalingv2.ScalingActive]
	if ableExists && activeExists && able.Status == corev1.ConditionTrue && active.Status == corev1.ConditionTrue {
		summary.Status = metav1.ConditionTrue
		summary.Reason = reasonAutoscalerReady
		summary.Message = "The autoscaler can evaluate and apply scaling decisions"
	}
	return append(conditions, summary)
}

// Aggregate every enabled placement: a healthy location must not hide a broken
// autoscaler elsewhere. Keep availability as an independent service signal.
func reconcileWorkloadAutoscalingStatus(workload *computev1alpha.Workload, status *computev1alpha.WorkloadStatus, deployments map[string][]computev1alpha.WorkloadDeployment) {
	workloadConditions := []metav1.Condition{}
	workloadLimits := []metav1.Condition{}
	for i := range status.Placements {
		placement := &status.Placements[i]
		enabled := false
		var desiredSettings computev1alpha.HorizontalScaleSettings
		for _, desired := range workload.Spec.Placements {
			if desired.Name == placement.Name {
				desiredSettings = desired.ScaleSettings
				enabled = desired.ScaleSettings.MaxReplicas != nil && len(desired.ScaleSettings.Metrics) > 0
				break
			}
		}
		if !enabled {
			for _, conditionType := range autoscalingConditionTypes {
				apimeta.RemoveStatusCondition(&placement.Conditions, conditionType)
			}
			continue
		}
		placementConditions := []metav1.Condition{}
		placementLimits := []metav1.Condition{}
		sorted := append([]computev1alpha.WorkloadDeployment(nil), deployments[placement.Name]...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
		for _, deployment := range sorted {
			condition := apimeta.FindStatusCondition(deployment.Status.Conditions, computev1alpha.AutoscalingReady)
			// Both status generations originate in the cell. The federated
			// object's metadata.generation belongs to a different API server.
			if condition == nil || !equality.Semantic.DeepEqual(desiredSettings, deployment.Spec.ScaleSettings) || condition.ObservedGeneration < deployment.Status.ObservedGeneration {
				placementConditions = append(placementConditions, pendingAutoscalingCondition(workload.Generation))
				placementLimits = append(placementLimits, pendingAutoscalingLimit(workload.Generation))
				continue
			}
			current := *condition
			current.Message = fmt.Sprintf("Location %q, deployment %q: %s", deployment.Spec.LocationRef.Name, deployment.Name, current.Message)
			placementConditions = append(placementConditions, current)
			limit := apimeta.FindStatusCondition(deployment.Status.Conditions, computev1alpha.AutoscalingLimited)
			if limit == nil || limit.ObservedGeneration < deployment.Status.ObservedGeneration {
				placementLimits = append(placementLimits, pendingAutoscalingLimit(workload.Generation))
			} else {
				currentLimit := *limit
				currentLimit.Message = fmt.Sprintf("Location %q, deployment %q: %s", deployment.Spec.LocationRef.Name, deployment.Name, currentLimit.Message)
				placementLimits = append(placementLimits, currentLimit)
			}
		}
		condition := aggregateAutoscalingConditions(placementConditions, workload.Generation)
		apimeta.SetStatusCondition(&placement.Conditions, condition)
		condition.Message = fmt.Sprintf("Placement %q: %s", placement.Name, condition.Message)
		workloadConditions = append(workloadConditions, condition)
		limit := aggregateAutoscalingLimits(placementLimits, workload.Generation)
		apimeta.SetStatusCondition(&placement.Conditions, limit)
		limit.Message = fmt.Sprintf("Placement %q: %s", placement.Name, limit.Message)
		workloadLimits = append(workloadLimits, limit)
	}
	if len(workloadConditions) == 0 {
		for _, conditionType := range autoscalingConditionTypes {
			apimeta.RemoveStatusCondition(&status.Conditions, conditionType)
		}
		return
	}
	apimeta.SetStatusCondition(&status.Conditions, aggregateAutoscalingConditions(workloadConditions, workload.Generation))
	apimeta.SetStatusCondition(&status.Conditions, aggregateAutoscalingLimits(workloadLimits, workload.Generation))
}

func aggregateAutoscalingConditions(conditions []metav1.Condition, generation int64) metav1.Condition {
	result := pendingAutoscalingCondition(generation)
	for _, desiredStatus := range []metav1.ConditionStatus{metav1.ConditionFalse, metav1.ConditionUnknown} {
		for _, condition := range conditions {
			if condition.Status == desiredStatus {
				condition.ObservedGeneration = generation
				return condition
			}
		}
	}
	if len(conditions) > 0 {
		result.Status = metav1.ConditionTrue
		result.Reason = "AutoscalersReady"
		result.Message = "All enabled autoscalers can evaluate and apply scaling decisions"
	}
	return result
}

func pendingAutoscalingLimit(generation int64) metav1.Condition {
	return metav1.Condition{
		Type: computev1alpha.AutoscalingLimited, Status: metav1.ConditionUnknown,
		Reason: reasonAwaitingAutoscaler, Message: "Waiting for the autoscaler to report scaling limits",
		ObservedGeneration: generation,
	}
}

func aggregateAutoscalingLimits(conditions []metav1.Condition, generation int64) metav1.Condition {
	for _, desiredStatus := range []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionUnknown} {
		for _, condition := range conditions {
			if condition.Status == desiredStatus {
				condition.ObservedGeneration = generation
				return condition
			}
		}
	}
	result := pendingAutoscalingLimit(generation)
	if len(conditions) > 0 {
		result.Status = metav1.ConditionFalse
		result.Reason = "WithinScalingLimits"
		result.Message = "All enabled autoscalers are operating within their scaling limits"
	}
	return result
}
