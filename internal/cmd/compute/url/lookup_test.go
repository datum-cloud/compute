// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// Fixtures shared by the tests in this package.
const (
	testWorkloadName   = "api"
	testPortName       = "http"
	testCanonical      = "a1b2c3d4.datumproxy.net"
	testCanonicalURL   = "https://" + testCanonical
	testCustomHostname = "api.example.com"
	testCustomURL      = "https://" + testCustomHostname

	kindProxy   = "HTTPProxy"
	kindService = "NetworkService"
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

func newFakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithStatusSubresource(&networkingv1alpha.HTTPProxy{}, &networkingv1alpha.NetworkService{}).
		WithObjects(objs...).
		Build()
}

func cond(condType string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: condType, Status: status, Reason: reason, Message: message}
}

// publishedProxy returns a proxy in the shape the platform reports once it is
// serving on the managed hostname alone.
func publishedProxy(workload, canonical string) *networkingv1alpha.HTTPProxy {
	p := BuildHTTPProxy(workloadNamed(workload), testPortName, nil)
	p.Status.CanonicalHostname = canonical
	p.Status.Conditions = []metav1.Condition{
		cond(networkingv1alpha.HTTPProxyConditionAccepted, metav1.ConditionTrue, "Accepted", ""),
		cond(networkingv1alpha.HTTPProxyConditionProgrammed, metav1.ConditionTrue, "Programmed", ""),
		cond(networkingv1alpha.HTTPProxyConditionCertificatesReady, metav1.ConditionTrue, "AllCertificatesReady", ""),
	}
	return p
}

func publishedService(workload string, port int32, locations ...networkingv1alpha.NetworkServiceLocationStatus) *networkingv1alpha.NetworkService {
	s := BuildNetworkService(workloadNamed(workload), testPortName, port)
	var members, healthy int32
	for _, l := range locations {
		members += l.Members
		healthy += l.Healthy
	}
	s.Status.Summary = networkingv1alpha.NetworkServiceSummary{
		Locations: int32(len(locations)),
		Members:   members,
		Healthy:   healthy,
	}
	s.Status.Locations = locations
	s.Status.Conditions = []metav1.Condition{
		cond(networkingv1alpha.NetworkServiceMembersResolved, metav1.ConditionTrue, "MembersResolved", ""),
		cond(networkingv1alpha.NetworkServiceReady, metav1.ConditionTrue, "Ready", ""),
	}
	return s
}

func workloadNamed(name string) *computev1alpha.Workload {
	w := testWorkload()
	w.Name = name
	w.UID = types.UID("uid-" + name)
	return w
}

func location(city string, members, healthy int32, serving bool) networkingv1alpha.NetworkServiceLocationStatus {
	return networkingv1alpha.NetworkServiceLocationStatus{Name: city, Members: members, Healthy: healthy, Serving: serving}
}

func TestForWorkloadPublished(t *testing.T) {
	c := newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 2, 2, true), location("IAD", 2, 2, true)),
	)

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}
	if info == nil {
		t.Fatal("ForWorkload returned nil for a published workload")
	}

	if info.URL != testCanonicalURL {
		t.Errorf("URL = %q, want the managed hostname as https", info.URL)
	}
	if info.CanonicalHostname != testCanonical {
		t.Errorf("CanonicalHostname = %q", info.CanonicalHostname)
	}
	if len(info.CustomHostnames) != 0 {
		t.Errorf("CustomHostnames = %v, want none", info.CustomHostnames)
	}
	if !info.EdgeProgrammed || !info.CertificateIssued || !info.Live() {
		t.Errorf("edge=%v cert=%v live=%v, want all true", info.EdgeProgrammed, info.CertificateIssued, info.Live())
	}
	if info.Port != 8080 || info.PortName != testPortName || info.Protocol != "TCP" {
		t.Errorf("backend port = %d/%s (%s), want 8080/http (TCP)", info.Port, info.PortName, info.Protocol)
	}
	want := Backends{Cities: 2, Total: 4, Healthy: 4}
	if info.Backends != want {
		t.Errorf("Backends = %+v, want %+v", info.Backends, want)
	}
	if len(info.Locations) != 2 || info.Locations[0] != (Location{Location: "DFW", Backends: 2, Healthy: 2, Serving: true}) {
		t.Errorf("Locations = %+v", info.Locations)
	}
	if info.Proxy == nil || info.Service == nil {
		t.Error("raw objects should be carried for -o yaml")
	}
	if len(info.ServiceConditions) == 0 || len(info.ProxyConditions) == 0 {
		t.Error("conditions should be carried through for rendering")
	}
}

func TestForWorkloadDegraded(t *testing.T) {
	c := newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 2, 2, true), location("IAD", 2, 0, false)),
	)

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}
	if info.Backends.Healthy != 2 || info.Backends.Total != 4 {
		t.Errorf("Backends = %+v, want 2 of 4 healthy", info.Backends)
	}
	// A degraded backend set does not stop the URL from answering.
	if !info.Live() {
		t.Error("URL should still be live while one city is out of rotation")
	}
}

