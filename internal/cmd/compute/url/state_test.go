// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// pendingCertProxy is the shape the platform reports in the window between
// `domains add` and the new hostname's certificate being issued: the
// per-hostname status says that one hostname is waiting, and the proxy-level
// certificate roll-up — whose True reason is "AllCertificatesReady" — is
// therefore False for the proxy as a whole.
func pendingCertProxy() *networkingv1alpha.HTTPProxy {
	p := publishedProxy(testWorkloadName, testCanonical)
	p.Spec.Hostnames = append(p.Spec.Hostnames, testCustomHostname)
	p.Status.Conditions = []metav1.Condition{
		cond(networkingv1alpha.HTTPProxyConditionAccepted, metav1.ConditionTrue, "Accepted", ""),
		cond(networkingv1alpha.HTTPProxyConditionProgrammed, metav1.ConditionTrue, "Programmed", ""),
		cond(networkingv1alpha.HTTPProxyConditionCertificatesReady, metav1.ConditionFalse,
			"CertificatePending", "issuing certificate for "+testCustomHostname),
	}
	p.Status.HostnameStatuses = []networkingv1alpha.HostnameStatus{{
		Hostname: testCustomHostname,
		Conditions: []metav1.Condition{
			cond(networkingv1alpha.HostnameConditionVerified, metav1.ConditionTrue, "Verified", ""),
			cond(networkingv1alpha.HostnameConditionCertificateReady, metav1.ConditionFalse, "Pending", "issuing"),
		},
	}}
	return p
}

// TestManagedHostnameSurvivesAPendingCustomHostname is the state every user
// who runs `domains add` passes through, and the one no existing test covers:
// one hostname is waiting on a certificate while the platform-managed hostname
// carries on serving exactly as it did before.
//
// The managed hostname has no per-hostname status of its own here — control
// planes only publish HostnameStatuses for hostnames they are working on — and
// the proxy-level conditions are describing the *other* hostname. Nothing the
// platform says about a custom hostname may be attributed to this one.
func TestManagedHostnameSurvivesAPendingCustomHostname(t *testing.T) {
	c := newFakeClient(t, pendingCertProxy(),
		publishedService(testWorkloadName, 8080, location("DFW", 2, 2, true)))

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}

	managed := info.Hostnames[len(info.Hostnames)-1]
	if !managed.Managed {
		t.Fatalf("last hostname = %+v, want the managed one", managed)
	}
	if !managed.Active || managed.Status != statusActive {
		t.Errorf("managed hostname = %+v, want it still active — it was serving before the custom hostname was attached", managed)
	}
	if managed.Detail != "" {
		t.Errorf("managed hostname detail = %q, want nothing: that message is about %s", managed.Detail, testCustomHostname)
	}

	// The consequence that costs the most: url.Publish waits on Live(), so a
	// redeploy of this workload blocks until an unrelated certificate issues.
	if !info.Live() {
		t.Errorf("URL is not live, so `deploy` will wait on a hostname the user did not ask about; info = %+v", info)
	}
}

// The custom hostname's own state is read from its own conditions and is
// correct even today — this pins the half that works, so a fix for the managed
// hostname cannot regress it.
func TestPendingCustomHostnameReportsItsOwnReason(t *testing.T) {
	c := newFakeClient(t, pendingCertProxy(),
		publishedService(testWorkloadName, 8080, location("DFW", 2, 2, true)))

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}

	custom := info.Hostnames[0]
	if custom.Managed || custom.Active {
		t.Fatalf("first hostname = %+v, want the custom one, not serving yet", custom)
	}
	if custom.Status != "issuing" || custom.Certificate != "issuing" {
		t.Errorf("custom hostname = %+v, want the server's own message", custom)
	}

	// The URL shown is still the managed one: a hostname that is not serving
	// must never be the address handed to the user.
	if info.URL != testCanonicalURL {
		t.Errorf("URL = %q, want the managed hostname while the custom one is pending", info.URL)
	}
}

