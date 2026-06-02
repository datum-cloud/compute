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

// ReadinessBlock reads the named readiness condition and reports whether it is
// blocking (present and not True). When blocked, it returns the machine-readable
// reason and human-readable message from the condition. Callers must not branch
// on specific reason values — display whatever the server emits.
func ReadinessBlock(conditions []metav1.Condition, condType string) (reason, message string, blocked bool) {
	c := FindCondition(conditions, condType)
	if c == nil || c.Status == metav1.ConditionTrue {
		return "", "", false
	}
	return c.Reason, c.Message, true
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
//	Ready=False/<reason> → "Pending (<reason>)" from server-rolled-up blocking reason
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

	// Use the server-rolled-up blocking reason from the Ready condition. This
	// surfaces reasons like SourceNotFound or ReferencedDataNotReady that the
	// sub-condition checks above don't cover, without requiring client-side
	// knowledge of every reason value.
	if reason, _, blocked := ReadinessBlock(conditions, v1alpha.InstanceReady); blocked && reason != "" {
		return "Pending (" + reason + ")"
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
//	Ready=False/<reason> → "Pending — <reason>", message  (server-rolled-up blocking reason)
//	default → "Pending", ""
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

	// Fall back to the server-rolled-up blocking reason on the Ready condition.
	// This surfaces reasons like SourceNotFound and ReferencedDataNotReady
	// without requiring client-side special-casing of every reason value.
	if reason, msg, blocked := ReadinessBlock(conditions, v1alpha.InstanceReady); blocked && reason != "" {
		return "Pending — " + reason, msg
	}

	return "Pending", ""
}

// WorkloadHealth derives a one-line health summary from workload Available condition + replica counts.
//
//	Available=True, ready==desired → "Available — all placements at desired replicas"
//	Available=True, ready<desired  → "Degraded — N instances below desired count"  (N = desired-ready)
//	Available=False/<reason>       → "Unavailable — <reason>" (reason from server-rolled-up blocking reason)
//	Available=False (no reason)    → "Unavailable — no healthy instances"
//	Unknown/missing               → "Unknown"
func WorkloadHealth(conditions []metav1.Condition, ready, desired int32) string {
	avail := FindCondition(conditions, v1alpha.WorkloadAvailable)
	if avail == nil || avail.Status == metav1.ConditionUnknown {
		return "Unknown"
	}
	if avail.Status == metav1.ConditionFalse {
		if avail.Reason != "" {
			return "Unavailable — " + avail.Reason
		}
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