func TestForWorkloadCustomHostnamePreferredWhenActive(t *testing.T) {
	proxy := publishedProxy(testWorkloadName, testCanonical)
	proxy.Spec.Hostnames = append(proxy.Spec.Hostnames, testCustomHostname)
	proxy.Status.HostnameStatuses = []networkingv1alpha.HostnameStatus{{
		Hostname: testCustomHostname,
		Conditions: []metav1.Condition{
			cond(networkingv1alpha.HostnameConditionVerified, metav1.ConditionTrue, "Verified", ""),
			cond(networkingv1alpha.HostnameConditionDNSRecordProgrammed, metav1.ConditionTrue, "RecordCreated", ""),
			cond(networkingv1alpha.HostnameConditionCertificateReady, metav1.ConditionTrue, "CertificateIssued", ""),
		},
	}}

	c := newFakeClient(t, proxy, publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)))

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}
	if info.URL != testCustomURL {
		t.Errorf("URL = %q, want the custom hostname", info.URL)
	}
	if len(info.Hostnames) != 2 {
		t.Fatalf("Hostnames = %+v, want the custom one and the managed one", info.Hostnames)
	}
	if info.Hostnames[0].Managed || !info.Hostnames[1].Managed {
		t.Errorf("hostname order = %+v, want custom first and managed last", info.Hostnames)
	}
	if info.Hostnames[0].Status != statusActive || info.Hostnames[0].Certificate != certificateValid {
		t.Errorf("custom hostname = %+v, want active with a valid certificate", info.Hostnames[0])
	}
}

func TestForWorkloadPendingCustomHostnameFallsBackToManaged(t *testing.T) {
	proxy := publishedProxy(testWorkloadName, testCanonical)
	proxy.Spec.Hostnames = append(proxy.Spec.Hostnames, testCustomHostname)
	proxy.Status.HostnameStatuses = []networkingv1alpha.HostnameStatus{{
		Hostname: testCustomHostname,
		Conditions: []metav1.Condition{
			cond(networkingv1alpha.HostnameConditionVerified, metav1.ConditionFalse, "DomainNotVerified", "waiting for TXT record"),
		},
	}}

	c := newFakeClient(t, proxy, publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)))

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}
	if info.URL != testCanonicalURL {
		t.Errorf("URL = %q, want the managed hostname while the custom one is pending", info.URL)
	}
	// The server's own words, unedited — its message, never its reason.
	if info.Hostnames[0].Status != "waiting for TXT record" || info.Hostnames[0].Detail != "waiting for TXT record" {
		t.Errorf("custom hostname = %+v, want the server's message verbatim", info.Hostnames[0])
	}
	if info.Hostnames[0].Status == "DomainNotVerified" {
		t.Error("a raw camelCase condition reason must never be shown as a status")
	}
	if info.Hostnames[0].Active {
		t.Error("a hostname with a blocking condition is not active")
	}
}

func TestForWorkloadNotPublished(t *testing.T) {
	c := newFakeClient(t, publishedProxy("other", "zzz.datumproxy.net"))

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("an unpublished workload is not an error, got: %v", err)
	}
	if info != nil {
		t.Errorf("info = %+v, want nil for an unpublished workload", info)
	}
}

func TestForWorkloadWithoutTheCRDsInstalled(t *testing.T) {
	// A control plane that never had the networking kinds answers with a
	// no-match error. That means "no URLs here", not "the command failed".
	s := runtime.NewScheme()
	if err := computev1alpha.AddToScheme(s); err != nil {
		t.Fatalf("registering compute scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(s).Build()

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("a control plane without the kinds is not an error, got: %v", err)
	}
	if info != nil {
		t.Errorf("info = %+v, want nil", info)
	}

	all, err := ForAll(context.Background(), c)
	if err != nil {
		t.Fatalf("ForAll returned error: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("ForAll = %v, want empty", all)
	}
}

func TestForWorkloadPropagatesTransportErrors(t *testing.T) {
	boom := errors.New("connection refused")
	c := interceptor.NewClient(newFakeClient(t), interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return boom
		},
	})

	if _, err := ForWorkload(context.Background(), c, testWorkloadName); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the transport error to propagate", err)
	}
	if _, err := ForAll(context.Background(), c); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the transport error to propagate", err)
	}
}

