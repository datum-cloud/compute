// SPDX-License-Identifier: AGPL-3.0-only

package workloads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	testProject   = "acme-prod"
	locEast       = "us-east-1"
	locWest       = "eu-west-1"
	locFra        = "ap-south-1"
	locLhr        = "sa-east-1"
	workerName    = "worker"
	testCanonical = "a1b2c3d4.datumproxy.net"
	testCustom    = "api.example.com"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := computev1alpha.AddToScheme(s); err != nil {
		t.Fatalf("registering compute scheme: %v", err)
	}
	if err := networkingv1alpha.AddToScheme(s); err != nil {
		t.Fatalf("registering networking scheme: %v", err)
	}
	return s
}

// workload returns a sandbox workload with one placement in the given locations.
func workload(name, uid, image string, locations ...string) *computev1alpha.Workload {
	return &computev1alpha.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: util.ResourceNamespace,
			UID:       types.UID(uid),
		},
		Spec: computev1alpha.WorkloadSpec{
			Template: computev1alpha.InstanceTemplateSpec{
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Sandbox: &computev1alpha.SandboxRuntime{
							Containers: []computev1alpha.SandboxContainer{{Name: name, Image: image}},
						},
					},
				},
			},
			Placements: []computev1alpha.WorkloadPlacement{{
				Name:      "default",
				Locations: locationRefs(locations...),
			}},
		},
		Status: computev1alpha.WorkloadStatus{
			Conditions: []metav1.Condition{{
				Type:   computev1alpha.WorkloadAvailable,
				Status: metav1.ConditionTrue,
				Reason: "Available",
			}},
		},
	}
}

// locationRefs turns location names into the references a placement stores.
func locationRefs(names ...string) []locationsv1alpha1.LocationReference {
	refs := make([]locationsv1alpha1.LocationReference, 0, len(names))
	for _, n := range names {
		refs = append(refs, locationsv1alpha1.LocationReference{Name: n})
	}
	return refs
}

// deployment returns a deployment for a workload in one location.
func deployment(name, workloadUID, location string, ready, desired int32) *computev1alpha.WorkloadDeployment {
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: util.ResourceNamespace,
			Labels:    map[string]string{computev1alpha.WorkloadUIDLabel: workloadUID},
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			LocationRef:   locationsv1alpha1.LocationReference{Name: location},
			PlacementName: "default",
		},
		Status: computev1alpha.WorkloadDeploymentStatus{
			ReadyReplicas:   ready,
			UpdatedReplicas: ready,
			DesiredReplicas: desired,
		},
	}
}

// publishedProxy is the proxy the platform reports for a live URL.
func publishedProxy(workloadName string, customHostnames ...string) *networkingv1alpha.HTTPProxy {
	hostnames := make([]gatewayv1.Hostname, 0, len(customHostnames))
	// A custom hostname serves on the strength of its own status entry and
	// nothing else: the proxy-level conditions are a roll-up over every
	// hostname and cannot vouch for any one of them.
	statuses := make([]networkingv1alpha.HostnameStatus, 0, len(customHostnames))
	for _, h := range customHostnames {
		hostnames = append(hostnames, gatewayv1.Hostname(h))
		statuses = append(statuses, networkingv1alpha.HostnameStatus{
			Hostname: h,
			Conditions: []metav1.Condition{
				{Type: networkingv1alpha.HostnameConditionVerified, Status: metav1.ConditionTrue, Reason: "Verified"},
				{Type: networkingv1alpha.HostnameConditionDNSRecordProgrammed, Status: metav1.ConditionTrue, Reason: "RecordCreated"},
				{Type: networkingv1alpha.HostnameConditionCertificateReady, Status: metav1.ConditionTrue, Reason: "CertificateIssued"},
			},
		})
	}
	return &networkingv1alpha.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workloadName,
			Namespace: util.ResourceNamespace,
			Labels:    map[string]string{computev1alpha.WorkloadNameLabel: workloadName},
		},
		Spec: networkingv1alpha.HTTPProxySpec{Hostnames: hostnames},
		Status: networkingv1alpha.HTTPProxyStatus{
			CanonicalHostname: testCanonical,
			HostnameStatuses:  statuses,
			Conditions: []metav1.Condition{
				{Type: networkingv1alpha.HTTPProxyConditionProgrammed, Status: metav1.ConditionTrue, Reason: "Programmed"},
				{Type: networkingv1alpha.HTTPProxyConditionCertificatesReady, Status: metav1.ConditionTrue, Reason: "Issued"},
			},
		},
	}
}

