// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"bytes"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// row returns the whitespace-separated fields of the first line containing the
// given first field, so assertions do not depend on column widths.
func row(t *testing.T, output, first string) []string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == first {
			return fields
		}
	}
	t.Fatalf("no line starting with %q in:\n%s", first, output)
	return nil
}

func TestRenderDetailDegraded(t *testing.T) {
	info := newInfo(testWorkloadName,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 2, 2, true), location("IAD", 2, 0, false)),
	)

	var out bytes.Buffer
	RenderDetail(&out, info)
	got := out.String()

	if f := row(t, got, "URL"); f[1] != testCanonicalURL {
		t.Errorf("URL row = %v", f)
	}
	if f := row(t, got, "Backend"); f[1] != "port" || f[2] != "8080/tcp" {
		t.Errorf("Backend row = %v, want port 8080/tcp", f)
	}
	// "Serving", not "Health": this block renders under a workload's own Health
	// line in `workloads describe`, so the labels have to stay distinguishable.
	if !strings.Contains(got, "Serving      Degraded — 2 of 4 backends healthy") {
		t.Errorf("missing the health summary:\n%s", got)
	}

	if f := row(t, got, "LOCATION"); strings.Join(f, " ") != "LOCATION BACKENDS HEALTHY SERVING" {
		t.Errorf("table header = %v", f)
	}
	if f := row(t, got, "DFW"); strings.Join(f[1:], " ") != "2 2 yes" {
		t.Errorf("DFW row = %v, want 2 2 yes", f)
	}
	if f := row(t, got, "IAD:"); len(f) == 0 {
		t.Error("expected a narrative line for the unhealthy location")
	}
	if f := row(t, got, "IAD"); strings.Join(f[1:], " ") != "2 0 no" {
		t.Errorf("IAD row = %v, want 2 0 no", f)
	}

	for _, want := range []string{
		"IAD: no healthy backends — instances are running but not passing health checks.",
		"Traffic is being served from DFW only.",
		"Next steps:",
		"datumctl compute instances --workload=api --location=IAD",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}

	// The user never sees the machinery.
	for _, forbidden := range []string{kindService, kindProxy, "http://"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("output names the machinery %q:\n%s", forbidden, got)
		}
	}
}

func TestRenderDetailHealthySaysNothingToDo(t *testing.T) {
	info := newInfo(testWorkloadName,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 2, 2, true), location("IAD", 2, 2, true)),
	)

	var out bytes.Buffer
	RenderDetail(&out, info)
	got := out.String()

	if !strings.Contains(got, "Healthy — 4 of 4 backends healthy") {
		t.Errorf("missing the health summary:\n%s", got)
	}
	if strings.Contains(got, "Next steps") {
		t.Errorf("a healthy URL needs no next steps:\n%s", got)
	}
	if f := row(t, got, "IAD"); strings.Join(f[1:], " ") != "2 2 yes" {
		t.Errorf("IAD row = %v", f)
	}
}

func TestRenderDetailBothHostnames(t *testing.T) {
	proxy := publishedProxy(testWorkloadName, testCanonical)
	proxy.Spec.Hostnames = append(proxy.Spec.Hostnames, testCustomHostname)
	proxy.Status.HostnameStatuses = []networkingv1alpha.HostnameStatus{{
		Hostname: testCustomHostname,
		Conditions: []metav1.Condition{
			cond(networkingv1alpha.HostnameConditionVerified, metav1.ConditionTrue, "Verified", ""),
			cond(networkingv1alpha.HostnameConditionCertificateReady, metav1.ConditionTrue, "CertificateIssued", ""),
		},
	}}

	var out bytes.Buffer
	RenderDetail(&out, newInfo(testWorkloadName, proxy, publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true))))
	got := out.String()

	lines := strings.Split(got, "\n")
	if !strings.HasPrefix(lines[0], "URL") || !strings.Contains(lines[0], testCustomURL) {
		t.Errorf("first line = %q, want the custom hostname", lines[0])
	}
	if strings.Contains(lines[1], "URL") || !strings.Contains(lines[1], testCanonicalURL) {
		t.Errorf("second line = %q, want the managed hostname under an empty label", lines[1])
	}
}

func TestRenderDetailShowsTheServersOwnWords(t *testing.T) {
	proxy := publishedProxy(testWorkloadName, testCanonical)
	svc := publishedService(testWorkloadName, 8080)
	svc.Status.Conditions = []metav1.Condition{
		cond(networkingv1alpha.NetworkServiceReady, metav1.ConditionFalse,
			"SomeReasonTheCLIHasNeverHeardOf", "selector matched no network interface"),
	}

	var out bytes.Buffer
	RenderDetail(&out, newInfo(testWorkloadName, proxy, svc))
	got := out.String()

	// The server's message verbatim, and never its reason: a reason the CLI has
	// never heard of is still an identifier, and product principle 4 keeps those
	// away from the developer. TestRenderDetailPendingHostnameCarriesItsStatus
	// asserts the same rule for hostnames.
	if !strings.Contains(got, "selector matched no network interface") {
		t.Errorf("the server's message must appear verbatim:\n%s", got)
	}
	if strings.Contains(got, "SomeReasonTheCLIHasNeverHeardOf") {
		t.Errorf("a raw condition reason reached the user:\n%s", got)
	}
	if !strings.Contains(got, "Unavailable — no backends registered") {
		t.Errorf("missing the health summary:\n%s", got)
	}
	if !strings.Contains(got, "datumctl compute instances --workload=api") {
		t.Errorf("missing next steps:\n%s", got)
	}
}

func TestRenderDetailPendingHostnameCarriesItsStatus(t *testing.T) {
	proxy := publishedProxy(testWorkloadName, testCanonical)
	proxy.Spec.Hostnames = append(proxy.Spec.Hostnames, testCustomHostname)
	proxy.Status.HostnameStatuses = []networkingv1alpha.HostnameStatus{{
		Hostname: testCustomHostname,
		Conditions: []metav1.Condition{
			cond(networkingv1alpha.HostnameConditionVerified, metav1.ConditionFalse, "DomainNotVerified", "waiting for TXT record"),
		},
	}}

	var out bytes.Buffer
	RenderDetail(&out, newInfo(testWorkloadName, proxy, publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true))))
	got := out.String()

	// The server's message, not its reason: "DomainNotVerified" is internal
	// state and a developer must never be shown it.
	if !strings.Contains(got, "https://api.example.com  (waiting for TXT record)") {
		t.Errorf("a pending hostname should carry the server's message:\n%s", got)
	}
	if strings.Contains(got, "DomainNotVerified") {
		t.Errorf("a raw condition reason reached the user:\n%s", got)
	}
	if !strings.Contains(got, "api.example.com: waiting for TXT record") {
		t.Errorf("the server's message should be shown verbatim:\n%s", got)
	}
}

func TestRenderDetailNilWritesNothing(t *testing.T) {
	var out bytes.Buffer
	RenderDetail(&out, nil)
	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing — the caller words the no-URL case", out.String())
	}
}
