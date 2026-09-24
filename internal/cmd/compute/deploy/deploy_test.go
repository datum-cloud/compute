// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/url"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	testWorkload  = "api"
	testCanonical = "a1b2c3d4.datumproxy.net"
	testImage     = "ghcr.io/acme/api:1"

	imageFlag    = "--image=" + testImage
	httpPortFlag = "--http-port=8080"
	noHTTPFlag   = "--no-http"

	// testInstanceType is the instance type the control plane records on a
	// stored workload, matching the package default.
	testInstanceType = "datumcloud/d1-standard-2"

	// testManifestFile is the -f argument, standing in for any manifest.
	testManifestFile = "workload.yaml"

	wantPortRange = "between 1 and 65535"
	wantManifest  = "manifest"
)

// TestValidateFlags is the migration matrix: which flag combinations the
// command refuses, and — the point of the whole break — that --port is one of
// them rather than a silent alias for --http-port.
func TestValidateFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name: "no http flags is the old behaviour",
			args: []string{testWorkload, imageFlag},
		},
		{
			name: "http-port publishes",
			args: []string{testWorkload, imageFlag, httpPortFlag},
		},
		{
			name: "no-http alone",
			args: []string{testWorkload, imageFlag, noHTTPFlag},
		},
		{
			name:    "port is removed, not aliased",
			args:    []string{testWorkload, imageFlag, "--port=8080"},
			wantErr: "--port has been replaced by --http-port",
		},
		{
			name:    "port zero still errors",
			args:    []string{testWorkload, imageFlag, "--port=0"},
			wantErr: "--port has been replaced by --http-port",
		},
		{
			name:    "http-port and no-http conflict",
			args:    []string{testWorkload, imageFlag, httpPortFlag, noHTTPFlag},
			wantErr: "cannot be combined",
		},
		{
			name:    "http-port with a manifest",
			args:    []string{"-f", testManifestFile, httpPortFlag},
			wantErr: "--http-port cannot be combined with -f",
		},
		{
			name:    "no-http with a manifest",
			args:    []string{"-f", testManifestFile, noHTTPFlag},
			wantErr: "--no-http cannot be combined with -f",
		},
		{
			name:    "http-port below range",
			args:    []string{testWorkload, imageFlag, "--http-port=0"},
			wantErr: wantPortRange,
		},
		{
			name:    "http-port above range",
			args:    []string{testWorkload, imageFlag, "--http-port=70000"},
			wantErr: wantPortRange,
		},
		{
			name:    "http-port negative",
			args:    []string{testWorkload, imageFlag, "--http-port=-1"},
			wantErr: wantPortRange,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, opts := command()
			if err := cmd.Flags().Parse(tc.args); err != nil {
				t.Fatalf("parsing flags: %v", err)
			}

			err := validateFlags(cmd, opts)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want no error, got %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestPortErrorReachesTheUser runs the command the way a user does. Validation
// that only exists in a helper is validation an upgrade path can skip.
func TestPortErrorReachesTheUser(t *testing.T) {
	cmd := Command()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{testWorkload, imageFlag, "--port=8080"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("deploying with --port must fail, not publish the workload")
	}
	if !errors.Is(err, errPortRenamed) {
		t.Fatalf("want the migration error, got %v", err)
	}
	for _, want := range []string{"--http-port 8080", noHTTPFlag} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("migration error does not mention %q: %v", want, err.Error())
		}
	}
}

// TestPortFlagStaysRegistered guards the difference between "errors" and
// "unknown flag": the migration message only lands if the flag still parses.
func TestPortFlagStaysRegistered(t *testing.T) {
	cmd := Command()

	port := cmd.Flags().Lookup("port")
	if port == nil {
		t.Fatal("--port must stay registered for one release so it can error")
	}
	if !port.Hidden {
		t.Error("--port must not be advertised in help")
	}
	if cmd.Flags().Lookup("http-port") == nil {
		t.Error("--http-port must be registered")
	}
	if cmd.Flags().Lookup("no-http") == nil {
		t.Error("--no-http must be registered")
	}
	if !strings.Contains(cmd.Example, httpPortFlag) {
		t.Error("the example block must show --http-port")
	}
	if strings.Contains(cmd.Example, "--port=8080") {
		t.Error("the example block must not show --port")
	}
}

