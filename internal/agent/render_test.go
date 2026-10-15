// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sigsyaml "sigs.k8s.io/yaml"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/locations"
	"go.datum.net/compute/internal/workloadspec"
)

const testImage = "ghcr.io/acme/api:1.4.2"

// renderInput is the everyday case: one container, one location, one port.
func renderInput() WorkloadRenderInput {
	return WorkloadRenderInput{
		Name:       wlAPIBackend,
		Image:      testImage,
		Placements: []RenderPlacement{{Locations: []string{locationDFW}, MinReplicas: 2}},
		Ports:      []RenderPort{{Name: "http", Port: 8080}},
	}
}

// TestWorkloadRenderProducesAManifestAndSaysWhatIsSettled: the manifest is only
// half the answer. The notes carry the decisions that cannot be corrected by a
// later render, and a model that does not read them out lets a person agree to
// something they would have to recreate the workload to change.
func TestWorkloadRenderProducesAManifestAndSaysWhatIsSettled(t *testing.T) {
	deps := fixtureDeps(fixtureReader())

	_, out, err := workloadRender(deps)(context.Background(), nil, renderInput())
	if err != nil {
		t.Fatalf("compute_workload_render: %v", err)
	}

	for _, want := range []string{
		"kind: Workload",
		"name: " + wlAPIBackend,
		testImage,
		"minReplicas: 2",
		"- name: " + locationDFW,
	} {
		if !strings.Contains(out.Manifest, want) {
			t.Errorf("manifest is missing %q:\n%s", want, out.Manifest)
		}
	}
	// The manifest is handed on to the assistant's plan tool as-is, so it has
	// to read straight back into a Workload.
	var decoded computev1alpha.Workload
	if err := sigsyaml.UnmarshalStrict([]byte(out.Manifest), &decoded); err != nil {
		t.Errorf("the rendered manifest does not read back: %v", err)
	}

	notes := strings.Join(out.Notes, "\n")
	// The interface is settled at create, and the instance type and network
	// were defaulted rather than chosen — both are things to say out loud
	// while the workload can still be changed. A missing network is planned
	// alongside the workload, so the note has to say how.
	for _, want := range []string{
		"cannot be changed once the workload exists",
		"IPv6 only",
		workloadspec.DefaultInstanceType,
		"\"" + workloadspec.DefaultNetwork + "\"",
		baseToolResourcesPlan,
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes do not mention %q:\n%s", want, notes)
		}
	}
}

// TestWorkloadRenderSelectsLocationsByTopology covers the second way a
// placement says where: a selector over location topology rather than a list of
// names. It is the only way to say "every location in this city", and it keeps
// matching locations added later — which is a standing behaviour the person
// agreeing to the manifest has to be told about, so the notes carry it.
func TestWorkloadRenderSelectsLocationsByTopology(t *testing.T) {
	deps := fixtureDeps(fixtureReader())
	in := renderInput()
	in.Placements = []RenderPlacement{{
		LocationSelector: &RenderLocationSelector{
			MatchLabels: map[string]string{locations.TopologyCityCodeKey: cityDFW},
		},
		MinReplicas: 2,
	}}

	_, out, err := workloadRender(deps)(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("compute_workload_render: %v", err)
	}
	for _, want := range []string{"locationSelector:", locations.TopologyCityCodeKey + ": " + cityDFW} {
		if !strings.Contains(out.Manifest, want) {
			t.Errorf("manifest is missing %q:\n%s", want, out.Manifest)
		}
	}
	if strings.Contains(out.Manifest, "locations:") {
		t.Errorf("a selector was given, so no location list may be emitted:\n%s", out.Manifest)
	}
	if !strings.Contains(strings.Join(out.Notes, "\n"), "locations added later") {
		t.Errorf("notes do not say the selector keeps matching new locations:\n%s", out.Notes)
	}
}

// TestWorkloadRenderPassesTheRuntimeClassThrough: the tier is the server's
// catalog to own. Whatever the person named goes through verbatim, and naming
// nothing leaves the field off so the server picks its own default rather than
// this tool settling a choice that cannot be changed afterwards.
func TestWorkloadRenderPassesTheRuntimeClassThrough(t *testing.T) {
	deps := fixtureDeps(fixtureReader())

	_, bare, err := workloadRender(deps)(context.Background(), nil, renderInput())
	if err != nil {
		t.Fatalf("compute_workload_render: %v", err)
	}
	if strings.Contains(bare.Manifest, "class:") {
		t.Errorf("no runtime class was asked for, so none may be rendered:\n%s", bare.Manifest)
	}

	in := renderInput()
	in.RuntimeClass = "datum-sandbox"
	_, out, err := workloadRender(deps)(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("compute_workload_render: %v", err)
	}
	if !strings.Contains(out.Manifest, "class: datum-sandbox") {
		t.Errorf("manifest does not carry the runtime class that was asked for:\n%s", out.Manifest)
	}
}

