// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	testWorkload      = "api"
	testContainer     = "app"
	testInstance      = "api-default-us-central-1-0"
	testOtherInstance = "api-default-us-east-1-0"
	testVMWorkload    = "bare"
)

func newFakeKube(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		computev1alpha.AddToScheme, networkingv1alpha.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func meta(name string, labels map[string]string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: util.ResourceNamespace, Labels: labels}
}

func proxy(name string, labels map[string]string, services ...string) *networkingv1alpha.HTTPProxy {
	p := &networkingv1alpha.HTTPProxy{ObjectMeta: meta(name, labels)}
	backends := make([]networkingv1alpha.HTTPProxyRuleBackend, 0, len(services))
	for _, s := range services {
		backends = append(backends, networkingv1alpha.HTTPProxyRuleBackend{
			NetworkService: &networkingv1alpha.NetworkServiceBackendRef{Name: s},
		})
	}
	p.Spec.Rules = []networkingv1alpha.HTTPProxyRule{{Backends: backends}}
	return p
}

// parse runs flag parsing and validation without executing the command.
func parse(t *testing.T, args ...string) (*options, error) {
	t.Helper()
	cmd, opts := newCommand()
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return opts, validate(cmd, opts)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		args     []string
		wantErr  string
		wantTail int
	}{
		{args: nil, wantTail: defaultTail},
		{args: []string{"--since=1h"}, wantTail: -1},
		{args: []string{"--since-time=2026-09-30T00:00:00Z"}, wantTail: -1},
		{args: []string{"--since=2h", "--tail=5"}, wantTail: 5},
		{args: []string{"-o", "table"}, wantTail: defaultTail},
		{args: []string{"--since=30m", "--since-time=2026-09-30T00:00:00Z"}, wantErr: "mutually exclusive"},
		{args: []string{"-f", "--until=2026-09-30T00:00:00Z"}, wantErr: "--until"},
		{args: []string{"--previous", "--current"}, wantErr: "mutually exclusive"},
		{args: []string{"-f", "--current"}, wantErr: "--follow"},
		{args: []string{"--tail=-2"}, wantErr: "--tail"},
		{args: []string{"--alb", "--instance=x"}, wantErr: "--instance"},
		{args: []string{"--alb", "--search=x"}, wantErr: "--search"},
		{args: []string{"-o", "yaml"}, wantErr: "unsupported output"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			opts, err := parse(t, tt.args...)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("validate() = %v, want an error mentioning %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate() = %v", err)
			}
			if opts.tail != tt.wantTail {
				t.Errorf("tail = %d, want %d", opts.tail, tt.wantTail)
			}
		})
	}
}