// TestForWorkloadWhenTheBackendKindIsNotServed covers the half-installed
// control plane: URLs are served, their backends are not. The URL is still
// reported — with no backends — rather than the whole lookup failing.
func TestForWorkloadWhenTheBackendKindIsNotServed(t *testing.T) {
	s := runtime.NewScheme()
	if err := computev1alpha.AddToScheme(s); err != nil {
		t.Fatalf("registering compute scheme: %v", err)
	}
	// Only the proxy kind, deliberately: listing NetworkServices answers with
	// a not-registered error, which reads as "nothing published here".
	s.AddKnownTypes(networkingv1alpha.GroupVersion,
		&networkingv1alpha.HTTPProxy{}, &networkingv1alpha.HTTPProxyList{})
	metav1.AddToGroupVersion(s, networkingv1alpha.GroupVersion)

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(publishedProxy(testWorkloadName, testCanonical)).Build()

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("a missing backend kind is not a lookup failure, got: %v", err)
	}
	if info == nil {
		t.Fatal("info = nil, want the URL to still be reported")
	}
	if info.URL != testCanonicalURL {
		t.Errorf("URL = %q, want %q", info.URL, testCanonicalURL)
	}
	if info.Service != nil || info.Backends != (Backends{}) {
		t.Errorf("Service = %v, Backends = %+v, want nothing known about backends", info.Service, info.Backends)
	}

	all, err := ForAll(context.Background(), c)
	if err != nil {
		t.Fatalf("ForAll returned error: %v", err)
	}
	if len(all) != 1 || all[testWorkloadName] == nil {
		t.Errorf("ForAll = %v, want the one URL", all)
	}
}

// TestPrimaryWithoutAManagedHostnameYet: between creating the proxy and the
// server assigning a hostname, a workload that was published with a custom
// hostname has exactly one hostname and it is not serving. Whatever is shown,
// it must be that hostname and not the empty string — an empty URL is what
// `open` turns into "the platform is still assigning one".
func TestPrimaryWithoutAManagedHostnameYet(t *testing.T) {
	proxy := BuildHTTPProxy(workloadNamed(testWorkloadName), testPortName, []string{testCustomHostname})
	// No CanonicalHostname, and nothing programmed yet.
	proxy.Status.Conditions = []metav1.Condition{
		cond(networkingv1alpha.HTTPProxyConditionAccepted, metav1.ConditionTrue, "Accepted", ""),
		cond(networkingv1alpha.HTTPProxyConditionProgrammed, metav1.ConditionFalse, "Pending", "waiting for the edge"),
	}

	c := newFakeClient(t, proxy)

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}
	if info.CanonicalHostname != "" {
		t.Fatalf("CanonicalHostname = %q, want none assigned yet", info.CanonicalHostname)
	}
	if len(info.Hostnames) != 1 || info.Hostnames[0].Managed {
		t.Fatalf("Hostnames = %+v, want the custom one alone", info.Hostnames)
	}
	if info.URL != testCustomURL {
		t.Errorf("URL = %q, want the only hostname there is", info.URL)
	}
	// It is not live, so no command may present it as ready.
	if info.Live() {
		t.Error("a URL whose edge is not programmed must not report as live")
	}
}

// A URL nothing is known about at all — no hostname of either kind — must
// report an empty URL rather than "https://".
func TestPrimaryWithNoHostnamesAtAll(t *testing.T) {
	c := newFakeClient(t, BuildHTTPProxy(workloadNamed(testWorkloadName), testPortName, nil))

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}
	if info.URL != "" {
		t.Errorf("URL = %q, want empty — there is no hostname to show", info.URL)
	}
	if info.Live() {
		t.Error("a URL with no hostname is not live")
	}
}