func publishedService(workloadName string) *networkingv1alpha.NetworkService {
	return &networkingv1alpha.NetworkService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workloadName,
			Namespace: util.ResourceNamespace,
			Labels:    map[string]string{computev1alpha.WorkloadNameLabel: workloadName},
		},
		Spec: networkingv1alpha.NetworkServiceSpec{
			Ports: []networkingv1alpha.NetworkServicePort{{Name: "http", Port: 8080}},
		},
		Status: networkingv1alpha.NetworkServiceStatus{
			Summary: networkingv1alpha.NetworkServiceSummary{Locations: 2, Members: 4, Healthy: 4},
		},
	}
}

func newFakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
}

// project returns the objects for a project with a published wlAPI and an
// unpublished workerName.
func projectObjects() []client.Object {
	return []client.Object{
		workload(wlAPI, "uid-api", "ghcr.io/acme/api:1.4.2", locEast, locWest),
		workload(workerName, "uid-worker", "ghcr.io/acme/worker:2.0", locEast),
		deployment("api-dfw", "uid-api", locEast, 2, 2),
		deployment("api-iad", "uid-api", locWest, 2, 2),
		deployment("worker-dfw", "uid-worker", locEast, 1, 1),
		publishedProxy(wlAPI, testCustom),
		publishedService(wlAPI),
	}
}

func TestListWorkloadsTable(t *testing.T) {
	tests := []struct {
		name        string
		objs        []client.Object
		opts        listOptions
		wantLines   []string
		wantMissing []string
	}{
		{
			name: "url column carries the custom hostname, unpublished shows a dash",
			objs: projectObjects(),
			opts: listOptions{output: util.OutputTable},
			wantLines: []string{
				"NAME", "URL",
				wlAPI, "https://" + testCustom,
				workerName, noURL,
			},
		},
		{
			name: "managed hostname when there is no custom one",
			objs: []client.Object{
				workload(wlAPI, "uid-api", "ghcr.io/acme/api:1.4.2", locEast),
				deployment("api-dfw", "uid-api", locEast, 2, 2),
				publishedProxy(wlAPI),
				publishedService(wlAPI),
			},
			opts:      listOptions{output: util.OutputTable},
			wantLines: []string{"https://" + testCanonical},
		},
		{
			name:        "no-headers drops the header row but keeps the url",
			objs:        projectObjects(),
			opts:        listOptions{output: util.OutputTable, noHeaders: true},
			wantLines:   []string{"https://" + testCustom},
			wantMissing: []string{"UP-TO-DATE"},
		},
		{
			name:      "wide keeps the url last",
			objs:      projectObjects(),
			opts:      listOptions{output: util.OutputWide},
			wantLines: []string{"INSTANCE TYPE", "URL", "https://" + testCustom},
		},
		{
			name:        "location filter still resolves urls",
			objs:        projectObjects(),
			opts:        listOptions{output: util.OutputTable, location: locWest},
			wantLines:   []string{wlAPI, "https://" + testCustom},
			wantMissing: []string{workerName},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			c := newFakeClient(t, tc.objs...)

			if err := listWorkloads(context.Background(), &out, &errOut, c, testProject, tc.opts); err != nil {
				t.Fatalf("listWorkloads: %v", err)
			}

			got := out.String()
			for _, want := range tc.wantLines {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}
			for _, missing := range tc.wantMissing {
				if strings.Contains(got, missing) {
					t.Errorf("output should not contain %q:\n%s", missing, got)
				}
			}
			if errOut.Len() != 0 {
				t.Errorf("unexpected stderr: %s", errOut.String())
			}
		})
	}
}