func TestResolveWindow(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		opts      options
		wantStart time.Time
		wantEnd   time.Time
		wantErr   bool
	}{
		{opts: options{}, wantStart: now.Add(-defaultLookback), wantEnd: now},
		{opts: options{current: true}, wantStart: now.Add(-generationLookback), wantEnd: now},
		{opts: options{since: "7d"}, wantStart: now.Add(-7 * 24 * time.Hour), wantEnd: now},
		{opts: options{since: "90m", until: "2026-09-30T10:00:00Z"}, wantStart: now.Add(-210 * time.Minute), wantEnd: now.Add(-2 * time.Hour)},
		{opts: options{sinceTime: "2026-09-29T00:00:00Z"}, wantStart: now.Add(-36 * time.Hour), wantEnd: now},
		{opts: options{since: "soon"}, wantErr: true},
		{opts: options{since: "0d"}, wantErr: true},
		{opts: options{since: "-1h"}, wantErr: true},
		{opts: options{until: "yesterday"}, wantErr: true},
		{opts: options{sinceTime: "2026-09-30T13:00:00Z"}, wantErr: true},
	}
	for _, tt := range tests {
		w, err := resolveWindow(&tt.opts, now)
		if (err != nil) != tt.wantErr {
			t.Errorf("resolveWindow(%+v) error = %v, wantErr %v", tt.opts, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && (!w.start.Equal(tt.wantStart) || !w.end.Equal(tt.wantEnd)) {
			t.Errorf("resolveWindow(%+v) = [%v, %v), want [%v, %v)", tt.opts, w.start, w.end, tt.wantStart, tt.wantEnd)
		}
	}
}

func TestExecute(t *testing.T) {
	defer func(v bool) { color.NoColor = v }(color.NoColor)
	color.NoColor = true

	sec := time.Second.Nanoseconds()
	byWorkload := map[string]string{computev1alpha.WorkloadNameLabel: testWorkload}
	api := &computev1alpha.Workload{ObjectMeta: meta(testWorkload, nil)}
	api.Spec.Template.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
		Containers: []computev1alpha.SandboxContainer{{Name: testContainer}, {Name: "sidecar"}},
	}
	unikernel := &computev1alpha.Workload{ObjectMeta: meta("uk", nil)}
	unikernel.Spec.Template.Spec.Runtime.Class = "tiny"
	unikernel.Spec.Template.Spec.Runtime.Sandbox = api.Spec.Template.Spec.Runtime.Sandbox
	kube := newFakeKube(t,
		api,
		unikernel,
		&computev1alpha.RuntimeClass{
			ObjectMeta: metav1.ObjectMeta{Name: "tiny"},
			Spec:       computev1alpha.RuntimeClassSpec{Isolation: computev1alpha.RuntimeClassIsolation{Boundary: isolationUnikernel}},
		},
		&computev1alpha.Workload{ObjectMeta: meta(testVMWorkload, nil)},
		&networkingv1alpha.NetworkService{
			ObjectMeta: meta("api-svc", nil),
			Spec: networkingv1alpha.NetworkServiceSpec{
				NetworkInterfaces: networkingv1alpha.NetworkServiceInterfaceSelector{
					Selector: metav1.LabelSelector{MatchLabels: byWorkload},
				},
			},
		},
		proxy("cli", byWorkload),
		proxy("hand-written", nil, "api-svc"),
		proxy("unrelated", nil, "other-svc"),
	)
	appLines := []entry{
		genLine(2*sec, testInstance, labelVMGeneration, "g2"),
		genLine(1*sec, testInstance, labelVMGeneration, "g1"),
	}
	for i := range appLines {
		appLines[i].line = "hello " + appLines[i].labels[labelVMGeneration]
	}

	tests := []struct {
		name       string
		workload   string
		opts       options
		entries    []entry
		values     map[string][]string
		wantErr    string
		wantQuery  string // the final query
		wantOut    string
		wantStatus string // substring
	}{
		{
			name:     "filters",
			workload: testWorkload,
			opts: options{
				instances: []string{"default-us-central-1-0"},
				locations: []string{"US-Central-1"},
				container: testContainer,
				search:    "hello",
			},
			entries: appLines,
			wantQuery: `{datum_workload_name="api", datum_instance_name="api-default-us-central-1-0", ` +
				`datum_instance_name=~"api-.+-(us-central-1)-[0-9]+", k8s_container_name="app"} |= "hello"`,
			wantOut:    "[default-us-central-1-0] hello g1\n[default-us-central-1-0] hello g2\n",
			wantStatus: "new generation g2",
		},
		{
			name:       "generation found before the search applies",
			workload:   testWorkload,
			opts:       options{previous: true, search: "hello"},
			entries:    appLines,
			wantQuery:  `{datum_workload_name="api", container_id="g1"} |= "hello"`,
			wantOut:    "[default-us-central-1-0] hello g1\n[default-us-central-1-0] hello g2\n",
			wantStatus: "Showing the previous generation of 1 instance(s).",
		},
		{
			name:       "deleted workload",
			workload:   "gone",
			values:     map[string][]string{labelWorkload: {"gone"}},
			wantQuery:  `{datum_workload_name="gone"}`,
			wantStatus: "no longer exists",
		},
		{name: "typo", workload: "nope", wantErr: `workload "nope" not found`},
		{name: "container on a VM workload", workload: testVMWorkload, opts: options{container: testContainer}, wantErr: "runs as a VM"},
		{name: "container on a unikernel workload", workload: "uk", opts: options{container: testContainer}, wantErr: "runs as a unikernel"},
		{name: "unknown container", workload: "api", opts: options{container: "db"}, wantErr: `no container "db" (it has app, sidecar)`},
		{
			name:     "instance outside the location",
			workload: testWorkload,
			opts:     options{instances: []string{"default-us-east-1-0"}, locations: []string{"us-central-1"}},
			wantErr:  "--instance default-us-east-1-0 is not in --location us-central-1",
		},
		{
			name:     "access logs",
			workload: testWorkload,
			opts:     options{alb: true},
			entries: []entry{{ts: sec, labels: map[string]string{
				labelMethod: "GET", labelResponseCode: "200", labelDuration: "3", labelPath: "/",
			}}},
			wantQuery: `{route_name=~"httproute/[^/]+/(cli|hand-written)/.*"}`,
			wantOut:   "GET    200      3ms  /\n",
		},
		{name: "access logs without a URL", workload: testVMWorkload, opts: options{alb: true}, wantErr: "no published URL"},
		{
			name:       "nothing in the window",
			workload:   testWorkload,
			wantQuery:  `{datum_workload_name="api"}`,
			wantStatus: "No log lines for workload \"api\"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &fakeQuerier{entries: tt.entries, values: tt.values}
			var out, status strings.Builder
			opts := tt.opts
			opts.prefix, opts.tail = true, -1

			err := execute(context.Background(), env{kube, q, &out, &status}, tt.workload, &opts, window{start: epoch, end: far, tail: -1})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("execute() = %v, want an error mentioning %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("execute() = %v", err)
			}
			last := len(q.queries) - 1
			if got := q.queries[last].query; got != tt.wantQuery {
				t.Errorf("query = %s\nwant    %s", got, tt.wantQuery)
			}
			for _, r := range q.queries[:last] {
				if strings.Contains(r.query, "|=") {
					t.Errorf("generation discovery used the search: %s", r.query)
				}
			}
			if out.String() != tt.wantOut {
				t.Errorf("out = %q, want %q", out.String(), tt.wantOut)
			}
			if !strings.Contains(status.String(), tt.wantStatus) {
				t.Errorf("status = %q, want it to contain %q", status.String(), tt.wantStatus)
			}
		})
	}
}

