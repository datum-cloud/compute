// SPDX-License-Identifier: AGPL-3.0-only

// Package activitypolicy_test evaluates compute's ActivityPolicies the way the
// Activity service does: rules in order, first match wins, and summaries are
// templates of {{ CEL }} expressions over the audit log or event.
package activitypolicy_test

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"sigs.k8s.io/yaml"
)

const policyDir = "../../config/milo/activity/policies/"

type rule struct {
	Name    string `json:"name"`
	Match   string `json:"match"`
	Summary string `json:"summary"`
}

type policy struct {
	Spec struct {
		Resource struct {
			APIGroup string `json:"apiGroup"`
			Kind     string `json:"kind"`
		} `json:"resource"`
		AuditRules []rule `json:"auditRules"`
		EventRules []rule `json:"eventRules"`
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
	if len(p.Spec.AuditRules)+len(p.Spec.EventRules) == 0 {
		t.Fatalf("%s has no rules", path)
	}
	return p
}

// linkFunc renders link() as its display text and, like the Activity
// service, records the resource it points at.
func linkFunc(links *[]map[string]any) cel.EnvOption {
	return cel.Function("link",
		cel.Overload("link_string_dyn", []*cel.Type{cel.StringType, cel.DynType}, cel.StringType,
			cel.BinaryBinding(func(text, resource ref.Val) ref.Val {
				if links != nil {
					*links = append(*links, linkResource(resource.Value()))
				}
				return types.String(fmt.Sprintf("%v", text.Value()))
			}),
		),
	)
}

func linkResource(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case map[ref.Val]ref.Val:
		out := make(map[string]any, len(m))
		for k, v := range m {
			if key, ok := k.Value().(string); ok {
				out[key] = v.Value()
			}
		}
		return out
	}
	return nil
}

func auditEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv(
		cel.Variable("audit", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("actor", cel.StringType),
		cel.Variable("actorRef", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("kind", cel.StringType),
		linkFunc(nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func eventEnv(t *testing.T, links *[]map[string]any) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv(
		cel.Variable("event", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("actor", cel.StringType),
		cel.Variable("actorRef", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("source", cel.MapType(cel.StringType, cel.StringType)),
		linkFunc(links),
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

// firstMatch returns the name and rendered summary of the first rule that
// matches. A match or summary that errors fails the test, because the Activity
// service sends that message to the dead letter queue instead of retrying it.
func firstMatch(t *testing.T, env *cel.Env, rules []rule, vars map[string]any) (string, string) {
	t.Helper()
	for _, r := range rules {
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

// summarize evaluates the policy's audit rules against an audit log entry.
// Missing nested fields default to empty maps, as in the Activity service.
func summarize(t *testing.T, p policy, audit map[string]any) (string, string) {
	t.Helper()
	for _, field := range []string{"objectRef", "user", "responseStatus", "responseObject", "requestObject"} {
		if _, ok := audit[field]; !ok {
			audit[field] = map[string]any{}
		}
	}
	username, _ := audit["user"].(map[string]any)["username"].(string)
	resource, _ := audit["objectRef"].(map[string]any)["resource"].(string)
	vars := map[string]any{
		"audit":    audit,
		"actor":    username,
		"actorRef": map[string]any{},
		"kind":     resource,
	}
	return firstMatch(t, auditEnv(t), p.Spec.AuditRules, vars)
}

// summarizeEvent evaluates the policy's event rules against an event. Like the
// Activity service, source always carries all four keys, empty when the event
// came from a control plane without an exporter that stamps them.
func summarizeEvent(t *testing.T, p policy, event map[string]any) (string, string) {
	t.Helper()
	rule, summary, _ := summarizeEventWithLinks(t, p, event)
	return rule, summary
}

// summarizeEventWithLinks also returns the resources the summary links to.
func summarizeEventWithLinks(t *testing.T, p policy, event map[string]any) (string, string, []map[string]any) {
	t.Helper()
	annotations := map[string]any{}
	if md, ok := event["metadata"].(map[string]any); ok {
		if a, ok := md["annotations"].(map[string]any); ok {
			annotations = a
		}
	}
	str := func(k string) string { s, _ := annotations[k].(string); return s }
	vars := map[string]any{
		"event":    event,
		"actor":    "compute.datumapis.com/instance-controller",
		"actorRef": map[string]any{"type": "controller", "name": "compute.datumapis.com/instance-controller"},
		"source": map[string]string{
			"planeType": str("activity.miloapis.com/source-plane-type"),
			"cluster":   str("activity.miloapis.com/source-cluster"),
			"region":    str("activity.miloapis.com/source-region"),
			"city":      str("activity.miloapis.com/source-city"),
		},
	}
	var links []map[string]any
	rule, summary := firstMatch(t, eventEnv(t, &links), p.Spec.EventRules, vars)
	return rule, summary, links
}