// TestAFreshlyAttachedHostnameIsPending is the first seconds of `domains add`:
// the hostname is on the spec and the platform has not looked at it yet, so it
// has no entry in the per-hostname statuses.
//
// The proxy is already serving and every proxy-level condition is True, so a
// hostname that borrowed them would read as active and verified the instant it
// was attached — `domains add` would print a checkmark, skip the DNS records
// the user has to create, and exit.
func TestAFreshlyAttachedHostnameIsPending(t *testing.T) {
	proxy := publishedProxy(testWorkloadName, testCanonical)
	proxy.Spec.Hostnames = append(proxy.Spec.Hostnames, testCustomHostname)
	// Deliberately no HostnameStatuses: nothing has been reported about it.

	c := newFakeClient(t, proxy,
		publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)))

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}

	custom := info.Hostnames[0]
	if custom.Managed {
		t.Fatalf("first hostname = %+v, want the custom one", custom)
	}
	if custom.Active || custom.Status != statusPending {
		t.Errorf("custom hostname = %+v, want it pending: the platform has said nothing about it", custom)
	}
	if custom.Certificate != "" {
		t.Errorf("certificate = %q, want nothing known — no certificate has been reported for this hostname", custom.Certificate)
	}
	if len(custom.Conditions) != 0 {
		t.Errorf("conditions = %+v, want none: borrowing another hostname's checks is what puts a checkmark on an unverified domain", custom.Conditions)
	}

	// The address handed to the user stays the one that answers.
	if info.URL != testCanonicalURL {
		t.Errorf("URL = %q, want the managed hostname", info.URL)
	}

	// And the managed hostname still reads from the proxy-level conditions,
	// which is all a control plane that reports nothing per-hostname publishes.
	managed := info.Hostnames[1]
	if !managed.Active || managed.Certificate != certificateValid {
		t.Errorf("managed hostname = %+v, want it active with a valid certificate", managed)
	}
}

// TestABlockingConditionWithNoMessageNeverShowsItsReason: a condition reason is
// camelCase internal state, and the product promises a developer never sees
// one. When the server has no message to show, the CLI says the plain thing
// rather than leaking the reason into a table cell.
func TestABlockingConditionWithNoMessageNeverShowsItsReason(t *testing.T) {
	const (
		verifyReason = "UnverifiedHostnamesPresent"
		certReason   = "CertificateRequestPending"
	)

	proxy := publishedProxy(testWorkloadName, testCanonical)
	proxy.Spec.Hostnames = append(proxy.Spec.Hostnames, testCustomHostname)
	proxy.Status.HostnameStatuses = []networkingv1alpha.HostnameStatus{{
		Hostname: testCustomHostname,
		Conditions: []metav1.Condition{
			cond(networkingv1alpha.HostnameConditionVerified, metav1.ConditionFalse, verifyReason, ""),
			cond(networkingv1alpha.HostnameConditionCertificateReady, metav1.ConditionFalse, certReason, ""),
		},
	}}

	info := newInfo(testWorkloadName, proxy, nil)
	h := info.Hostnames[0]

	if h.Status != statusPending {
		t.Errorf("status = %q, want %q — the server gave no message to show", h.Status, statusPending)
	}
	if h.Certificate != statusPending {
		t.Errorf("certificate = %q, want %q", h.Certificate, statusPending)
	}
	for _, field := range []string{h.Status, h.Certificate, h.Detail} {
		for _, reason := range []string{verifyReason, certReason} {
			if strings.Contains(field, reason) {
				t.Errorf("%q reached the user: condition reasons are internal state", field)
			}
		}
	}
}

// The same rule with a message to show: the message is what a user reads, and
// the reason still does not appear anywhere.
func TestABlockingConditionShowsTheServersMessage(t *testing.T) {
	proxy := publishedProxy(testWorkloadName, testCanonical)
	proxy.Spec.Hostnames = append(proxy.Spec.Hostnames, testCustomHostname)
	proxy.Status.HostnameStatuses = []networkingv1alpha.HostnameStatus{{
		Hostname: testCustomHostname,
		Conditions: []metav1.Condition{
			cond(networkingv1alpha.HostnameConditionVerified, metav1.ConditionFalse,
				"DomainNotVerified", "no TXT record found at _datum-challenge.api.example.com"),
			cond(networkingv1alpha.HostnameConditionCertificateReady, metav1.ConditionFalse,
				"CertificatePending", "waiting for the domain to verify"),
		},
	}}

	h := newInfo(testWorkloadName, proxy, nil).Hostnames[0]

	if h.Status != "no TXT record found at _datum-challenge.api.example.com" {
		t.Errorf("status = %q, want the server's message", h.Status)
	}
	if h.Certificate != "waiting for the domain to verify" {
		t.Errorf("certificate = %q, want the server's message", h.Certificate)
	}
	if strings.Contains(h.Status+h.Certificate, "DomainNotVerified") ||
		strings.Contains(h.Status+h.Certificate, "CertificatePending") {
		t.Errorf("a raw reason reached the user: status=%q certificate=%q", h.Status, h.Certificate)
	}
}
