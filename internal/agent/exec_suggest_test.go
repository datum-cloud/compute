package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	testExecInstance  = "api-0"
	testExecContainer = "app"
)

func suggestFixture() *fakeReader {
	inst := instance(testExecInstance)
	inst.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{Containers: []computev1alpha.SandboxContainer{
		{Name: testExecContainer}, {Name: "sidecar"},
	}}
	return &fakeReader{instances: map[string][]computev1alpha.Instance{"api": {inst}}}
}

func TestExecSuggestFormatsExactCommandWithoutRunningIt(t *testing.T) {
	r := suggestFixture()
	_, out, err := execSuggest(fixtureDeps(r))(context.Background(), nil, ExecSuggestInput{
		Instance: testExecInstance, Container: testExecContainer,
		Command: []string{"sh", "-c", "printf '%s' \"$HOME\""}, TTL: "5m",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `datumctl compute exec api-0 -c app --ttl 5m -- sh -c 'printf '"'"'%s'"'"' "$HOME"'`
	if out.Command != want {
		t.Errorf("command = %q, want %q", out.Command, want)
	}
	if !strings.Contains(out.Note, "has not run") {
		t.Errorf("note does not distinguish a suggestion from execution: %q", out.Note)
	}
}

func TestExecSuggestRefusesAmbiguousOrMissingTarget(t *testing.T) {
	r := suggestFixture()
	for _, tc := range []struct {
		name string
		in   ExecSuggestInput
		want string
	}{
		{"multiple containers", ExecSuggestInput{Instance: testExecInstance, Command: []string{"ps"}}, "several containers"},
		{"wrong container", ExecSuggestInput{Instance: testExecInstance, Container: "other", Command: []string{"ps"}}, "no container"},
		{"wrong instance", ExecSuggestInput{Instance: "other", Container: testExecContainer, Command: []string{"ps"}}, "instance not found"},
		{"empty command", ExecSuggestInput{Instance: testExecInstance, Container: testExecContainer}, "command must contain"},
		{"line break", ExecSuggestInput{Instance: testExecInstance, Container: testExecContainer, Command: []string{"sh", "bad\nline"}}, "line break"},
		{"too short TTL", ExecSuggestInput{Instance: testExecInstance, Container: testExecContainer, Command: []string{"ps"}, TTL: "1s"}, "ttl must"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := execSuggest(fixtureDeps(r))(context.Background(), nil, tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestExecSuggestUsesCallerScopedReader(t *testing.T) {
	denied := func(context.Context) (ToolDeps, error) { return ToolDeps{}, errors.New("caller not authorized") }
	_, _, err := execSuggest(denied)(context.Background(), nil, ExecSuggestInput{
		Instance: testExecInstance, Command: []string{"ps"},
	})
	if err == nil || !strings.Contains(err.Error(), "caller not authorized") {
		t.Fatalf("error = %v, want caller denial", err)
	}
}

func TestExecSuggestInfersSingleContainer(t *testing.T) {
	r := suggestFixture()
	inst := r.instances["api"][0]
	inst.Spec.Runtime.Sandbox.Containers = inst.Spec.Runtime.Sandbox.Containers[:1]
	r.instances["api"] = []computev1alpha.Instance{inst}
	_, out, err := execSuggest(fixtureDeps(r))(context.Background(), nil, ExecSuggestInput{
		Instance: testExecInstance, Command: []string{"ps", "aux"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Container != testExecContainer || out.Command != "datumctl compute exec api-0 -c app -- ps aux" {
		t.Fatalf("output = %+v", out)
	}
}
