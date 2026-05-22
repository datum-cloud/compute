package util

import (
	"fmt"

	v1alpha "go.datum.net/compute/api/v1alpha"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FindCondition returns the first condition with the given type, or nil.
func FindCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// InstanceStatus returns a short user-facing status string for list views.
// Priority order:
//
//	Ready=True → "Running"
//	QuotaGranted=False/QuotaExceeded → "Pending (quota exceeded)"
//	QuotaGranted=False/ValidationFailed → "Pending (quota validation failed)"
//	QuotaGranted=Unknown/PendingEvaluation → "Pending (quota evaluation)"
//	Programmed=False/PendingProgramming or ProgrammingInProgress → "Pending (network provisioning)"
//	Running=False/Starting → "Starting"
//	Running=False/Stopping → "Stopping"
//	Ready=False/SchedulingGatesPresent → "Pending (scheduling gates)"
//	default → "Pending"
func InstanceStatus(conditions []metav1.Condition) string {
	ready := FindCondition(conditions, v1alpha.InstanceReady)
	if ready != nil && ready.Status == metav1.ConditionTrue {
		return "Running"
	}

	quota := FindCondition(conditions, v1alpha.InstanceQuotaGranted)
	if quota != nil && quota.Status == metav1.ConditionFalse {
		switch quota.Reason {
		case v1alpha.InstanceQuotaGrantedReasonQuotaExceeded:
			return "Pending (quota exceeded)"
		case v1alpha.InstanceQuotaGrantedReasonValidationFailed:
			return "Pending (quota validation failed)"
		}
	}
	if quota != nil && quota.Status == metav1.ConditionUnknown {
		if quota.Reason == v1alpha.InstanceQuotaGrantedReasonPendingEvaluation {
			return "Pending (quota evaluation)"
		}
	}

	programmed := FindCondition(conditions, v1alpha.InstanceProgrammed)
	if programmed != nil && programmed.Status == metav1.ConditionFalse {
		switch programmed.Reason {
		case v1alpha.InstanceProgrammedReasonPendingProgramming, v1alpha.InstanceProgrammedReasonProgrammingInProgress:
			return "Pending (network provisioning)"
		}
	}

	running := FindCondition(conditions, v1alpha.InstanceRunning)
	if running != nil && running.Status == metav1.ConditionFalse {
		switch running.Reason {
		case v1alpha.InstanceRunningReasonStarting:
			return "Starting"
		case v1alpha.InstanceRunningReasonStopping:
			return "Stopping"
		}
	}

	if ready != nil && ready.Status == metav1.ConditionFalse {
		if ready.Reason == v1alpha.InstanceReadyReasonSchedulingGatesPresent {
			return "Pending (scheduling gates)"
		}
	}

	return "Pending"
}

// InstanceStatusDetail returns a status line and optional detail message for describe views.
//
//	Ready=True → "Running", ""
//	QuotaGranted=False/QuotaExceeded → "Not running — quota exceeded", condition.Message
//	Programmed=False/PendingProgramming → "Not running — network provisioning", ""
//	Programmed=False/ProgrammingInProgress → "Not running — network provisioning in progress", ""
//	Running=False/Starting → "Starting", ""
//	Running=False/Stopping → "Stopping", ""
//	default → "Unknown", ""
func InstanceStatusDetail(conditions []metav1.Condition) (status, detail string) {
	ready := FindCondition(conditions, v1alpha.InstanceReady)
	if ready != nil && ready.Status == metav1.ConditionTrue {
		return "Running", ""
	}

	quota := FindCondition(conditions, v1alpha.InstanceQuotaGranted)
	if quota != nil && quota.Status == metav1.ConditionFalse && quota.Reason == v1alpha.InstanceQuotaGrantedReasonQuotaExceeded {
		return "Not running — quota exceeded", quota.Message
	}

	programmed := FindCondition(conditions, v1alpha.InstanceProgrammed)
	if programmed != nil && programmed.Status == metav1.ConditionFalse {
		switch programmed.Reason {
		case v1alpha.InstanceProgrammedReasonPendingProgramming:
			return "Not running — network provisioning", ""
		case v1alpha.InstanceProgrammedReasonProgrammingInProgress:
			return "Not running — network provisioning in progress", ""
		}
	}

	running := FindCondition(conditions, v1alpha.InstanceRunning)
	if running != nil && running.Status == metav1.ConditionFalse {
		switch running.Reason {
		case v1alpha.InstanceRunningReasonStarting:
			return "Starting", ""
		case v1alpha.InstanceRunningReasonStopping:
			return "Stopping", ""
		}
	}

	return "Unknown", ""
}

// WorkloadHealth derives a one-line health summary from workload Available condition + replica counts.
//
//	Available=True, ready==desired → "Available — all placements at desired replicas"
//	Available=True, ready<desired  → "Degraded — N instances below desired count"  (N = desired-ready)
//	Available=False               → "Unavailable — no healthy instances"
//	Unknown/missing               → "Unknown"
func WorkloadHealth(conditions []metav1.Condition, ready, desired int32) string {
	avail := FindCondition(conditions, v1alpha.WorkloadAvailable)
	if avail == nil || avail.Status == metav1.ConditionUnknown {
		return "Unknown"
	}
	if avail.Status == metav1.ConditionFalse {
		return "Unavailable — no healthy instances"
	}
	// Available=True
	if ready >= desired {
		return "Available — all placements at desired replicas"
	}
	diff := desired - ready
	return fmt.Sprintf("Degraded — %d instances below desired count", diff)
}

// IsRunning returns true if the instance's Ready condition status is True.
func IsRunning(conditions []metav1.Condition) bool {
	c := FindCondition(conditions, v1alpha.InstanceReady)
	return c != nil && c.Status == metav1.ConditionTrue
}
