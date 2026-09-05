package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Fixture names, shared across the tool tests.
const (
	wlWebFrontend      = "web-frontend"
	wlAPIBackend       = "api-backend"
	wlEdgeCache        = "edge-cache"
	depAPIBackend      = "api-backend-a"
	placementUSCentral = "us-central"
	cityDFW            = "DFW"
)

// fakeReader serves canned objects so the tools can be exercised without a
// cluster. Keyed by workload name, matching the Reader interface's shape.
type fakeReader struct {
	workloads   []computev1alpha.Workload
	deployments map[string][]computev1alpha.WorkloadDeployment
	instances   map[string][]computev1alpha.Instance
	err         error
}

var _ Reader = (*fakeReader)(nil)

func (f *fakeReader) ListWorkloads(_ context.Context, _ string) ([]computev1alpha.Workload, error) {
	return f.workloads, f.err
}

func (f *fakeReader) GetWorkload(_ context.Context, _, name string) (*computev1alpha.Workload, error) {
	if f.err != nil {
		return nil, f.err
	}
	for i := range f.workloads {
		if f.workloads[i].Name == name {
			return &f.workloads[i], nil
		}
	}
	return nil, errors.New("workload not found")
}

func (f *fakeReader) ListDeployments(
	_ context.Context, _, workload string,
) ([]computev1alpha.WorkloadDeployment, error) {
	return f.deployments[workload], f.err
}

func (f *fakeReader) ListInstances(
	_ context.Context, _, workload string,
) ([]computev1alpha.Instance, error) {
	return f.instances[workload], f.err
}