func TestPortalLogsURL(t *testing.T) {
	tests := []struct {
		apiHost, instance, want string
		wantErr                 bool
	}{
		{apiHost: "api.staging.env.datum.net", want: "https://cloud.staging.env.datum.net/project/p/services/compute-datumapis-com/api/logs"},
		{apiHost: "api.datum.net", instance: testOtherInstance, want: "https://cloud.datum.net/project/p/services/compute-datumapis-com/api/instances/api-default-us-east-1-0/logs"},
		{apiHost: "api.example.com", wantErr: true},
	}
	for _, tt := range tests {
		got, err := portalLogsURL(tt.apiHost, "p", testWorkload, tt.instance)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("portalLogsURL(%q, %q) = %q, %v, want %q", tt.apiHost, tt.instance, got, err, tt.want)
		}
	}
}

func TestCompletionCandidates(t *testing.T) {
	byWorkload := map[string]string{computev1alpha.WorkloadNameLabel: testWorkload}
	wl := &computev1alpha.Workload{ObjectMeta: meta(testWorkload, nil)}
	wl.Spec.Template.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
		Containers: []computev1alpha.SandboxContainer{{Name: testContainer}, {Name: "worker"}},
	}
	kube := newFakeKube(t,
		wl,
		&computev1alpha.Workload{ObjectMeta: meta("vm", nil)},
		&computev1alpha.Instance{ObjectMeta: meta(testOtherInstance, byWorkload)},
		&computev1alpha.Instance{ObjectMeta: meta("web-default-us-east-1-0", map[string]string{computev1alpha.WorkloadNameLabel: "web"})},
	)
	ctx := context.Background()

	for _, tt := range []struct {
		name     string
		complete func(context.Context, client.Client, string) ([]string, error)
		workload string
		want     []string
	}{
		{"instances by short name", instanceNames, testWorkload, []string{"default-us-east-1-0"}},
		{"sandbox containers", containerNames, testWorkload, []string{testContainer, "worker"}},
		{"no containers on a VM workload", containerNames, "vm", nil},
	} {
		got, err := tt.complete(ctx, kube, tt.workload)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %v, %v, want %v", tt.name, got, err, tt.want)
		}
	}
}
