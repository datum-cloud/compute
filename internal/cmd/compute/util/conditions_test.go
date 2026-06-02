package util

import (
	"testing"

	v1alpha "go.datum.net/compute/api/v1alpha"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func makeCondition(condType, status, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:    condType,
		Status:  metav1.ConditionStatus(status),
		Reason:  reason,
		Message: message,
	}
}

// TestReadinessBlock covers the generic helper that is the heart of the
// status-blocking-reason contract.
func TestReadinessBlock(t *testing.T) {
	tests := []struct {
		name        string
		conditions  []metav1.Condition
		condType    string
		wantReason  string
		wantMessage string
		wantBlocked bool
	}{
		{
			name:        "condition absent — not blocked",
			conditions:  nil,
			condType:    v1alpha.InstanceReady,
			wantBlocked: false,
		},
		{
			name: "condition True — not blocked",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.InstanceReady, "True", "Running", ""),
			},
			condType:    v1alpha.InstanceReady,
			wantBlocked: false,
		},
		{
			name: "condition False with reason and message — blocked",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.InstanceReady, "False", "SourceNotFound", `ConfigMap "app-config" not found in namespace "default"`),
			},
			condType:    v1alpha.InstanceReady,
			wantReason:  "SourceNotFound",
			wantMessage: `ConfigMap "app-config" not found in namespace "default"`,
			wantBlocked: true,
		},
		{
			name: "condition False with reason only — blocked",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.InstanceReady, "False", "ReferencedDataNotReady", ""),
			},
			condType:    v1alpha.InstanceReady,
			wantReason:  "ReferencedDataNotReady",
			wantMessage: "",
			wantBlocked: true,
		},
		{
			name: "condition Unknown — blocked",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.InstanceReady, "Unknown", "PendingQuota", ""),
			},
			condType:    v1alpha.InstanceReady,
			wantReason:  "PendingQuota",
			wantBlocked: true,
		},
		{
			name: "wrong condition type present — not blocked",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.InstanceProgrammed, "False", "PendingProgramming", ""),
			},
			condType:    v1alpha.InstanceReady,
			wantBlocked: false,
		},
		{
			name: "WorkloadDeploymentAvailable False — blocked",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.WorkloadDeploymentAvailable, "False", "NetworkProvisioning", "Waiting for network assignment"),
			},
			condType:    v1alpha.WorkloadDeploymentAvailable,
			wantReason:  "NetworkProvisioning",
			wantMessage: "Waiting for network assignment",
			wantBlocked: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, msg, blocked := ReadinessBlock(tc.conditions, tc.condType)
			if blocked != tc.wantBlocked {
				t.Errorf("ReadinessBlock() blocked = %v, want %v", blocked, tc.wantBlocked)
			}
			if reason != tc.wantReason {
				t.Errorf("ReadinessBlock() reason = %q, want %q", reason, tc.wantReason)
			}
			if msg != tc.wantMessage {
				t.Errorf("ReadinessBlock() message = %q, want %q", msg, tc.wantMessage)
			}
		})
	}
}

// TestInstanceStatusDetail_BlockingReason verifies that the describe view
// surfaces reason+message from the Ready condition when no specific sub-condition
// check matches — the server-rolled-up blocking reason path.
func TestInstanceStatusDetail_BlockingReason(t *testing.T) {
	tests := []struct {
		name       string
		conditions []metav1.Condition
		wantStatus string
		wantDetail string
	}{
		{
			name:       "no conditions — Pending, no detail",
			conditions: nil,
			wantStatus: "Pending",
			wantDetail: "",
		},
		{
			name: "Ready True — Running",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.InstanceReady, "True", "Running", ""),
			},
			wantStatus: "Running",
			wantDetail: "",
		},
		{
			name: "Ready False / SourceNotFound — Pending with reason and message",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.InstanceReady, "False", "SourceNotFound", `ConfigMap "app-config" not found in namespace "default"`),
			},
			wantStatus: "Pending — SourceNotFound",
			wantDetail: `ConfigMap "app-config" not found in namespace "default"`,
		},
		{
			name: "Ready False / ReferencedDataNotReady — Pending with reason, no message",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.InstanceReady, "False", "ReferencedDataNotReady", ""),
			},
			wantStatus: "Pending — ReferencedDataNotReady",
			wantDetail: "",
		},
		{
			name: "quota exceeded still uses specific path",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.InstanceQuotaGranted, "False", v1alpha.InstanceQuotaGrantedReasonQuotaExceeded, "quota limit reached"),
			},
			wantStatus: "Not running — quota exceeded",
			wantDetail: "quota limit reached",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, detail := InstanceStatusDetail(tc.conditions)
			if status != tc.wantStatus {
				t.Errorf("InstanceStatusDetail() status = %q, want %q", status, tc.wantStatus)
			}
			if detail != tc.wantDetail {
				t.Errorf("InstanceStatusDetail() detail = %q, want %q", detail, tc.wantDetail)
			}
		})
	}
}

// TestInstanceStatus_BlockingReason verifies that the list-view short status
// surfaces the server-rolled-up reason from Ready when no sub-condition matches.
func TestInstanceStatus_BlockingReason(t *testing.T) {
	tests := []struct {
		name       string
		conditions []metav1.Condition
		wantStatus string
	}{
		{
			name:       "no conditions — Pending",
			conditions: nil,
			wantStatus: "Pending",
		},
		{
			name: "Ready False / SourceNotFound — Pending (SourceNotFound)",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.InstanceReady, "False", "SourceNotFound", `ConfigMap "app-config" not found`),
			},
			wantStatus: "Pending (SourceNotFound)",
		},
		{
			name: "Ready False / ReferencedDataNotReady — Pending (ReferencedDataNotReady)",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.InstanceReady, "False", "ReferencedDataNotReady", ""),
			},
			wantStatus: "Pending (ReferencedDataNotReady)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := InstanceStatus(tc.conditions)
			if got != tc.wantStatus {
				t.Errorf("InstanceStatus() = %q, want %q", got, tc.wantStatus)
			}
		})
	}
}

// TestWorkloadHealth_BlockingReason verifies that Unavailable workloads surface
// the reason from the Available condition rather than a generic message.
func TestWorkloadHealth_BlockingReason(t *testing.T) {
	tests := []struct {
		name       string
		conditions []metav1.Condition
		ready      int32
		desired    int32
		wantHealth string
	}{
		{
			name: "Available False with reason — Unavailable — <reason>",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.WorkloadAvailable, "False", "SourceNotFound", `ConfigMap "app-config" not found`),
			},
			ready:      0,
			desired:    1,
			wantHealth: "Unavailable — SourceNotFound",
		},
		{
			name: "Available False without reason — generic message",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.WorkloadAvailable, "False", "", ""),
			},
			ready:      0,
			desired:    1,
			wantHealth: "Unavailable — no healthy instances",
		},
		{
			name: "Available True all ready",
			conditions: []metav1.Condition{
				makeCondition(v1alpha.WorkloadAvailable, "True", "Available", ""),
			},
			ready:      2,
			desired:    2,
			wantHealth: "Available — all placements at desired replicas",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WorkloadHealth(tc.conditions, tc.ready, tc.desired)
			if got != tc.wantHealth {
				t.Errorf("WorkloadHealth() = %q, want %q", got, tc.wantHealth)
			}
		})
	}
}
