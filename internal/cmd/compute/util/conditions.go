package util

import (
	"fmt"

	v1alpha "go.datum.net/compute/api/v1alpha"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// User-facing instance status strings, shared between the list and describe
// views (and their tests) so the wording stays in one place.
const (
	statusAvailable = "Available"
	statusStarting  = "Starting"
	statusPending   = "Pending"

	statusFailedImageUnavailable = "Failed (image unavailable)"
	statusFailedCrashing         = "Failed (crashing)"
	statusFailedConfigError      = "Failed (configuration error)"

	detailImageUnavailable = "Not available — image unavailable"
	detailInstanceCrashing = "Not available — instance crashing"
	detailConfigError      = "Not available — configuration error"
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
// It reports availability, not live runtime state — it never indicates
// whether a process is actively running at this instant.
// Priority order:
//
//	Ready=True → "Available"
//	QuotaGranted=False/QuotaExceeded → "Pending (quota exceeded)"
//	QuotaGranted=False/ValidationFailed → "Pending (quota validation failed)"
//	QuotaGranted=Unknown/PendingEvaluation → "Pending (quota evaluation)"
//	Programmed=False/ImageUnavailable → "Failed (image unavailable)"
//	Programmed=False/InstanceCrashing → "Failed (crashing)"
//	Programmed=False/ConfigurationError → "Failed (configuration error)"
//	Programmed≠True/PendingProgramming or ProgrammingInProgress → "Starting"
//	Ready≠True/<reason> → "Pending (<reason>)" from server-rolled-up blocking reason
//	default → "Pending"
func InstanceStatus(conditions []metav1.Condition) string {
	ready := FindCondition(conditions, v1alpha.InstanceReady)
	if ready != nil && ready.Status == metav1.ConditionTrue {
		return statusAvailable
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
	if programmed != nil && programmed.Status != metav1.ConditionTrue {
		switch programmed.Reason {
		case v1alpha.InstanceProgrammedReasonImageUnavailable:
			return statusFailedImageUnavailable
		case v1alpha.InstanceProgrammedReasonInstanceCrashing:
			return statusFailedCrashing
		case v1alpha.InstanceProgrammedReasonConfigurationError:
			return statusFailedConfigError
		case v1alpha.InstanceProgrammedReasonPendingProgramming, v1alpha.InstanceProgrammedReasonProgrammingInProgress:
			return statusStarting
		}
	}

	// Use the server-rolled-up blocking reason from the Ready condition. This
	// surfaces reasons like SourceNotFound or ReferencedDataNotReady that the
	// sub-condition checks above don't cover, without requiring client-side
	// knowledge of every reason value.
	if reason, _, blocked := ReadinessBlock(conditions, v1alpha.InstanceReady); blocked && reason != "" {
		return "Pending (" + reason + ")"
	}

	return statusPending
}

// InstanceStatusDetail returns a status line and optional detail message for describe views.
//
//	Ready=True → "Available", ""
//	QuotaGranted=False/QuotaExceeded → "Not available — quota exceeded", condition.Message
//	Programmed=False/ImageUnavailable → "Not available — image unavailable", condition.Message
//	Programmed=False/InstanceCrashing → "Not available — instance crashing", condition.Message
//	Programmed=False/ConfigurationError → "Not available — configuration error", condition.Message
//	Programmed≠True/PendingProgramming or ProgrammingInProgress → "Starting", ""
//	Ready≠True/<reason> → "Pending — <reason>", message  (server-rolled-up blocking reason)
//	default → "Pending", ""
func InstanceStatusDetail(conditions []metav1.Condition) (status, detail string) {
	ready := FindCondition(conditions, v1alpha.InstanceReady)
	if ready != nil && ready.Status == metav1.ConditionTrue {
		return statusAvailable, ""
	}

	quota := FindCondition(conditions, v1alpha.InstanceQuotaGranted)
	if quota != nil && quota.Status == metav1.ConditionFalse && quota.Reason == v1alpha.InstanceQuotaGrantedReasonQuotaExceeded {
		return "Not available — quota exceeded", quota.Message
	}

	programmed := FindCondition(conditions, v1alpha.InstanceProgrammed)
	if programmed != nil && programmed.Status != metav1.ConditionTrue {
		switch programmed.Reason {
		case v1alpha.InstanceProgrammedReasonImageUnavailable:
			return detailImageUnavailable, programmed.Message
		case v1alpha.InstanceProgrammedReasonInstanceCrashing:
			return detailInstanceCrashing, programmed.Message
		case v1alpha.InstanceProgrammedReasonConfigurationError:
			return detailConfigError, programmed.Message
		case v1alpha.InstanceProgrammedReasonPendingProgramming, v1alpha.InstanceProgrammedReasonProgrammingInProgress:
			return statusStarting, ""
		}
	}

	// Fall back to the server-rolled-up blocking reason on the Ready condition.
	// This surfaces reasons like SourceNotFound and ReferencedDataNotReady
	// without requiring client-side special-casing of every reason value.
	if reason, msg, blocked := ReadinessBlock(conditions, v1alpha.InstanceReady); blocked && reason != "" {
		return "Pending — " + reason, msg
	}

	return statusPending, ""
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

// IsAvailable returns true if the instance's Ready condition status is True.
func IsAvailable(conditions []metav1.Condition) bool {
	c := FindCondition(conditions, v1alpha.InstanceReady)
	return c != nil && c.Status == metav1.ConditionTrue
}
