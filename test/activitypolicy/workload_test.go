// SPDX-License-Identifier: AGPL-3.0-only

package activitypolicy_test

import "testing"

const workloadPolicy = policyDir + "workload-policy.yaml"

// workloadAudit is a request for the web workload. Updates and patches carry a
// spec change unless the test replaces the request object.
func workloadAudit(verb, username string, code int, subresource string) map[string]any {
	objectRef := map[string]any{
		"resource":  "workloads",
		"namespace": "default",
		"name":      "web",
		"apiGroup":  "compute.datumapis.com",
	}
	if subresource != "" {
		objectRef["subresource"] = subresource
	}
	audit := map[string]any{
		"verb":           verb,
		"requestURI":     "/apis/compute.datumapis.com/v1alpha/namespaces/default/workloads/web",
		"objectRef":      objectRef,
		"user":           map[string]any{"username": username},
		"responseStatus": map[string]any{"code": int64(code)},
	}
	if verb == "update" || verb == "patch" {
		audit["requestObject"] = map[string]any{"spec": map[string]any{}}
	}
	return audit
}

func jsonPatch(ops ...map[string]any) []any {
	patch := make([]any, 0, len(ops))
	for _, op := range ops {
		patch = append(patch, op)
	}
	return patch
}

func TestWorkloadUserActions(t *testing.T) {
	p := loadPolicy(t, workloadPolicy)
	specJSONPatch := workloadAudit("patch", "alice@example.com", 200, "")
	specJSONPatch["requestObject"] = jsonPatch(map[string]any{
		"op": "replace", "path": "/spec/template/spec/runtime/sandbox/containers/0/image", "value": "nginx:1.27",
	})

	for name, tc := range map[string]struct {
		audit map[string]any
		want  string
	}{
		"create":          {workloadAudit("create", "alice@example.com", 201, ""), "alice@example.com created workload web"},
		"update":          {workloadAudit("update", "alice@example.com", 200, ""), "alice@example.com updated workload web"},
		"merge patch":     {workloadAudit("patch", "alice@example.com", 200, ""), "alice@example.com updated workload web"},
		"spec json patch": {specJSONPatch, "alice@example.com updated workload web"},
		"delete":          {workloadAudit("delete", "alice@example.com", 200, ""), "alice@example.com deleted workload web"},
	} {
		t.Run(name, func(t *testing.T) {
			_, summary := summarize(t, p, tc.audit)
			if summary != tc.want {
				t.Fatalf("summary = %q, want %q", summary, tc.want)
			}
		})
	}
}

func TestWorkloadCreateWithGeneratedName(t *testing.T) {
	p := loadPolicy(t, workloadPolicy)
	audit := workloadAudit("create", "alice@example.com", 201, "")
	delete(audit["objectRef"].(map[string]any), "name")
	audit["responseObject"] = map[string]any{"metadata": map[string]any{"name": "web-x7k2p"}}

	_, summary := summarize(t, p, audit)

	if want := "alice@example.com created workload web-x7k2p"; summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
}

func TestWorkloadNoiseIsNotRecorded(t *testing.T) {
	p := loadPolicy(t, workloadPolicy)

	dryRunCreate := workloadAudit("create", "alice@example.com", 201, "")
	dryRunCreate["requestURI"] = "/apis/compute.datumapis.com/v1alpha/namespaces/default/workloads?dryRun=All"
	dryRunDelete := workloadAudit("delete", "alice@example.com", 200, "")
	dryRunDelete["requestURI"] = "/apis/compute.datumapis.com/v1alpha/namespaces/default/workloads/web?dryRun=All"
	labelMergePatch := workloadAudit("patch", "alice@example.com", 200, "")
	labelMergePatch["requestObject"] = map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"team": "web"}},
	}
	labelJSONPatch := workloadAudit("patch", "alice@example.com", 200, "")
	labelJSONPatch["requestObject"] = jsonPatch(
		map[string]any{"op": "add", "path": "/metadata/labels/team", "value": "web"},
	)
	specTestOp := workloadAudit("patch", "alice@example.com", 200, "")
	specTestOp["requestObject"] = jsonPatch(map[string]any{"op": "test", "path": "/spec/placements", "value": []any{}})

	for name, audit := range map[string]map[string]any{
		"compute finalizer update": workloadAudit("update", "system:control@compute.datumapis.com", 200, ""),
		"status write":             workloadAudit("update", "alice@example.com", 200, "status"),
		"refused create":           workloadAudit("create", "alice@example.com", 422, ""),
		"read":                     workloadAudit("get", "alice@example.com", 200, ""),
		"dry-run create":           dryRunCreate,
		"dry-run delete":           dryRunDelete,
		"label merge patch":        labelMergePatch,
		"label json patch":         labelJSONPatch,
		"json patch test of spec":  specTestOp,
	} {
		t.Run(name, func(t *testing.T) {
			if rule, summary := summarize(t, p, audit); rule != "" {
				t.Fatalf("rule %s recorded %q", rule, summary)
			}
		})
	}
}
