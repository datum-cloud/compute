// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/url"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	// handMadePort is the port name used by a workload created outside
	// deploy, and by a load balancer set up outside datumctl.
	handMadePort = "web"

	// sharedProxy names a load balancer set up outside datumctl.
	sharedProxy = "shared"
)

// sharedLoadBalancer is a load balancer set up outside datumctl — in the
// portal, say — routing to the workload: the setup in #485.
func sharedLoadBalancer(name string) []client.Object {
	svc := &networkingv1alpha.NetworkService{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-backends", Namespace: util.ResourceNamespace},
		Spec: networkingv1alpha.NetworkServiceSpec{
			NetworkInterfaces: networkingv1alpha.NetworkServiceInterfaceSelector{
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{computev1alpha.WorkloadNameLabel: testWorkload},
				},
			},
			Ports: []networkingv1alpha.NetworkServicePort{{Name: handMadePort, Port: 8080, Protocol: networkingv1alpha.NetworkServiceProtocolTCP}},
		},
	}
	proxy := &networkingv1alpha.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: util.ResourceNamespace},
		Spec: networkingv1alpha.HTTPProxySpec{
			Rules: []networkingv1alpha.HTTPProxyRule{{
				Backends: []networkingv1alpha.HTTPProxyRuleBackend{{
					NetworkService: &networkingv1alpha.NetworkServiceBackendRef{Name: svc.Name, Port: handMadePort},
				}},
			}},
		},
	}
	return []client.Object{svc, proxy}
}

