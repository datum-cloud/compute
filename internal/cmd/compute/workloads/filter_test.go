// SPDX-License-Identifier: AGPL-3.0-only

package workloads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// The health values the filter selects on, and the workload names the cases
// name, spelled once each.
const (
	healthAvailable   = "Available"
	healthDegraded    = "Degraded"
	healthUnavailable = "Unavailable"

	wlAPI    = "api"
	wlBroken = "broken"
	wlSlow   = "slow"
)

// unavailable returns a workload the platform reports as not available. A
// "Degraded" one is different again: available, but short of its desired
// replicas. The filter selects on the first word of either.
func unavailable(name, uid, image string, cities ...string) *computev1alpha.Workload {
	w := workload(name, uid, image, cities...)
	w.Status.Conditions = []metav1.Condition{{
		Type:   computev1alpha.WorkloadAvailable,
		Status: metav1.ConditionFalse,
		Reason: "InsufficientCapacity",
	}}
	return w
}

// TestListWorkloadsHealthFilter: the health filter is the one piece of the
// list view the URL work moved wholesale into a new function, and nothing
// exercised it afterwards. It has to still select on the first word of the
// health, case-insensitively, and the rows it keeps still carry their URLs.
func TestListWorkloadsHealthFilter(t *testing.T) {
	objs := []client.Object{
		workload(wlAPI, "uid-api", "ghcr.io/acme/api:1.4.2", "DFW"),
		unavailable(wlBroken, "uid-broken", "ghcr.io/acme/broken:1", "DFW"),
		workload(wlSlow, "uid-slow", "ghcr.io/acme/slow:1", "DFW"),
		deployment("api-dfw", "uid-api", "DFW", 2, 2),
		deployment("broken-dfw", "uid-broken", "DFW", 0, 2),
		deployment("slow-dfw", "uid-slow", "DFW", 1, 3),
		publishedProxy(wlAPI, testCustom),
		publishedService(wlAPI),
	}

	tests := []struct {
		name        string
		health      string
		wantNames   []string
		wantMissing []string
	}{
		{name: "no filter lists them all", wantNames: []string{wlAPI, wlBroken, wlSlow}},
		{name: "available only", health: healthAvailable, wantNames: []string{wlAPI}, wantMissing: []string{wlBroken, wlSlow}},
		{name: "the filter is case-insensitive", health: "available", wantNames: []string{wlAPI}, wantMissing: []string{wlBroken}},
		{name: "unavailable only", health: healthUnavailable, wantNames: []string{wlBroken}, wantMissing: []string{wlAPI, wlSlow}},
		{name: "degraded is available but short of replicas", health: healthDegraded, wantNames: []string{wlSlow}, wantMissing: []string{wlAPI, wlBroken}},
		{name: "matching on the whole health string finds nothing", health: healthAvailable + " — all placements at desired replicas", wantMissing: []string{wlAPI, wlBroken, wlSlow}},
		{name: "a health nothing has", health: "Nonsense", wantMissing: []string{wlAPI, wlBroken, wlSlow}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			c := newFakeClient(t, objs...)

			opts := listOptions{output: util.OutputJSON, health: tc.health}
			if err := listWorkloads(context.Background(), &out, &errOut, c, testProject, opts); err != nil {
				t.Fatalf("listWorkloads: %v", err)
			}

			var views []workloadView
			if err := json.Unmarshal(out.Bytes(), &views); err != nil {
				t.Fatalf("output is not a JSON array: %v\n%s", err, out.String())
			}
			got := map[string]workloadView{}
			for _, v := range views {
				got[v.Name] = v
			}

			for _, name := range tc.wantNames {
				if _, ok := got[name]; !ok {
					t.Errorf("%q missing from the filtered list: %v", name, got)
				}
			}
			for _, name := range tc.wantMissing {
				if _, ok := got[name]; ok {
					t.Errorf("%q should have been filtered out: %v", name, got)
				}
			}
			if v, ok := got[wlAPI]; ok && v.URL != "https://"+testCustom {
				t.Errorf("api url = %q, want the URL to survive filtering", v.URL)
			}
		})
	}
}

// TestListWorkloadsFailsWhenWorkloadsCannotBeListed: an unreadable project is
// an error. Only the URL column degrades to "unknown" — the workloads
// themselves are the command.
func TestListWorkloadsFailsWhenWorkloadsCannotBeListed(t *testing.T) {
	boom := errors.New("forbidden")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(projectObjects()...).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*computev1alpha.WorkloadList); ok {
					return boom
				}
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()

	var out, errOut bytes.Buffer
	err := listWorkloads(context.Background(), &out, &errOut, c, testProject, listOptions{output: util.OutputTable})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the list failure", err)
	}
}

// TestListWorkloadsUnknownURLsInJSON: the table says "?" when the URLs could
// not be read. JSON has no such marker — the field is just empty — so a script
// reading `.url` cannot tell "no URL" from "could not tell". This pins the
// current shape so the gap is visible rather than assumed away.
func TestListWorkloadsUnknownURLsInJSON(t *testing.T) {
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

	var views []workloadView
	if err := json.Unmarshal(out.Bytes(), &views); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out.String())
	}
	for _, v := range views {
		if v.URL != "" {
			t.Errorf("%s url = %q, want empty when the URLs could not be read", v.Name, v.URL)
		}
	}
	// The warning is the only signal a script has, and it goes to stderr.
	if !strings.Contains(errOut.String(), "could not read URLs") {
		t.Errorf("stderr must carry the warning:\n%s", errOut.String())
	}
}