// TestListWorkloadsURLField pins the scripting contract from the spec:
// `workloads -o json | jq -r '.[] | select(.name==wlAPI) | .url'`.
func TestListWorkloadsURLField(t *testing.T) {
	var out, errOut bytes.Buffer
	c := newFakeClient(t, projectObjects()...)

	if err := listWorkloads(context.Background(), &out, &errOut, c, testProject, listOptions{output: util.OutputJSON}); err != nil {
		t.Fatalf("listWorkloads: %v", err)
	}

	var views []map[string]any
	if err := json.Unmarshal(out.Bytes(), &views); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out.String())
	}
	if len(views) != 2 {
		t.Fatalf("got %d views, want 2:\n%s", len(views), out.String())
	}

	byName := map[string]map[string]any{}
	for _, v := range views {
		name, _ := v["name"].(string)
		byName[name] = v
	}

	if got := byName[wlAPI]["url"]; got != "https://"+testCustom {
		t.Errorf("api url = %v, want https://%s", got, testCustom)
	}
	// An unpublished workload carries no url field at all — see
	// TestListWorkloadsJSONNoURLVersusUnknown for why "" would be a lie.
	if _, ok := byName[workerName]["url"]; ok {
		t.Errorf("unpublished worker should carry no url field:\n%s", out.String())
	}

	// The raw resource is still there, whole, under "workload".
	raw, ok := byName[wlAPI]["workload"].(map[string]any)
	if !ok {
		t.Fatalf("api view has no workload object:\n%s", out.String())
	}
	if raw["spec"] == nil {
		t.Errorf("workload object lost its spec:\n%s", out.String())
	}
}

func TestListWorkloadsYAMLURLField(t *testing.T) {
	var out, errOut bytes.Buffer
	c := newFakeClient(t, projectObjects()...)

	if err := listWorkloads(context.Background(), &out, &errOut, c, testProject, listOptions{output: util.OutputYAML}); err != nil {
		t.Fatalf("listWorkloads: %v", err)
	}

	if !strings.Contains(out.String(), "url: https://"+testCustom) {
		t.Errorf("yaml missing url field:\n%s", out.String())
	}
}

func TestListWorkloadsEmptyJSONIsArray(t *testing.T) {
	var out, errOut bytes.Buffer
	c := newFakeClient(t)

	if err := listWorkloads(context.Background(), &out, &errOut, c, testProject, listOptions{output: util.OutputJSON}); err != nil {
		t.Fatalf("listWorkloads: %v", err)
	}

	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Errorf("empty project encoded as %q, want []", got)
	}
}

// TestListWorkloadsURLsUnreadable: a project whose URLs cannot be read still
// lists its workloads. The column says "unknown", not "none".
func TestListWorkloadsURLsUnreadable(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(projectObjects()...).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*networkingv1alpha.HTTPProxyList); ok {
					return errors.New("forbidden")
				}
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()

	var out, errOut bytes.Buffer
	if err := listWorkloads(context.Background(), &out, &errOut, c, testProject, listOptions{output: util.OutputTable}); err != nil {
		t.Fatalf("listWorkloads: %v", err)
	}

	if !strings.Contains(out.String(), wlAPI) {
		t.Errorf("workloads should still list when their URLs cannot be read:\n%s", out.String())
	}
	// Both rows say "unknown", and neither says "none" — an unreadable URL is
	// not the same claim as a workload having no URL.
	if got := strings.Count(out.String(), unknownURL); got != 2 {
		t.Errorf("got %d unknown URL cells, want 2:\n%s", got, out.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), noURL) {
			t.Errorf("unreadable URLs must not read as no URL:\n%s", out.String())
		}
	}
	if !strings.Contains(errOut.String(), "could not read URLs") {
		t.Errorf("stderr should warn about the URL read:\n%s", errOut.String())
	}
}