// TestResolveHTTP pins the rule #485 is about: a deploy that does not mention
// HTTP never publishes a URL that was not already there. A port in the spec
// is not a request for a URL — a workload served through a load balancer
// someone else set up declares one too.
func TestResolveHTTP(t *testing.T) {
	declaring := func(name string) *computev1alpha.Workload {
		w := workloadWithPorts(computev1alpha.NamedPort{Name: name, Port: 8080})
		return &w
	}

	tests := []struct {
		name     string
		objs     []client.Object
		workload *computev1alpha.Workload
		creating bool
		opts     *options
		want     httpPlan
	}{
		{
			name:     "an image bump keeps a URL datumctl published up to date",
			objs:     []client.Object{liveProxy(workload())},
			workload: declaring(httpPortName),
			opts:     &options{},
			want:     httpPlan{port: 8080, publish: true},
		},
		{
			name:     "an image bump keeps the port but publishes nothing new",
			workload: declaring(httpPortName),
			opts:     &options{},
			want:     httpPlan{port: 8080},
		},
		{
			name:     "a port under any name is still not a request for a URL",
			workload: declaring(handMadePort),
			opts:     &options{},
			want:     httpPlan{port: 8080},
		},
		{
			name:     "an image bump names the load balancer already serving the workload",
			objs:     sharedLoadBalancer(sharedProxy),
			workload: declaring(handMadePort),
			opts:     &options{},
			want:     httpPlan{port: 8080, servedBy: []string{sharedProxy}},
		},
		{
			name:     "--http-port publishes, and says what else serves the workload",
			objs:     sharedLoadBalancer(sharedProxy),
			workload: declaring(handMadePort),
			opts:     &options{httpPort: 8080},
			want:     httpPlan{port: 8080, publish: true, servedBy: []string{sharedProxy}},
		},
		{
			name:     "--http-port on a new workload publishes",
			workload: workload(),
			creating: true,
			opts:     &options{httpPort: 8080},
			want:     httpPlan{port: 8080, publish: true},
		},
		{
			name:     "a new workload without --http-port is not published",
			workload: workload(),
			creating: true,
			opts:     &options{},
			want:     httpPlan{},
		},
		{
			name:     "--no-http declares nothing",
			objs:     []client.Object{liveProxy(workload())},
			workload: declaring(httpPortName),
			opts:     &options{noHTTP: true},
			want:     httpPlan{},
		},
		{
			name:     "a workload with no port has nothing to publish",
			workload: workload(),
			opts:     &options{},
			want:     httpPlan{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveHTTP(context.Background(), newFakeClient(t, tc.objs...), tc.workload, tc.creating, tc.opts, false)
			if err != nil {
				t.Fatalf("resolveHTTP: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("resolveHTTP = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Without knowing whether the workload has a URL there is no safe guess, so
// a deploy stops before anything changes rather than publishing a URL, or
// reporting a live one as missing. --http-port is no exception: whether the
// URL is served over HTTP/2 turns on the same read.
func TestResolveHTTPFailsClosedWhenTheURLCannotBeRead(t *testing.T) {
	boom := errors.New("httpproxies.networking.datumapis.com is forbidden")
	c := interceptor.NewClient(newFakeClient(t), interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return boom
		},
	})
	w := workloadWithPorts(computev1alpha.NamedPort{Name: httpPortName, Port: 8080})

	for _, opts := range []*options{{}, {httpPort: 8080}} {
		if _, err := resolveHTTP(context.Background(), c, &w, false, opts, false); !errors.Is(err, boom) {
			t.Errorf("opts %+v: error = %v, want the server's failure", *opts, err)
		}
	}
}

// The plan is where a user consents. What it says about HTTP has to match
// what the deploy is about to do.
func TestPlanHTTPServiceForAPortThatIsNotPublished(t *testing.T) {
	tests := []struct {
		name    string
		plan    httpPlan
		want    []string
		without []string
	}{
		{
			name:    "a port nothing serves",
			plan:    httpPlan{port: 8080},
			want:    []string{"HTTP service:        port 8080, no Datum-managed URL"},
			without: []string{"→", "TLS"},
		},
		{
			name:    "a port served through someone else's load balancer",
			plan:    httpPlan{port: 8080, servedBy: []string{sharedProxy}},
			want:    []string{`HTTP service:        port 8080, served by load balancer "` + sharedProxy + `"`},
			without: []string{"→", "TLS"},
		},
		{
			name: "publishing alongside someone else's load balancer says so first",
			plan: httpPlan{port: 8080, publish: true, servedBy: []string{"a", "b"}},
			want: []string{
				"port 8080 → Datum-managed URL",
				`Already served by load balancers "a", "b"; this adds a separate URL alongside it.`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			planHTTPService(context.Background(), &out, newFakeClient(t), testWorkload, tc.plan, &options{}, false)
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("plan missing %q:\n%s", want, out.String())
				}
			}
			for _, not := range tc.without {
				if strings.Contains(out.String(), not) {
					t.Errorf("plan mentions %q, but nothing is being published:\n%s", not, out.String())
				}
			}
		})
	}
}

// TestPublishForAPortThatIsNotPublished: after the rollout, a workload with a
// port nothing serves is the same dead end as one with no port, and a user
// is told how out of it. One served by someone else's load balancer is not a
// dead end, and the plan already named it.
func TestPublishForAPortThatIsNotPublished(t *testing.T) {
	var out bytes.Buffer
	if err := publish(context.Background(), &out, nil, workload(), httpPlan{port: 3000}, &options{}, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	for _, want := range []string{
		"Port 3000 has no Datum-managed URL.",
		"To publish it:  datumctl compute deploy api --http-port 3000",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}

	var served bytes.Buffer
	if err := publish(context.Background(), &served, nil, workload(), httpPlan{port: 3000, servedBy: []string{sharedProxy}}, &options{}, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if served.Len() != 0 {
		t.Errorf("a workload served elsewhere is reachable; nothing more may be said:\n%s", served.String())
	}
}

// handMadeNamesake is a load balancer set up outside datumctl that happens to
// carry the workload's name.
func handMadeNamesake() *networkingv1alpha.HTTPProxy {
	return &networkingv1alpha.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{Name: testWorkload, Namespace: util.ResourceNamespace},
	}
}

// TestResolveHTTPRefusesBeforeAnythingIsApplied: publishing would have to
// take over a load balancer datumctl did not create. That is found while
// planning, so the deploy stops with nothing applied — not after the rollout,
// with a plan that promised a URL.
func TestResolveHTTPRefusesBeforeAnythingIsApplied(t *testing.T) {
	w := workloadWithPorts(computev1alpha.NamedPort{Name: httpPortName, Port: 8080})

	tests := []struct {
		name     string
		w        *computev1alpha.Workload
		creating bool
		opts     *options
		http2Set bool
	}{
		{name: "--http-port on an existing workload", w: &w, opts: &options{httpPort: 8080}},
		{name: "--http2 on an existing workload", w: &w, opts: &options{http2: true}, http2Set: true},
		{name: "--http-port on a new workload", w: workload(), creating: true, opts: &options{httpPort: 8080}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeClient(t, handMadeNamesake())
			_, err := resolveHTTP(context.Background(), c, tc.w, tc.creating, tc.opts, tc.http2Set)
			if !errors.Is(err, url.ErrNotCreatedByDatumctl) {
				t.Errorf("error = %v, want ErrNotCreatedByDatumctl", err)
			}
		})
	}

	// An image bump publishes nothing, so a namesake is no reason to stop it.
	got, err := resolveHTTP(context.Background(), newFakeClient(t, handMadeNamesake()), &w, false, &options{}, false)
	if err != nil {
		t.Fatalf("an image bump must not be refused over a load balancer it leaves alone: %v", err)
	}
	if got.publish {
		t.Errorf("resolveHTTP = %+v; nothing should be published", got)
	}
}

// TestNoHTTPNamesWhatItLeaves: --no-http removes what datumctl published and
// nothing else. A namesake it leaves may still answer, so both the plan and
// the result say it is still there instead of letting "removed" cover it.
func TestNoHTTPNamesWhatItLeaves(t *testing.T) {
	c := newFakeClient(t, handMadeNamesake())
	w := workloadWithPorts(computev1alpha.NamedPort{Name: httpPortName, Port: 8080})

	svc, err := resolveHTTP(context.Background(), c, &w, false, &options{noHTTP: true}, false)
	if err != nil {
		t.Fatalf("resolveHTTP: %v", err)
	}

	var plan bytes.Buffer
	planHTTPService(context.Background(), &plan, c, testWorkload, svc, &options{noHTTP: true}, false)
	if want := `Leaves load balancer "api" in place: datumctl did not create it.`; !strings.Contains(plan.String(), want) {
		t.Errorf("plan missing %q:\n%s", want, plan.String())
	}

	var out bytes.Buffer
	if err := removeHTTPService(context.Background(), &out, c, testWorkload, "", false); err != nil {
		t.Fatalf("removeHTTPService: %v", err)
	}
	if want := `Left load balancer "api" in place: datumctl did not create it, so it may still serve this workload`; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(handMadeNamesake()), &networkingv1alpha.HTTPProxy{}); err != nil {
		t.Errorf("--no-http deleted a load balancer datumctl did not create: %v", err)
	}
}