// TestDeclaredHTTPPort covers the carry-forward rule: a deploy that does not
// mention a port must not silently take a live URL down.
func TestDeclaredHTTPPort(t *testing.T) {
	tcp := corev1.ProtocolTCP
	tests := []struct {
		name     string
		workload computev1alpha.Workload
		want     int32
	}{
		{
			name:     "no runtime",
			workload: computev1alpha.Workload{},
		},
		{
			name:     "no ports",
			workload: workloadWithPorts(),
		},
		{
			name:     "the http port",
			workload: workloadWithPorts(computev1alpha.NamedPort{Name: httpPortName, Port: 8080, Protocol: &tcp}),
			want:     8080,
		},
		{
			name: "http wins over an earlier port",
			workload: workloadWithPorts(
				computev1alpha.NamedPort{Name: "metrics", Port: 9090},
				computev1alpha.NamedPort{Name: "http", Port: 8080},
			),
			want: 8080,
		},
		{
			name:     "falls back to the first port",
			workload: workloadWithPorts(computev1alpha.NamedPort{Name: "web", Port: 3000}),
			want:     3000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := declaredHTTPPort(&tc.workload); got != tc.want {
				t.Fatalf("declaredHTTPPort = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestPlanLine pins the plan summary's alignment: the HTTP service line has to
// line up under the placement line the developer reads it with.
func TestPlanLine(t *testing.T) {
	placement := planLine(`Placement "default"`, "cities=[DFW, IAD], min=2")
	http := planLine("HTTP service", "port 8080 → Datum-managed URL")

	if want := `  Placement "default": cities=[DFW, IAD], min=2`; placement != want {
		t.Errorf("placement line = %q, want %q", placement, want)
	}
	if want := "  HTTP service:        port 8080 → Datum-managed URL"; http != want {
		t.Errorf("http line = %q, want %q", http, want)
	}
	if strings.Index(placement, "cities") != strings.Index(http, "port 8080") {
		t.Errorf("plan values do not start in the same column:\n%s\n%s", placement, http)
	}
}

// TestPublishWithoutHTTPPortSaysSo covers the dead end this feature exists to
// close: a workload with no HTTP port must never leave the developer guessing
// why there is nothing to open.
func TestPublishWithoutHTTPPortSaysSo(t *testing.T) {
	var out bytes.Buffer

	// A nil client is deliberate: with no port there is nothing to publish, so
	// nothing may be read or written.
	if err := publish(context.Background(), &out, nil, workload(), 0, &options{}, nil); err != nil {
		t.Fatalf("publish without a port must not fail: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"No HTTP port declared — this workload is not reachable from the internet.",
		"To publish it:  datumctl compute deploy api --http-port 8080",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Publishing") {
		t.Errorf("nothing was published, so nothing should say so:\n%s", got)
	}
}

// TestPublishPrintsTheURLLast covers the product promise: the URL is the
// deliverable, on its own line, at the end.
func TestPublishPrintsTheURLLast(t *testing.T) {
	w := workload()
	c := newFakeClient(t, liveProxy(w), url.BuildNetworkService(w, "http", 8080))

	var out bytes.Buffer
	if err := publish(context.Background(), &out, c, w, 8080, &options{}, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Publishing...") {
		t.Errorf("output missing the publishing heading:\n%s", got)
	}

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	last := lines[len(lines)-1]
	if want := "  https://" + testCanonical; last != want {
		t.Errorf("last line = %q, want %q\nfull output:\n%s", last, want, got)
	}
}

// TestPublishFailureStillReportsTheRollout is the rule that a healthy workload
// never reads as a failed deploy: the URL is declared before the rollout, so a
// failure to declare it is carried past the rollout table and reported as what
// it is — a failure to publish something that did deploy.
func TestPublishFailureStillReportsTheRollout(t *testing.T) {
	boom := errors.New("connection refused")
	c := interceptor.NewClient(newFakeClient(t), interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return boom
		},
	})

	declareErr := declareURL(context.Background(), c, workload(), "http", 8080)
	if declareErr == nil {
		t.Fatal("declaring a URL against a control plane that refuses writes must fail")
	}

	var out bytes.Buffer
	err := publish(context.Background(), &out, c, workload(), 8080,
		&options{image: testImage}, declareErr)

	if err == nil {
		t.Fatal("a failure to publish must be reported as an error")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the underlying failure must not be swallowed: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"The rollout succeeded — the workload is deployed and running.",
		"Only publishing its URL failed.",
		"datumctl compute deploy api --image " + testImage + " --http-port 8080",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

// TestDeclareURLWritesTheObjectsBeforeTheRollout: declaring an HTTP port
// declares a URL, and the objects go in while the workload does — silently,
// because the rollout table is the next thing the user reads.
func TestDeclareURLWritesTheObjectsBeforeTheRollout(t *testing.T) {
	c := newFakeClient(t)

	if err := declareURL(context.Background(), c, workload(), "http", 8080); err != nil {
		t.Fatalf("declareURL: %v", err)
	}

	key := types.NamespacedName{Namespace: util.ResourceNamespace, Name: url.ResourceName(testWorkload)}
	if err := c.Get(context.Background(), key, &networkingv1alpha.NetworkService{}); err != nil {
		t.Errorf("backends were not declared: %v", err)
	}
	if err := c.Get(context.Background(), key, &networkingv1alpha.HTTPProxy{}); err != nil {
		t.Errorf("the URL was not declared: %v", err)
	}
}

// A workload with no HTTP port declares no URL, so nothing may be written for
// it — least of all a proxy publishing a workload the user kept internal.
func TestDeclareURLWritesNothingWithoutAPort(t *testing.T) {
	// A nil client is the assertion: any write at all would panic.
	if err := declareURL(context.Background(), nil, workload(), "", 0); err != nil {
		t.Fatalf("declaring nothing must not fail: %v", err)
	}
}

// TestPublishAfterTheRolloutOnlyWaits is the ordering the spec is explicit
// about: the URL objects are created alongside the workload so that backends
// register as instances come up, and the URL answers within a second or two of
// the last city reaching Done. Publishing after the rollout therefore has
// nothing left to write — a write here is proof the objects were not declared
// earlier, and proof of the stall the spec says never to add.
func TestPublishAfterTheRolloutOnlyWaits(t *testing.T) {
	w := workload()

	// The declared backends are deliberately out of date, so an apply running
	// at this point would have to update them and be caught doing it.
	declared := newFakeClient(t, liveProxy(w), url.BuildNetworkService(w, "http", 9090))
	c := interceptor.NewClient(declared, interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			t.Errorf("publishing wrote %T after the rollout — the URL objects belong alongside the workload", obj)
			return nil
		},
		Update: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.UpdateOption) error {
			t.Errorf("publishing wrote %T after the rollout — the URL objects belong alongside the workload", obj)
			return nil
		},
	})

	var out bytes.Buffer
	if err := publish(context.Background(), &out, c, w, 8080, &options{}, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !strings.Contains(out.String(), "https://"+testCanonical) {
		t.Errorf("publishing must still wait for and print the URL:\n%s", out.String())
	}
}

// Ctrl-C during the rollout detaches. The URL is already declared, so
// publishing has nothing to write and only stops watching — and a detach is
// never an error.
func TestPublishDetachedIsNotAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	if err := publish(ctx, &out, newFakeClient(t), workload(), 8080, &options{}, nil); err != nil {
		t.Fatalf("detaching is never an error: %v", err)
	}
	if strings.Contains(out.String(), "https://") {
		t.Errorf("no URL is known yet, so none may be printed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Detached.") {
		t.Errorf("a detach must say publishing carries on:\n%s", out.String())
	}
}

// TestPublishKeepsCustomHostnames is the cross-command contract between
// `domains add` and `deploy`: publishing rewrites the proxy spec, so a
// redeploy must carry forward the hostnames the user attached. Dropping them
// would unpublish a live custom domain on the next image bump, which only
// `domains remove` is allowed to do.
func TestPublishKeepsCustomHostnames(t *testing.T) {
	w := workload()
	existing := liveProxy(w)
	existing.Spec.Hostnames = []gatewayv1.Hostname{"api.example.com"}
	c := newFakeClient(t, existing, url.BuildNetworkService(w, "http", 8080))

	if err := declareURL(context.Background(), c, w, "http", 8080); err != nil {
		t.Fatalf("declareURL: %v", err)
	}

	var got networkingv1alpha.HTTPProxy
	key := types.NamespacedName{Namespace: util.ResourceNamespace, Name: url.ResourceName(testWorkload)}
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("reading the proxy back: %v", err)
	}

	want := []gatewayv1.Hostname{"api.example.com"}
	if !reflect.DeepEqual(got.Spec.Hostnames, want) {
		t.Errorf("hostnames after redeploy = %v, want %v — a redeploy must not detach a custom domain", got.Spec.Hostnames, want)
	}
}

// --- fixtures ---

func workload() *computev1alpha.Workload {
	return &computev1alpha.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkload,
			Namespace: util.ResourceNamespace,
			UID:       types.UID("11111111-2222-3333-4444-555555555555"),
		},
	}
}

func workloadWithPorts(ports ...computev1alpha.NamedPort) computev1alpha.Workload {
	w := workload()
	w.Spec.Template.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
		Containers: []computev1alpha.SandboxContainer{{Name: containerName, Image: "ghcr.io/acme/api:1", Ports: ports}},
	}
	return *w
}

// liveProxy is the proxy as the platform reports it once the URL answers.
func liveProxy(w *computev1alpha.Workload) *networkingv1alpha.HTTPProxy {
	p := url.BuildHTTPProxy(w, "http", nil)
	p.Status.CanonicalHostname = testCanonical
	p.Status.Conditions = []metav1.Condition{
		{Type: networkingv1alpha.HTTPProxyConditionAccepted, Status: metav1.ConditionTrue, Reason: "Accepted"},
		{Type: networkingv1alpha.HTTPProxyConditionProgrammed, Status: metav1.ConditionTrue, Reason: "Programmed"},
		{Type: networkingv1alpha.HTTPProxyConditionCertificatesReady, Status: metav1.ConditionTrue, Reason: "AllCertificatesReady"},
	}
	return p
}

func newFakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	s := runtime.NewScheme()
	if err := computev1alpha.AddToScheme(s); err != nil {
		t.Fatalf("registering compute scheme: %v", err)
	}
	if err := networkingv1alpha.AddToScheme(s); err != nil {
		t.Fatalf("registering networking scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&networkingv1alpha.HTTPProxy{}, &networkingv1alpha.NetworkService{}).
		WithObjects(objs...).
		Build()
}
