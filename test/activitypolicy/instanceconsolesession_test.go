// SPDX-License-Identifier: AGPL-3.0-only

// Package activitypolicy_test evaluates compute's ActivityPolicies the way the
// Activity service does: rules in order, first match wins, and summaries are
// templates of {{ CEL }} expressions over the audit event.
package activitypolicy_test

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"sigs.k8s.io/yaml"
)

const sessionPolicy = "../../config/milo/activity/policies/instanceconsolesession-policy.yaml"

type rule struct {
	Name    string `json:"name"`
	Match   string `json:"match"`
	Summary string `json:"summary"`
}

type policy struct {
	Spec struct {
		AuditRules []rule `json:"auditRules"`
	} `json:"spec"`
}

func loadPolicy(t *testing.T, path string) policy {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p policy
	if err := yaml.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Spec.AuditRules) == 0 {
		t.Fatalf("%s has no audit rules", path)
	}
	return p
}

func auditEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv(
		cel.Variable("audit", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("actor", cel.StringType),
		cel.Variable("actorRef", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("kind", cel.StringType),
		cel.Function("link",
			cel.Overload("link_string_dyn", []*cel.Type{cel.StringType, cel.DynType}, cel.StringType,
				cel.BinaryBinding(func(text, _ ref.Val) ref.Val {
					return types.String(fmt.Sprintf("%v", text.Value()))
				}),
			),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func eval(t *testing.T, env *cel.Env, expr string, vars map[string]any) (any, error) {
	t.Helper()
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("compile %q: %v", expr, iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		t.Fatalf("program %q: %v", expr, err)
	}
	out, _, err := prg.Eval(vars)
	if err != nil {
		return nil, err
	}
	return out.Value(), nil
}

var summaryExpr = regexp.MustCompile(`\{\{\s*(.+?)\s*\}\}`)

// summarize returns the name and summary of the first rule that matches, as
// the Activity service would record it.
func summarize(t *testing.T, p policy, audit map[string]any) (string, string) {
	t.Helper()
	for _, field := range []string{"objectRef", "user", "responseStatus", "responseObject", "requestObject"} {
		if _, ok := audit[field]; !ok {
			audit[field] = map[string]any{}
		}
	}
	vars := map[string]any{
		"audit":    audit,
		"actor":    "alice@example.com",
		"actorRef": map[string]any{},
		"kind":     "instanceconsolesessions",
	}
	env := auditEnv(t)
	for _, r := range p.Spec.AuditRules {
		matched, err := eval(t, env, r.Match, vars)
		if err != nil {
			t.Fatalf("rule %s: match: %v", r.Name, err)
		}
		if matched != true {
			continue
		}
		var failed error
		summary := summaryExpr.ReplaceAllStringFunc(r.Summary, func(m string) string {
			v, err := eval(t, env, summaryExpr.FindStringSubmatch(m)[1], vars)
			if err != nil {
				failed = err
			}
			return fmt.Sprintf("%v", v)
		})
		if failed != nil {
			t.Fatalf("rule %s: summary: %v", r.Name, failed)
		}
		return r.Name, summary
	}
	return "", ""
}

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

func TestSessionCreateNamesTheGeneratedSession(t *testing.T) {
	p := loadPolicy(t, sessionPolicy)
	created := map[string]any{"metadata": map[string]any{"name": "web-0-x7k2p", "generateName": "web-0-"}}
	audit := sessionCreate(201, created)

	_, summary := summarize(t, p, audit)

	want := "alice@example.com opened shell session web-0-x7k2p in container app of instance web-0"
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
