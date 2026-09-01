package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCatalogCoversEveryAPIReason parses api/v1alpha and asserts that every
// condition reason the API declares has a catalog entry.
//
// This is the test that keeps the catalog honest. The assistant's answers are
// only as good as this table: an uncatalogued reason still surfaces, but with
// no explanation and no actionability, so the assistant cannot tell the
// customer whether to act or escalate. Parsing the source (rather than keeping
// a hand-written list here) means adding a reason to the API and forgetting to
// classify it fails this test instead of quietly degrading an answer.
func TestCatalogCoversEveryAPIReason(t *testing.T) {
	apiReasons := parseAPIReasons(t)
	if len(apiReasons) == 0 {
		t.Fatal("parsed no reason constants from api/v1alpha — the parser is broken, not the catalog")
	}

	for name, value := range apiReasons {
		if _, ok := ExplainReason(value); !ok {
			t.Errorf("api/v1alpha.%s = %q has no catalog entry: add one to catalog.go "+
				"classifying it as user / platform / transient", name, value)
		}
	}
}

// TestCatalogHasNoUnknownReasons is the converse: every catalogued reason must
// correspond to a value the API actually declares, so entries for renamed or
// removed reasons do not linger.
func TestCatalogHasNoUnknownReasons(t *testing.T) {
	apiValues := make(map[string]struct{})
	for _, v := range parseAPIReasons(t) {
		apiValues[v] = struct{}{}
	}

	for _, info := range AllReasons() {
		if _, ok := apiValues[info.Reason]; !ok {
			t.Errorf("catalog has entry for %q, which api/v1alpha no longer declares", info.Reason)
		}
	}
}

// TestEveryCatalogEntryIsUsable guards the fields the assistant actually reads.
func TestEveryCatalogEntryIsUsable(t *testing.T) {
	for _, info := range AllReasons() {
		if info.Explanation == "" {
			t.Errorf("%s: empty Explanation", info.Reason)
		}
		if len(info.ConditionTypes) == 0 {
			t.Errorf("%s: no ConditionTypes", info.Reason)
		}
		switch info.Actionability {
		case ActionabilityUser, ActionabilityPlatform, ActionabilityTransient:
		default:
			t.Errorf("%s: invalid Actionability %q", info.Reason, info.Actionability)
		}
		// A cause someone has to act on must say what to do.
		if info.Actionability != ActionabilityTransient && info.Remediation == "" {
			t.Errorf("%s: actionable (%s) but has no Remediation", info.Reason, info.Actionability)
		}
	}
}

// TestPointerReasonsAreCatalogued ensures the walk's pointer set and the
// catalog cannot drift apart.
func TestPointerReasonsAreCatalogued(t *testing.T) {
	for reason := range pointerReasons {
		if _, ok := ExplainReason(reason); !ok {
			t.Errorf("pointer reason %q has no catalog entry", reason)
		}
	}
}

func TestExplainReason(t *testing.T) {
	info, ok := ExplainReason("QuotaExceeded")
	if !ok {
		t.Fatal("QuotaExceeded should be catalogued")
	}
	if info.Actionability != ActionabilityUser {
		t.Errorf("QuotaExceeded actionability = %q, want %q", info.Actionability, ActionabilityUser)
	}

	// QuotaNoBudget reads like QuotaExceeded to a customer but is Datum's to
	// fix — the distinction the catalog exists to make.
	info, ok = ExplainReason("QuotaNoBudget")
	if !ok {
		t.Fatal("QuotaNoBudget should be catalogued")
	}
	if info.Actionability != ActionabilityPlatform {
		t.Errorf("QuotaNoBudget actionability = %q, want %q", info.Actionability, ActionabilityPlatform)
	}

	if _, ok := ExplainReason("NotARealReason"); ok {
		t.Error("unknown reason should not resolve")
	}
}

// parseAPIReasons returns the string-valued constants in api/v1alpha whose
// names mark them as condition reasons, as name -> value.
func parseAPIReasons(t *testing.T) map[string]string {
	t.Helper()

	dir := filepath.Join("..", "..", "api", "v1alpha")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("globbing %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	reasons := make(map[string]string)
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					// Condition *types* (InstanceReady, WorkloadAvailable) are
					// not reasons; only names carrying "Reason" are.
					if !strings.Contains(name.Name, "Reason") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					reasons[name.Name] = value
				}
			}
		}
	}
	return reasons
}