func TestURLColumn(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		urlsKnown bool
		want      string
	}{
		{"published", "https://api.example.com", true, "https://api.example.com"},
		{"unpublished", "", true, noURL},
		{"unknown", "", false, unknownURL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := urlColumn(tc.url, tc.urlsKnown); got != tc.want {
				t.Errorf("urlColumn(%q, %v) = %q, want %q", tc.url, tc.urlsKnown, got, tc.want)
			}
		})
	}
}

// manyLocations returns a workload placed in n locations, so the LOCATIONS column has
// to decide what to do with a list that does not fit.
func manyLocations(name, uid string, locations ...string) *computev1alpha.Workload {
	return workload(name, uid, "ghcr.io/acme/"+name+":1.0", locations...)
}

// TestListWorkloadsLocationsColumn pins the spec's list view: the locations a
// workload runs in are a table column, not a JSON-only field.
func TestListWorkloadsLocationsColumn(t *testing.T) {
	for _, output := range []util.OutputFormat{util.OutputTable, util.OutputWide} {
		t.Run(string(output), func(t *testing.T) {
			var out, errOut bytes.Buffer
			c := newFakeClient(t, projectObjects()...)

			if err := listWorkloads(context.Background(), &out, &errOut, c, testProject, listOptions{output: output}); err != nil {
				t.Fatalf("listWorkloads: %v", err)
			}

			got := out.String()
			if !strings.Contains(got, "LOCATIONS") {
				t.Errorf("table has no LOCATIONS header:\n%s", got)
			}
			if !strings.Contains(got, locEast+", "+locWest) {
				t.Errorf("api row does not name its locations:\n%s", got)
			}
			// The mock's column order: NAME ... LOCATIONS ... READY ... IMAGE ... URL.
			nameAt := strings.Index(got, "NAME")
			locationsAt := strings.Index(got, "LOCATIONS")
			readyAt := strings.Index(got, "READY")
			urlAt := strings.Index(got, "URL")
			if nameAt >= locationsAt || locationsAt >= readyAt || readyAt >= urlAt {
				t.Errorf("columns out of spec order (name=%d locations=%d ready=%d url=%d):\n%s",
					nameAt, locationsAt, readyAt, urlAt, got)
			}
		})
	}
}

// TestLocationsColumnTruncates: a workload in many locations must not blow out the
// column, and the cell has to say that it is not the whole list.
func TestLocationsColumnTruncates(t *testing.T) {
	tests := []struct {
		name      string
		locations []string
		want      string
	}{
		{"none", nil, "(none)"},
		{"one", []string{locEast}, locEast},
		{"at the limit", []string{locEast, locWest, locFra}, "us-east-1, eu-west-1, ap-south-1"},
		{"over the limit", []string{locEast, locWest, locFra, locLhr}, "us-east-1, eu-west-1, ap-south-1 (+1)"},
		{"well over", []string{locEast, locWest, locFra, locLhr, "af-south-1", "me-south-1"}, "us-east-1, eu-west-1, ap-south-1 (+3)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := locationsColumn(tc.locations); got != tc.want {
				t.Errorf("locationsColumn(%v) = %q, want %q", tc.locations, got, tc.want)
			}
		})
	}
}