// TestWorkloadRenderSaysTheRuntimeClassIsFinal: leaving the class out does not
// leave it open. The server stamps its default at create and the class never
// changes, so the notes must surface the choice either way, and an unset class
// must also warn that an update which drops the class is refused.
func TestWorkloadRenderSaysTheRuntimeClassIsFinal(t *testing.T) {
	deps := fixtureDeps(fixtureReader())

	_, bare, err := workloadRender(deps)(context.Background(), nil, renderInput())
	if err != nil {
		t.Fatalf("compute_workload_render: %v", err)
	}
	notes := strings.Join(bare.Notes, "\n")
	for _, want := range []string{
		"No runtime class was given",
		"marked default",
		"cannot be changed afterwards",
		"confirm it before this is applied",
		"existing workload",
		baseToolResourcesList,
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes for an unset runtime class do not mention %q:\n%s", want, notes)
		}
	}

	in := renderInput()
	in.RuntimeClass = "datum-sandbox"
	_, named, err := workloadRender(deps)(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("compute_workload_render: %v", err)
	}
	notes = strings.Join(named.Notes, "\n")
	if !strings.Contains(notes, `runtime class "datum-sandbox"`) || !strings.Contains(notes, "final") {
		t.Errorf("notes do not report the named runtime class as final:\n%s", notes)
	}
	if strings.Contains(notes, "No runtime class was given") {
		t.Errorf("notes claim no runtime class was given after one was named:\n%s", notes)
	}
}

// TestWorkloadRenderReportsAPublicAddressAsFinal: asking for IPv4 fixes the
// address families for the life of the workload, so the note has to change
// with the input rather than always saying the same thing.
func TestWorkloadRenderReportsAPublicAddressAsFinal(t *testing.T) {
	deps := fixtureDeps(fixtureReader())
	in := renderInput()
	in.PublicIPv4 = true

	_, out, err := workloadRender(deps)(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("compute_workload_render: %v", err)
	}
	notes := strings.Join(out.Notes, "\n")
	if !strings.Contains(notes, "public IPv4 address was asked for") {
		t.Errorf("notes do not report the public address as settled:\n%s", notes)
	}
	if strings.Contains(notes, "IPv6 only") {
		t.Errorf("notes still claim IPv6 only after IPv4 was asked for:\n%s", notes)
	}
}

// TestWorkloadRenderRefusesAnIncompleteInput: a missing image is the caller's
// to supply, and rendering something plausible around a name nobody pushed is
// the failure this prevents.
func TestWorkloadRenderRefusesAnIncompleteInput(t *testing.T) {
	deps := fixtureDeps(fixtureReader())
	in := renderInput()
	in.Image = ""

	if _, _, err := workloadRender(deps)(context.Background(), nil, in); err == nil {
		t.Error("compute_workload_render accepted an input with no image")
	}
}

// TestWorkloadRenderFailsWhenDepsAreUnavailable: rendering reads nothing, but
// it must not be a probe an unauthenticated caller can use either.
func TestWorkloadRenderFailsWhenDepsAreUnavailable(t *testing.T) {
	wantErr := errors.New("no bearer token on the request")
	denied := DepsFor(func(context.Context) (ToolDeps, error) { return ToolDeps{}, wantErr })

	if _, _, err := workloadRender(denied)(context.Background(), nil, renderInput()); !errors.Is(err, wantErr) {
		t.Errorf("compute_workload_render error = %v, want the deps error to surface unchanged", err)
	}
}

// TestWorkloadRenderAnswersOverTheWire proves registration and the schemas,
// not just the handler: a tool that is never wired into RegisterTools passes
// every test above and is uncallable in production, and an output the SDK
// cannot encode reaches the model as nothing at all.
func TestWorkloadRenderAnswersOverTheWire(t *testing.T) {
	ctx := context.Background()

	server := mcp.NewServer(&mcp.Implementation{Name: testServerName, Version: testImplVersion}, nil)
	RegisterTools(server, fixtureDeps(fixtureReader()))

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: testClientName, Version: testImplVersion}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: ToolWorkloadRender,
		Arguments: map[string]any{
			"name":  wlAPIBackend,
			"image": testImage,
			"placements": []map[string]any{
				{"locations": []string{locationDFW}, "minReplicas": 2},
			},
		},
	})
	if err != nil {
		t.Fatalf("calling %s: %v", ToolWorkloadRender, err)
	}
	if res.IsError {
		t.Fatalf("%s returned an error result: %+v", ToolWorkloadRender, res.Content)
	}

	// Round-tripped through the wire's JSON, so the output schema is exercised
	// as the model would receive it.
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshalling structured content: %v", err)
	}
	var out WorkloadRenderOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding %s output: %v", ToolWorkloadRender, err)
	}
	if !strings.Contains(out.Manifest, "name: "+wlAPIBackend) {
		t.Errorf("render over the wire returned no usable manifest: %s", raw)
	}
}
