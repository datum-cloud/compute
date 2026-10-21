// SPDX-License-Identifier: AGPL-3.0-only

package activitypolicy_test

import (
	"strings"
	"testing"
)

const sessionPolicy = policyDir + "instanceconsolesession-policy.yaml"

func sessionCreate(code int, response map[string]any) map[string]any {
	audit := map[string]any{
		"verb": "create",
		"objectRef": map[string]any{
			"resource":  "instanceconsolesessions",
			"namespace": "default",
			"apiGroup":  "compute.datumapis.com",
		},
		"user":           map[string]any{"username": "alice@example.com"},
		"responseStatus": map[string]any{"code": int64(code)},
		"requestObject": map[string]any{
			"metadata": map[string]any{"generateName": "web-0-"},
			"spec": map[string]any{
				"containerName": "app",
				"instanceRef":   map[string]any{"name": "web-0", "uid": "instance-uid"},
			},
		},
	}
	if response != nil {
		audit["responseObject"] = response
	}
	return audit
}

func TestSessionCreateOmitsTheGeneratedName(t *testing.T) {
	p := loadPolicy(t, sessionPolicy)
	created := map[string]any{"metadata": map[string]any{"name": "web-0-x7k2p", "generateName": "web-0-"}}
	audit := sessionCreate(201, created)

	_, summary := summarize(t, p, audit)

	want := "alice@example.com opened a shell session in container app of instance web-0"
	if summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
}

func TestSessionCreateWithoutAResponseStillRecords(t *testing.T) {
	p := loadPolicy(t, sessionPolicy)

	name, summary := summarize(t, p, sessionCreate(201, nil))

	if name == "" || !strings.Contains(summary, "opened a shell session in container app of instance web-0") {
		t.Fatalf("rule %q summary %q; an audit event logged without its response must still be recorded", name, summary)
	}
}

func TestRefusedSessionCreateIsNotRecorded(t *testing.T) {
	p := loadPolicy(t, sessionPolicy)
	refusal := map[string]any{"kind": "Status", "status": "Failure", "code": int64(422)}

	if name, summary := summarize(t, p, sessionCreate(422, refusal)); name != "" {
		t.Fatalf("rule %s recorded a refused create: %q", name, summary)
	}
}

func TestSessionActivityNamesVerifiedAssistantOrigin(t *testing.T) {
	p := loadPolicy(t, sessionPolicy)
	for _, tc := range []struct {
		reason, origin, exitCode, want string
	}{
		{"SessionStarted", "patch", "", "through the Datum assistant"},
		{"SessionEnded", "patch", "0", "(via Datum assistant)"},
		{"SessionEnded", "patch", "", "(via Datum assistant)"},
		{"SessionStarted", "", "", ""},
		{"SessionEnded", "other", "0", ""},
	} {
		t.Run(tc.reason+"/"+tc.origin+"/"+tc.exitCode, func(t *testing.T) {
			annotations := map[string]any{
				"compute.datumapis.com/requester": "alice@example.com",
				"compute.datumapis.com/container": "app",
				"compute.datumapis.com/instance":  "web-0",
				"compute.datumapis.com/cell":      "us-central-1-staging-lab",
				"compute.datumapis.com/reason":    "Completed",
			}
			if tc.origin != "" {
				annotations["compute.datumapis.com/assistant-origin"] = tc.origin
			}
			if tc.exitCode != "" {
				annotations["compute.datumapis.com/exit-code"] = tc.exitCode
			}
			event := map[string]any{
				"reason":   tc.reason,
				"metadata": map[string]any{"annotations": annotations},
			}
			_, summary := summarizeEvent(t, p, event)
			if summary == "" {
				t.Fatal("session lifecycle event was not recorded")
			}
			if got := strings.Contains(summary, "Datum assistant"); got != (tc.want != "") {
				t.Fatalf("assistant origin shown = %v, want %v: %q", got, tc.want != "", summary)
			}
			if tc.want != "" && !strings.Contains(summary, tc.want) {
				t.Fatalf("summary = %q, want %q", summary, tc.want)
			}
		})
	}
}