func TestForAllRendersAWholeProjectInTwoListCalls(t *testing.T) {
	objs := []client.Object{
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 2, 2, true), location("IAD", 2, 2, true)),
		publishedProxy("web", "e5f6a7b8.datumproxy.net"),
		publishedService("web", 3000, location("DFW", 1, 1, true)),
		publishedProxy("docs", "c9d0e1f2.datumproxy.net"),
		publishedService("docs", 80, location("IAD", 1, 0, false)),
	}
	// A workload with no URL at all: it must simply be absent from the map.
	worker := workloadNamed("worker")

	lists := 0
	c := interceptor.NewClient(newFakeClient(t, objs...), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			lists++
			return cl.List(ctx, list, opts...)
		},
	})

	infos, err := ForAll(context.Background(), c)
	if err != nil {
		t.Fatalf("ForAll returned error: %v", err)
	}
	if lists != 2 {
		t.Errorf("List calls = %d, want exactly 2 no matter how many workloads", lists)
	}
	if len(infos) != 3 {
		t.Fatalf("infos = %d entries, want 3", len(infos))
	}
	if infos[worker.Name] != nil {
		t.Errorf("unpublished workload %q should be absent from the map", worker.Name)
	}

	if got := infos[testWorkloadName]; got == nil || got.URL != testCanonicalURL || got.Backends.Healthy != 4 {
		t.Errorf("api = %+v", got)
	}
	if got := infos["web"]; got == nil || got.Port != 3000 || got.Backends.Total != 1 {
		t.Errorf("web = %+v", got)
	}
	if got := infos["docs"]; got == nil || got.Backends.Healthy != 0 || len(got.Locations) != 1 {
		t.Errorf("docs = %+v", got)
	}
	for name, info := range infos {
		if info.WorkloadName != name {
			t.Errorf("info keyed %q carries workload name %q", name, info.WorkloadName)
		}
	}
}

func TestForWorkloadWithoutBackendsYet(t *testing.T) {
	// The proxy can exist for a moment before the service does. That is a URL
	// with no backends, not a lookup failure.
	c := newFakeClient(t, publishedProxy(testWorkloadName, testCanonical))

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}
	if info == nil {
		t.Fatal("info = nil, want a URL with no backends")
	}
	if info.Backends != (Backends{}) || info.Service != nil {
		t.Errorf("Backends = %+v, Service = %v, want empty", info.Backends, info.Service)
	}
	if info.Port != 0 {
		t.Errorf("Port = %d, want 0 when no backends are known", info.Port)
	}
}

func TestNamespaceIsAlwaysTheProjectNamespace(t *testing.T) {
	if util.ResourceNamespace != BuildHTTPProxy(testWorkload(), testPortName, nil).Namespace {
		t.Error("published objects must live in the project namespace")
	}
}

func TestLookupsIgnoreHandWrittenProxies(t *testing.T) {
	// A proxy the user wrote themselves is theirs. Reporting it as a
	// workload's URL would make `destroy` offer to delete it.
	handWritten := BuildHTTPProxy(workloadNamed(testWorkloadName), testPortName, nil)
	handWritten.Name = "hand-written"
	handWritten.Labels = nil
	handWritten.OwnerReferences = nil
	handWritten.Status.CanonicalHostname = testCanonical

	c := newFakeClient(t, handWritten)

	infos, err := ForAll(context.Background(), c)
	if err != nil {
		t.Fatalf("ForAll returned error: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("ForAll = %v, want nothing — that proxy is not a workload URL", infos)
	}

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}
	if info != nil {
		t.Errorf("ForWorkload = %+v, want nil", info)
	}
}

// TestObjectsIsTheEscapeHatchToTheRealState pins the promise the spec makes
// twice over: plain language in normal output, and `-o yaml` showing the real
// objects. Without a way out of this package to them, `domains <workload>
// -o yaml` can only re-print the same summary the table already showed.
func TestObjectsIsTheEscapeHatchToTheRealState(t *testing.T) {
	c := newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)),
	)

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}

	objs := info.Objects()
	if objs == nil {
		t.Fatal("Objects() = nil, want the real objects behind the URL")
	}
	if objs.HTTPProxy != info.Proxy || objs.NetworkService != info.Service {
		t.Error("Objects() must hand back the objects themselves, not a summary of them")
	}

	// What `-o yaml` would render: the real spec and status, not the CLI's view.
	var out bytes.Buffer
	if err := util.PrintYAML(&out, objs); err != nil {
		t.Fatalf("rendering the objects: %v", err)
	}
	got := out.String()
	for _, want := range []string{"httpProxy", "networkService", testCanonical, "canonicalHostname"} {
		if !strings.Contains(got, want) {
			t.Errorf("-o yaml output missing %q:\n%s", want, got)
		}
	}

	// And the default structured view still carries none of the machinery.
	var human bytes.Buffer
	if err := util.PrintJSON(&human, info); err != nil {
		t.Fatalf("rendering the URL: %v", err)
	}
	for _, unwanted := range []string{"httpProxy", "networkService"} {
		if strings.Contains(human.String(), unwanted) {
			t.Errorf("%q leaked into the default view:\n%s", unwanted, human.String())
		}
	}

	// Nothing to show reads as nothing, so a caller needs no second check.
	var missing *Info
	if missing.Objects() != nil {
		t.Error("Objects() on a workload with no URL must be nil")
	}
	if (&Info{}).Objects() != nil {
		t.Error("Objects() with neither object must be nil")
	}
}