// TestListWorkloadsLocationsJSONKeepsFullList: truncation is a table concern.
// `-o json` still carries every location.
func TestListWorkloadsLocationsJSONKeepsFullList(t *testing.T) {
	all := []string{locEast, locWest, locFra, locLhr, "af-south-1", "me-south-1"}
	objs := []client.Object{manyLocations(wlAPI, "uid-api", all...)}

	var tableOut, jsonOut, errOut bytes.Buffer
	c := newFakeClient(t, objs...)
	if err := listWorkloads(context.Background(), &tableOut, &errOut, c, testProject, listOptions{output: util.OutputTable}); err != nil {
		t.Fatalf("listWorkloads (table): %v", err)
	}
	if !strings.Contains(tableOut.String(), "us-east-1, eu-west-1, ap-south-1 (+3)") {
		t.Errorf("table column should name the first locations and count the rest:\n%s", tableOut.String())
	}
	if strings.Contains(tableOut.String(), "me-south-1") {
		t.Errorf("table column was not truncated:\n%s", tableOut.String())
	}

	c = newFakeClient(t, objs...)
	if err := listWorkloads(context.Background(), &jsonOut, &errOut, c, testProject, listOptions{output: util.OutputJSON}); err != nil {
		t.Fatalf("listWorkloads (json): %v", err)
	}
	var views []struct {
		Cities []string `json:"locations"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &views); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, jsonOut.String())
	}
	if len(views) != 1 {
		t.Fatalf("got %d views, want 1:\n%s", len(views), jsonOut.String())
	}
	if strings.Join(views[0].Cities, ",") != strings.Join(all, ",") {
		t.Errorf("json locations = %v, want %v", views[0].Cities, all)
	}
}

// TestListWorkloadsJSONNoURLVersusUnknown: the table draws "—" for a workload
// with no URL and "?" for URLs that could not be read. The structured output
// has to draw the same distinction — a consumer cannot be told both as `""`.
func TestListWorkloadsJSONNoURLVersusUnknown(t *testing.T) {
	t.Run("no url omits the field entirely", func(t *testing.T) {
		var out, errOut bytes.Buffer
		c := newFakeClient(t, projectObjects()...)
		if err := listWorkloads(context.Background(), &out, &errOut, c, testProject, listOptions{output: util.OutputJSON}); err != nil {
			t.Fatalf("listWorkloads: %v", err)
		}

		byName := viewsByName(t, out.Bytes())
		if _, ok := byName[workerName]["url"]; ok {
			t.Errorf("unpublished workload should not carry a url field:\n%s", out.String())
		}
		if _, ok := byName[workerName]["urlError"]; ok {
			t.Errorf("unpublished workload is not an error:\n%s", out.String())
		}
		if got := byName[wlAPI]["url"]; got != "https://"+testCustom {
			t.Errorf("api url = %v, want https://%s", got, testCustom)
		}
	})

	t.Run("unreadable urls carry an explicit signal", func(t *testing.T) {
		c := fake.NewClientBuilder().
			WithScheme(testScheme(t)).
			WithObjects(projectObjects()...).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*networkingv1alpha.HTTPProxyList); ok {
						return errors.New("forbidden")
					}
					return cl.List(ctx, list, opts...)
				},
			}).
			Build()

		var out, errOut bytes.Buffer
		if err := listWorkloads(context.Background(), &out, &errOut, c, testProject, listOptions{output: util.OutputJSON}); err != nil {
			t.Fatalf("listWorkloads: %v", err)
		}

		byName := viewsByName(t, out.Bytes())
		for _, name := range []string{wlAPI, workerName} {
			if _, ok := byName[name]["url"]; ok {
				t.Errorf("%s: url must not be reported when it could not be read:\n%s", name, out.String())
			}
			msg, ok := byName[name]["urlError"].(string)
			if !ok || msg == "" {
				t.Errorf("%s: expected a urlError signal:\n%s", name, out.String())
			}
			if !strings.Contains(msg, "forbidden") {
				t.Errorf("%s: urlError = %q, want the server's error in it", name, msg)
			}
		}
	})
}

// TestListWorkloadsYAMLNoURLVersusUnknown: the YAML form draws the same
// distinction as the JSON one.
func TestListWorkloadsYAMLNoURLVersusUnknown(t *testing.T) {
	var out, errOut bytes.Buffer
	c := newFakeClient(t, projectObjects()...)
	if err := listWorkloads(context.Background(), &out, &errOut, c, testProject, listOptions{output: util.OutputYAML}); err != nil {
		t.Fatalf("listWorkloads: %v", err)
	}
	if strings.Contains(out.String(), `url: ""`) {
		t.Errorf("an unpublished workload must not read as an empty url:\n%s", out.String())
	}
}

func viewsByName(t *testing.T, raw []byte) map[string]map[string]any {
	t.Helper()
	var views []map[string]any
	if err := json.Unmarshal(raw, &views); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, string(raw))
	}
	byName := map[string]map[string]any{}
	for _, v := range views {
		name, _ := v["name"].(string)
		byName[name] = v
	}
	return byName
}
