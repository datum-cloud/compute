// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"encoding/json"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// ReasonNonZeroExitCode is the reason Kubernetes reports on the exec status
// channel for a command that exited with a code other than zero.
const ReasonNonZeroExitCode = "NonZeroExitCode"

const causeTypeExitCode = "ExitCode"

var endMessages = map[string]string{
	computev1alpha.InstanceConsoleSessionReasonExpired:            "The session reached its time limit and the command was stopped.",
	computev1alpha.InstanceConsoleSessionReasonRevoked:            "The session was deleted and the command was stopped.",
	computev1alpha.InstanceConsoleSessionReasonNotConnected:       "No client connected before the connection deadline.",
	computev1alpha.InstanceConsoleSessionReasonAgentShutdown:      "The platform stopped the session for maintenance. Start a new session to continue.",
	computev1alpha.InstanceConsoleSessionReasonAgentLost:          "The platform lost the session unexpectedly. Start a new session to continue.",
	computev1alpha.InstanceConsoleSessionReasonTooManySessions:    "The instance already has as many open sessions as it allows.",
	computev1alpha.InstanceConsoleSessionReasonInstanceNotRunning: "The instance is not running.",
	computev1alpha.InstanceConsoleSessionReasonInstanceNotFound:   "The instance no longer exists or has been replaced.",
}

func endMessage(reason string) string {
	if msg, ok := endMessages[reason]; ok {
		return msg
	}
	return "The session ended."
}

// terminalReason reports whether reason ends a session for good.
func terminalReason(reason string) bool {
	switch reason {
	case computev1alpha.InstanceConsoleSessionReasonCompleted,
		computev1alpha.InstanceConsoleSessionReasonExpired,
		computev1alpha.InstanceConsoleSessionReasonRevoked,
		computev1alpha.InstanceConsoleSessionReasonNotConnected,
		computev1alpha.InstanceConsoleSessionReasonAgentShutdown,
		computev1alpha.InstanceConsoleSessionReasonAgentLost,
		computev1alpha.InstanceConsoleSessionReasonTooManySessions,
		computev1alpha.InstanceConsoleSessionReasonNoShell,
		computev1alpha.InstanceConsoleSessionReasonCommandUnavailable,
		computev1alpha.InstanceConsoleSessionReasonInstanceNotRunning,
		computev1alpha.InstanceConsoleSessionReasonInstanceNotFound,
		computev1alpha.InstanceConsoleSessionReasonInvalid:
		return true
	}
	return false
}

func readyCondition(s *computev1alpha.InstanceConsoleSession) *metav1.Condition {
	return meta.FindStatusCondition(s.Status.Conditions, computev1alpha.InstanceConsoleSessionReady)
}

func readyReason(s *computev1alpha.InstanceConsoleSession) string {
	if c := readyCondition(s); c != nil {
		return c.Reason
	}
	return computev1alpha.InstanceConsoleSessionReasonPending
}

func isTerminal(s *computev1alpha.InstanceConsoleSession) bool {
	c := readyCondition(s)
	return c != nil && c.Status == metav1.ConditionFalse && terminalReason(c.Reason)
}

func setReady(s *computev1alpha.InstanceConsoleSession, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{
		Type:               computev1alpha.InstanceConsoleSessionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: s.Generation,
	})
}

// closingStatus is the one status message the agent sends on the status
// channel before it closes a session's stream. A command that exited reports
// what Kubernetes would; any other ending names its terminal reason.
func closingStatus(reason string, exitCode *int32, message string) metav1.Status {
	status := metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure,
	}
	switch {
	case reason == computev1alpha.InstanceConsoleSessionReasonCompleted && exitCode != nil && *exitCode == 0:
		status.Status = metav1.StatusSuccess
	case reason == computev1alpha.InstanceConsoleSessionReasonCompleted && exitCode != nil:
		status.Reason = ReasonNonZeroExitCode
		status.Message = fmt.Sprintf("command terminated with non-zero exit code: %d", *exitCode)
		status.Details = &metav1.StatusDetails{Causes: []metav1.StatusCause{{
			Type:    causeTypeExitCode,
			Message: strconv.Itoa(int(*exitCode)),
		}}}
	default:
		status.Reason = metav1.StatusReason(reason)
		status.Message = message
	}
	return status
}

// exitCodeFromStatus reads the command's exit code from the status message the
// apiserver sends when the command exits. ok is false when the status reports
// an error other than the command's exit.
func exitCodeFromStatus(raw []byte) (code int32, message string, ok bool) {
	var status metav1.Status
	if err := json.Unmarshal(raw, &status); err != nil {
		return 0, "the platform could not read the command's result", false
	}
	if status.Status == metav1.StatusSuccess {
		return 0, "", true
	}
	if status.Reason == ReasonNonZeroExitCode && status.Details != nil {
		for _, cause := range status.Details.Causes {
			if cause.Type != causeTypeExitCode {
				continue
			}
			n, err := strconv.ParseInt(cause.Message, 10, 32)
			if err == nil {
				return int32(n), "", true
			}
		}
	}
	return 0, status.Message, false
}

// refusal maps a session that cannot take a connection to the protocol's
// refusal code.
func refusal(s *computev1alpha.InstanceConsoleSession) (int, string) {
	switch reason := readyReason(s); {
	case reason == computev1alpha.InstanceConsoleSessionReasonRevoked,
		reason == computev1alpha.InstanceConsoleSessionReasonExpired,
		reason == computev1alpha.InstanceConsoleSessionReasonNotConnected:
		return 410, endMessage(reason)
	case isTerminal(s):
		return 409, "the session has already ended"
	case reason == computev1alpha.InstanceConsoleSessionReasonConnected:
		return 409, "the session already has a connection"
	default:
		return 409, "the session is not ready for a connection"
	}
}