// fixtureReader builds a small fleet spanning the states that matter: one
// healthy workload, one blocked on quota (partially serving), and one blocked
// by a platform-side placement fault.
func fixtureReader() *fakeReader {
	healthy := workload(wlWebFrontend, 3, 3,
		cond(computev1alpha.WorkloadAvailable, "True",
			computev1alpha.WorkloadDeploymentReasonStableInstanceFound, "Serving."))

	quotaBlocked := workload(wlAPIBackend, 2, 6,
		cond(computev1alpha.WorkloadAvailable, "False",
			computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, "Quota is blocking 4 of 6 instances."))

	placementBlocked := workload(wlEdgeCache, 0, 4,
		cond(computev1alpha.WorkloadAvailable, "False",
			computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments."))

	readyInstance := func(name string) computev1alpha.Instance {
		i := instance(name,
			cond(computev1alpha.InstanceReady, "True",
				computev1alpha.InstanceReadyReasonAvailable, "Serving."))
		i.Labels = map[string]string{
			computev1alpha.WorkloadDeploymentNameLabel: depAPIBackend,
			computev1alpha.PlacementNameLabel:          placementUSCentral,
			computev1alpha.CityCodeLabel:               cityDFW,
		}
		return i
	}
	quotaInstance := func(name string) computev1alpha.Instance {
		i := instance(name,
			cond(computev1alpha.InstanceReady, "False",
				computev1alpha.InstanceReadyReasonSchedulingGatesPresent, "Gated pending quota."),
			cond(computev1alpha.InstanceQuotaGranted, "False",
				computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
				"Requested 4 vCPU / 16Gi; 2 vCPU / 8Gi remaining."))
		i.Labels = map[string]string{
			computev1alpha.WorkloadDeploymentNameLabel: depAPIBackend,
			computev1alpha.PlacementNameLabel:          placementUSCentral,
			computev1alpha.CityCodeLabel:               cityDFW,
		}
		return i
	}

	edgeDeployment := deployment("edge-cache-ams",
		cond(computev1alpha.WorkloadDeploymentAvailable, "False",
			computev1alpha.WorkloadDeploymentReasonNoMatchingLocation,
			"The cell has not been told which location it serves."))
	edgeDeployment.Spec.PlacementName = "ams-edge"
	edgeDeployment.Spec.CityCode = "AMS"

	apiDeployment := deployment(depAPIBackend,
		cond(computev1alpha.WorkloadDeploymentAvailable, "False",
			computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, "Quota is blocking 4 instances."))
	apiDeployment.Spec.PlacementName = placementUSCentral
	apiDeployment.Spec.CityCode = cityDFW

	return &fakeReader{
		workloads: []computev1alpha.Workload{*healthy, *quotaBlocked, *placementBlocked},
		deployments: map[string][]computev1alpha.WorkloadDeployment{
			wlAPIBackend: {apiDeployment},
			wlEdgeCache:  {edgeDeployment},
		},
		instances: map[string][]computev1alpha.Instance{
			wlWebFrontend: {readyInstance("web-1"), readyInstance("web-2"), readyInstance("web-3")},
			wlAPIBackend:  {readyInstance("api-ok-1"), readyInstance("api-ok-2"), quotaInstance("api-blocked-1")},
		},
	}
}

func fixtureDeps(r Reader) DepsFor {
	return func(context.Context) (ToolDeps, error) {
		return ToolDeps{Reader: r, Namespace: testNamespace}, nil
	}
}

func TestWorkloadsListReportsRootCauseAndOrdersWorstFirst(t *testing.T) {
	deps := fixtureDeps(fixtureReader())

	_, out, err := workloadsList(deps)(context.Background(), nil, WorkloadsListInput{})
	if err != nil {
		t.Fatalf("workloads_list: %v", err)
	}
	if len(out.Workloads) != 3 {
		t.Fatalf("got %d workloads, want 3", len(out.Workloads))
	}

	// The healthy workload must sort last so "what is broken" reads first.
	if last := out.Workloads[len(out.Workloads)-1]; last.Workload != wlWebFrontend {
		t.Errorf("last row = %q, want web-frontend (healthy workloads sort last)", last.Workload)
	}

	byName := make(map[string]WorkloadSummary, len(out.Workloads))
	for _, w := range out.Workloads {
		byName[w.Workload] = w
	}

	tests := []struct {
		workload          string
		wantAvailable     bool
		wantReason        string
		wantActionability Actionability
		wantReady         string
	}{
		{wlWebFrontend, true, "", "", "3/3"},
		{wlAPIBackend, false, computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded, ActionabilityUser, "2/3"},
		{wlEdgeCache, false, computev1alpha.WorkloadDeploymentReasonNoMatchingLocation, ActionabilityPlatform, "0/0"},
	}
	for _, tc := range tests {
		got, ok := byName[tc.workload]
		if !ok {
			t.Errorf("%s missing from the fleet view", tc.workload)
			continue
		}
		if got.Available != tc.wantAvailable {
			t.Errorf("%s Available = %v, want %v", tc.workload, got.Available, tc.wantAvailable)
		}
		if got.RootCauseReason != tc.wantReason {
			t.Errorf("%s RootCauseReason = %q, want %q", tc.workload, got.RootCauseReason, tc.wantReason)
		}
		if got.Actionability != tc.wantActionability {
			t.Errorf("%s Actionability = %q, want %q", tc.workload, got.Actionability, tc.wantActionability)
		}
		if got.ReadyReplicas != tc.wantReady {
			t.Errorf("%s ReadyReplicas = %q, want %q", tc.workload, got.ReadyReplicas, tc.wantReady)
		}
	}
}

func TestWorkloadsGetReturnsFullTree(t *testing.T) {
	deps := fixtureDeps(fixtureReader())

	_, out, err := workloadsGet(deps)(context.Background(), nil, WorkloadsGetInput{Name: wlAPIBackend})
	if err != nil {
		t.Fatalf("workloads_get: %v", err)
	}

	if out.Workload.Name != wlAPIBackend || out.Workload.Namespace != testNamespace {
		t.Errorf("workload = %s/%s, want %s/api-backend", out.Workload.Namespace, out.Workload.Name, testNamespace)
	}
	if len(out.Workload.Conditions) == 0 {
		t.Error("workload conditions are empty")
	}
	if len(out.Deployments) != 1 {
		t.Fatalf("got %d deployments, want 1", len(out.Deployments))
	}
	if d := out.Deployments[0]; d.Placement != placementUSCentral || d.CityCode != cityDFW {
		t.Errorf("deployment placement/city = %q/%q, want us-central/DFW", d.Placement, d.CityCode)
	}
	if len(out.Instances) != 3 {
		t.Fatalf("got %d instances, want 3", len(out.Instances))
	}
	// Deployment and placement come from labels, so no extra lookup is needed.
	if i := out.Instances[0]; i.Deployment != depAPIBackend || i.Placement != placementUSCentral {
		t.Errorf("instance deployment/placement = %q/%q, want api-backend-a/us-central", i.Deployment, i.Placement)
	}
}

func TestWorkloadsGetRequiresName(t *testing.T) {
	deps := fixtureDeps(fixtureReader())
	if _, _, err := workloadsGet(deps)(context.Background(), nil, WorkloadsGetInput{}); err == nil {
		t.Error("expected an error when name is empty")
	}
}

func TestInstancesListFilters(t *testing.T) {
	deps := fixtureDeps(fixtureReader())

	_, filtered, err := instancesList(deps)(context.Background(), nil, InstancesListInput{Workload: wlAPIBackend})
	if err != nil {
		t.Fatalf("instances_list filtered: %v", err)
	}
	if len(filtered.Instances) != 3 {
		t.Errorf("filtered instances = %d, want 3", len(filtered.Instances))
	}

	_, all, err := instancesList(deps)(context.Background(), nil, InstancesListInput{})
	if err != nil {
		t.Fatalf("instances_list unfiltered: %v", err)
	}
	// web-frontend has 3, api-backend has 3, edge-cache has none.
	if len(all.Instances) != 6 {
		t.Errorf("unfiltered instances = %d, want 6", len(all.Instances))
	}
}

// TestWorkloadDiagnoseSurfacesLeafCause is the tool-level restatement of the
// property the whole plugin turns on: the pointer reason on the workload must
// never be the answer.
func TestWorkloadDiagnoseSurfacesLeafCause(t *testing.T) {
	deps := fixtureDeps(fixtureReader())

	tests := []struct {
		workload          string
		wantReason        string
		wantLevel         Level
		wantActionability Actionability
	}{
		{
			workload:          wlAPIBackend,
			wantReason:        computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
			wantLevel:         LevelInstance,
			wantActionability: ActionabilityUser,
		},
		{
			workload:          wlEdgeCache,
			wantReason:        computev1alpha.WorkloadDeploymentReasonNoMatchingLocation,
			wantLevel:         LevelDeployment,
			wantActionability: ActionabilityPlatform,
		},
	}

	for _, tc := range tests {
		t.Run(tc.workload, func(t *testing.T) {
			_, d, err := workloadDiagnose(deps)(
				context.Background(), nil, WorkloadDiagnoseInput{Name: tc.workload})
			if err != nil {
				t.Fatalf("workload_diagnose: %v", err)
			}
			if d.RootCause == nil {
				t.Fatalf("RootCause is nil, want %q", tc.wantReason)
			}
			if d.RootCause.Reason != tc.wantReason {
				t.Errorf("RootCause.Reason = %q, want %q", d.RootCause.Reason, tc.wantReason)
			}
			if d.RootCause.Level != tc.wantLevel {
				t.Errorf("RootCause.Level = %q, want %q", d.RootCause.Level, tc.wantLevel)
			}
			if d.RootCause.Actionability != tc.wantActionability {
				t.Errorf("Actionability = %q, want %q", d.RootCause.Actionability, tc.wantActionability)
			}
			if IsPointerReason(d.RootCause.Reason) {
				t.Errorf("RootCause.Reason %q is a pointer reason and must never be the answer",
					d.RootCause.Reason)
			}
		})
	}
}

func TestReasonExplain(t *testing.T) {
	deps := fixtureDeps(fixtureReader())
	ctx := context.Background()

	_, one, err := reasonExplain(deps)(ctx, nil, ReasonExplainInput{Reason: "QuotaNoBudget"})
	if err != nil {
		t.Fatalf("reason_explain: %v", err)
	}
	if one.Reason == nil {
		t.Fatal("Reason is nil")
	}
	if one.Reason.Actionability != ActionabilityPlatform {
		t.Errorf("QuotaNoBudget actionability = %q, want platform", one.Reason.Actionability)
	}
	if len(one.Reasons) != 0 {
		t.Error("Reasons should be empty when a single reason was requested")
	}

	_, all, err := reasonExplain(deps)(ctx, nil, ReasonExplainInput{})
	if err != nil {
		t.Fatalf("reason_explain (all): %v", err)
	}
	if len(all.Reasons) != len(AllReasons()) {
		t.Errorf("got %d reasons, want the whole catalog (%d)", len(all.Reasons), len(AllReasons()))
	}

	if _, _, err := reasonExplain(deps)(ctx, nil, ReasonExplainInput{Reason: "NotARealReason"}); err == nil {
		t.Error("expected an error for an unknown reason")
	}
}

// TestToolsFailWhenDepsUnavailable covers the unauthenticated path: a request
// with no credentials must fail every tool, including the catalog-only one.
func TestToolsFailWhenDepsUnavailable(t *testing.T) {
	denied := func(context.Context) (ToolDeps, error) {
		return ToolDeps{}, errors.New("no bearer token on the request")
	}
	ctx := context.Background()

	if _, _, err := workloadsList(denied)(ctx, nil, WorkloadsListInput{}); err == nil {
		t.Error("workloads_list should fail without deps")
	}
	if _, _, err := workloadsGet(denied)(ctx, nil, WorkloadsGetInput{Name: "x"}); err == nil {
		t.Error("workloads_get should fail without deps")
	}
	if _, _, err := instancesList(denied)(ctx, nil, InstancesListInput{}); err == nil {
		t.Error("instances_list should fail without deps")
	}
	if _, _, err := workloadDiagnose(denied)(ctx, nil, WorkloadDiagnoseInput{Name: "x"}); err == nil {
		t.Error("workload_diagnose should fail without deps")
	}
	if _, _, err := reasonExplain(denied)(ctx, nil, ReasonExplainInput{}); err == nil {
		t.Error("reason_explain should fail without deps: it must not be a probe for unauthenticated callers")
	}
}

// TestReaderErrorsPropagate ensures a control-plane failure surfaces rather
// than being reported as an empty, healthy-looking fleet.
func TestReaderErrorsPropagate(t *testing.T) {
	r := fixtureReader()
	r.err = errors.New("control plane unreachable")
	deps := fixtureDeps(r)

	if _, _, err := workloadsList(deps)(context.Background(), nil, WorkloadsListInput{}); err == nil {
		t.Error("workloads_list should surface a reader error")
	}
}

// TestRegisterToolsPublishesExactlyTheReadOnlySet connects a real MCP client to
// a registered server and inspects what it actually advertises.
//
// It guards the tool surface itself: compute publishes five read-only tools and
// no mutating one, so anything extra appearing over the wire is a bug. It also
// catches a schema that fails to infer, since AddTool panics on a bad one.
func TestRegisterToolsPublishesExactlyTheReadOnlySet(t *testing.T) {
	ctx := context.Background()

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	RegisterTools(server, fixtureDeps(fixtureReader()))

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	got := make(map[string]string, len(res.Tools))
	for _, tool := range res.Tools {
		got[tool.Name] = tool.Description
	}

	want := []string{
		ToolWorkloadsList,
		ToolWorkloadsGet,
		ToolInstancesList,
		ToolWorkloadDiagnose,
		ToolReasonExplain,
	}
	if len(got) != len(want) {
		t.Errorf("published %d tools %v, want exactly %d", len(got), keysOf(got), len(want))
	}
	for _, name := range want {
		desc, ok := got[name]
		if !ok {
			t.Errorf("tool %q is not published", name)
			continue
		}
		// The description is what tells the model when to reach for a tool;
		// an empty one silently degrades every answer.
		if desc == "" {
			t.Errorf("tool %q has no description", name)
		}
	}

	// Compute ships no mutating tool. Enforcement of the allow-list is the
	// gateway's job, but a tool that does not exist cannot be called at all.
	for name := range got {
		for _, forbidden := range []string{"delete", "create", "update", "scale", "restart"} {
			if strings.Contains(name, forbidden) {
				t.Errorf("tool %q looks mutating; compute publishes read-only tools only", name)
			}
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// wedgedReader is the staging shape: one workload whose only instance has sat
// in a provider-side transient state since 2026-08-31.
func wedgedReader(since time.Time) *fakeReader {
	w := workload("xcheck-iad", 0, 1,
		condAt(computev1alpha.WorkloadAvailable, "False",
			computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments.", since))
	inst := instance("xcheck-iad-iad-0",
		condAt(computev1alpha.InstanceProgrammed, "False",
			computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
			"The infrastructure provider is programming the instance.", since))
	return &fakeReader{
		workloads: []computev1alpha.Workload{*w},
		instances: map[string][]computev1alpha.Instance{"xcheck-iad": {inst}},
	}
}

// TestWorkloadsListCarriesTheAgeOfTheRootCause is the fleet-view half of the
// staging failure: the row itself has to say how old the state is, because the
// fleet view is where the "is anything wrong?" question is actually answered.
func TestWorkloadsListCarriesTheAgeOfTheRootCause(t *testing.T) {
	deps := fixtureDeps(wedgedReader(time.Now().Add(-5 * 24 * time.Hour)))

	_, out, err := workloadsList(deps)(context.Background(), nil, WorkloadsListInput{})
	if err != nil {
		t.Fatalf("workloads_list: %v", err)
	}
	if len(out.Workloads) != 1 {
		t.Fatalf("got %d workloads, want 1", len(out.Workloads))
	}

	row := out.Workloads[0]
	if row.RootCauseFor != "5d" {
		t.Errorf("RootCauseFor = %q, want %q", row.RootCauseFor, "5d")
	}
	if _, err := time.Parse(time.RFC3339, row.RootCauseSince); err != nil {
		t.Errorf("RootCauseSince = %q, want an RFC 3339 timestamp: %v", row.RootCauseSince, err)
	}
}

// A healthy row carries no age, so the fleet view stays quiet about workloads
// nobody asked about.
func TestWorkloadsListOmitsAgeForHealthyWorkloads(t *testing.T) {
	deps := fixtureDeps(fixtureReader())

	_, out, err := workloadsList(deps)(context.Background(), nil, WorkloadsListInput{})
	if err != nil {
		t.Fatalf("workloads_list: %v", err)
	}
	for _, row := range out.Workloads {
		if row.Workload == wlWebFrontend && (row.RootCauseSince != "" || row.RootCauseFor != "") {
			t.Errorf("healthy row carries an age: since=%q for=%q", row.RootCauseSince, row.RootCauseFor)
		}
	}
}

// TestWorkloadsListFlagsTheStagingStall reproduces the failure that motivated
// all of this, end to end through the tool the assistant actually calls first.
//
// On 2026-09-05 the fleet view reported xcheck-iad as ProgrammingInProgress /
// transient, and the assistant answered "normal in-flight state, wait a few
// minutes". The condition had last transitioned on 2026-08-31. Five days of
// "in flight" must not read as healthy.
func TestWorkloadsListFlagsTheStagingStall(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	since := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	deps := fixtureDeps(wedgedReader(since))

	_, out, err := workloadsList(deps)(context.Background(), nil, WorkloadsListInput{})
	if err != nil {
		t.Fatalf("workloads_list: %v", err)
	}
	row := out.Workloads[0]
	if row.RootCauseReason != computev1alpha.InstanceProgrammedReasonProgrammingInProgress {
		t.Fatalf("RootCauseReason = %q, want ProgrammingInProgress", row.RootCauseReason)
	}
	if row.Actionability != ActionabilityStalled {
		t.Errorf("Actionability = %q, want %q — a five-day ProgrammingInProgress is not in-flight work",
			row.Actionability, ActionabilityStalled)
	}
	if row.RootCauseSince != since.Format(time.RFC3339) {
		t.Errorf("RootCauseSince = %q, want %q", row.RootCauseSince, since.Format(time.RFC3339))
	}

	// The same workload through the deep path, with the clock pinned so the
	// advice can be asserted exactly.
	w, err := wedgedReader(since).GetWorkload(context.Background(), testNamespace, "xcheck-iad")
	if err != nil {
		t.Fatalf("GetWorkload: %v", err)
	}
	d := DiagnoseAt(now, w, nil, wedgedReader(since).instances["xcheck-iad"])
	if d.RootCause.Actionability != ActionabilityStalled {
		t.Fatalf("RootCause.Actionability = %q, want stalled", d.RootCause.Actionability)
	}
	if d.RootCause.InStateFor != "5d" {
		t.Errorf("InStateFor = %q, want %q", d.RootCause.InStateFor, "5d")
	}
	if d.RootCause.Skill != SkillStalledTransient {
		t.Errorf("Skill = %q, want %q", d.RootCause.Skill, SkillStalledTransient)
	}
	// "Wait." was the catalogued advice and the wrong answer; it must not
	// survive the escalation.
	if strings.Contains(d.RootCause.Remediation, "Wait.") {
		t.Errorf("Remediation still says to wait: %q", d.RootCause.Remediation)
	}
	if !strings.Contains(d.RootCause.Remediation, "5d") {
		t.Errorf("Remediation = %q, want it to quote how long the state has held", d.RootCause.Remediation)
	}
	if !strings.Contains(d.Summary, "stuck") {
		t.Errorf("Summary = %q, want it to say the state is stuck rather than in flight", d.Summary)
	}
}

// TestWorkloadsListLeavesFreshTransientStateAlone is the other half: the same
// workload minutes old must still read as in-flight, or the escalation is just
// a new way to be wrong.
func TestWorkloadsListLeavesFreshTransientStateAlone(t *testing.T) {
	deps := fixtureDeps(wedgedReader(time.Now().Add(-2 * time.Minute)))

	_, out, err := workloadsList(deps)(context.Background(), nil, WorkloadsListInput{})
	if err != nil {
		t.Fatalf("workloads_list: %v", err)
	}
	if got := out.Workloads[0].Actionability; got != ActionabilityTransient {
		t.Errorf("Actionability = %q, want %q for a two-minute-old ProgrammingInProgress",
			got, ActionabilityTransient)
	}
}
