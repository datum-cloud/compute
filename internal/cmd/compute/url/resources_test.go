// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

func testWorkload() *computev1alpha.Workload {
	w := &computev1alpha.Workload{}
	w.Name = testWorkloadName
	w.Namespace = util.ResourceNamespace
	w.UID = types.UID("11111111-2222-3333-4444-555555555555")
	return w
}

func TestPortName(t *testing.T) {
	tests := []struct {
		name    string
		port    computev1alpha.NamedPort
		want    string
		wantErr bool
	}{
		{name: "the common deploy path", port: computev1alpha.NamedPort{Name: "http", Port: 8080}, want: testPortName},
		{name: "uppercase is lowered", port: computev1alpha.NamedPort{Name: "HTTP", Port: 80}, want: testPortName},
		{name: "underscores become dashes", port: computev1alpha.NamedPort{Name: "web_port", Port: 80}, want: "web-port"},
		{name: "dots become dashes", port: computev1alpha.NamedPort{Name: "web.port", Port: 80}, want: "web-port"},
		{name: "leading and trailing junk is trimmed", port: computev1alpha.NamedPort{Name: "_http_", Port: 80}, want: testPortName},
		{name: "digits are kept", port: computev1alpha.NamedPort{Name: "h2c9", Port: 80}, want: "h2c9"},
		{name: "spaces become dashes", port: computev1alpha.NamedPort{Name: "my port", Port: 80}, want: "my-port"},
		{
			name: "over-long names are truncated to a DNS label",
			port: computev1alpha.NamedPort{Name: strings.Repeat("a", 70), Port: 80},
			want: strings.Repeat("a", 63),
		},
		{
			// Truncating must not leave a trailing dash, which the API rejects.
			name: "truncation does not leave a trailing dash",
			port: computev1alpha.NamedPort{Name: strings.Repeat("a", 62) + "-b" + strings.Repeat("c", 10), Port: 80},
			want: strings.Repeat("a", 62),
		},
		{name: "empty name is an error", port: computev1alpha.NamedPort{Name: "", Port: 8080}, wantErr: true},
		{name: "whitespace-only name is an error", port: computev1alpha.NamedPort{Name: "   ", Port: 8080}, wantErr: true},
		{name: "nothing usable is an error", port: computev1alpha.NamedPort{Name: "___", Port: 8080}, wantErr: true},
		{name: "non-ascii only is an error", port: computev1alpha.NamedPort{Name: "日本", Port: 8080}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PortName(tc.port)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("PortName(%q) = %q, want error", tc.port.Name, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("PortName(%q) returned error: %v", tc.port.Name, err)
			}
			if got != tc.want {
				t.Errorf("PortName(%q) = %q, want %q", tc.port.Name, got, tc.want)
			}
			if !dnsLabel(got) {
				t.Errorf("PortName(%q) = %q, which the API would reject", tc.port.Name, got)
			}
		})
	}
}

// dnsLabel mirrors the pattern NetworkServicePort.Name is validated against.
func dnsLabel(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !alnum && r != '-' {
			return false
		}
		if (i == 0 || i == len(s)-1) && !alnum {
			return false
		}
	}
	return true
}

func TestResourceNameIsSharedByBothObjects(t *testing.T) {
	w := testWorkload()
	svc := BuildNetworkService(w, testPortName, 8080)
	proxy := BuildHTTPProxy(w, testPortName, nil)

	if svc.Name != ResourceName(w.Name) || proxy.Name != ResourceName(w.Name) {
		t.Fatalf("names diverge: service %q, proxy %q, ResourceName %q", svc.Name, proxy.Name, ResourceName(w.Name))
	}
	if proxy.Spec.Rules[0].Backends[0].NetworkService.Name != svc.Name {
		t.Errorf("backend points at %q, but the service is named %q",
			proxy.Spec.Rules[0].Backends[0].NetworkService.Name, svc.Name)
	}
}

func TestBuildNetworkService(t *testing.T) {
	w := testWorkload()
	svc := BuildNetworkService(w, testPortName, 8080)

	if svc.Namespace != util.ResourceNamespace {
		t.Errorf("namespace = %q, want %q", svc.Namespace, util.ResourceNamespace)
	}
	assertPublishedLabels(t, svc.Labels, w)
	assertOwnerRef(t, svc.OwnerReferences, w)

	want := map[string]string{computev1alpha.WorkloadNameLabel: w.Name}
	got := svc.Spec.NetworkInterfaces.Selector.MatchLabels
	if len(got) != len(want) {
		t.Fatalf("selector matchLabels = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("selector matchLabels[%s] = %q, want %q", k, got[k], v)
		}
	}
	if len(svc.Spec.NetworkInterfaces.Selector.MatchExpressions) != 0 {
		t.Errorf("selector should not use matchExpressions: %v", svc.Spec.NetworkInterfaces.Selector.MatchExpressions)
	}

	if len(svc.Spec.Ports) != 1 {
		t.Fatalf("ports = %d, want exactly 1", len(svc.Spec.Ports))
	}
	p := svc.Spec.Ports[0]
	if p.Name != testPortName || p.Port != 8080 || p.Protocol != networkingv1alpha.NetworkServiceProtocolTCP {
		t.Errorf("port = %+v, want {http 8080 TCP}", p)
	}

	// The type's own documentation says to leave this unset.
	if (svc.Spec.TrafficDistribution != networkingv1alpha.NetworkServiceTrafficDistribution{}) {
		t.Errorf("trafficDistribution = %+v, want unset", svc.Spec.TrafficDistribution)
	}
}

func TestBuildHTTPProxy(t *testing.T) {
	w := testWorkload()
	proxy := BuildHTTPProxy(w, testPortName, []string{testCustomHostname, "  ", "www.example.com"})

	if proxy.Namespace != util.ResourceNamespace {
		t.Errorf("namespace = %q, want %q", proxy.Namespace, util.ResourceNamespace)
	}
	assertPublishedLabels(t, proxy.Labels, w)
	assertOwnerRef(t, proxy.OwnerReferences, w)

	if len(proxy.Spec.Hostnames) != 2 {
		t.Fatalf("hostnames = %v, want the two non-blank entries", proxy.Spec.Hostnames)
	}
	if string(proxy.Spec.Hostnames[0]) != testCustomHostname || string(proxy.Spec.Hostnames[1]) != "www.example.com" {
		t.Errorf("hostnames = %v, want [api.example.com www.example.com]", proxy.Spec.Hostnames)
	}

	if len(proxy.Spec.Rules) != 1 {
		t.Fatalf("rules = %d, want exactly 1", len(proxy.Spec.Rules))
	}
	rule := proxy.Spec.Rules[0]
	if len(rule.Matches) != 0 {
		t.Errorf("matches = %v, want none so the CRD default (PathPrefix /) applies", rule.Matches)
	}
	if len(rule.Backends) != 1 {
		t.Fatalf("backends = %d, want exactly 1", len(rule.Backends))
	}

	b := rule.Backends[0]
	if b.NetworkService == nil {
		t.Fatal("backend does not reference a network service")
	}
	if b.NetworkService.Name != ResourceName(w.Name) || b.NetworkService.Port != testPortName {
		t.Errorf("backend ref = %+v, want {api http}", *b.NetworkService)
	}
	// The API rejects backend TLS for this backend form, and the other backend
	// forms are mutually exclusive with it.
	if b.TLS != nil {
		t.Error("backend TLS is set; the API rejects it for networkService backends")
	}
	if b.Endpoint != "" || b.Connector != nil || b.Instance != nil {
		t.Errorf("backend sets a mutually exclusive field: %+v", b)
	}
}

func TestBuildHTTPProxyWithoutHostnames(t *testing.T) {
	proxy := BuildHTTPProxy(testWorkload(), testPortName, nil)
	if len(proxy.Spec.Hostnames) != 0 {
		t.Errorf("hostnames = %v, want none — the managed hostname comes from status", proxy.Spec.Hostnames)
	}
}

func assertPublishedLabels(t *testing.T, got map[string]string, w *computev1alpha.Workload) {
	t.Helper()
	if got[computev1alpha.WorkloadNameLabel] != w.Name {
		t.Errorf("label %s = %q, want %q", computev1alpha.WorkloadNameLabel, got[computev1alpha.WorkloadNameLabel], w.Name)
	}
	if got[computev1alpha.WorkloadUIDLabel] != string(w.UID) {
		t.Errorf("label %s = %q, want %q", computev1alpha.WorkloadUIDLabel, got[computev1alpha.WorkloadUIDLabel], w.UID)
	}
}

func assertOwnerRef(t *testing.T, refs []metav1.OwnerReference, w *computev1alpha.Workload) {
	t.Helper()
	if len(refs) != 1 {
		t.Fatalf("owner references = %d, want exactly 1", len(refs))
	}
	ref := refs[0]
	if ref.APIVersion != computev1alpha.GroupVersion.String() {
		t.Errorf("owner apiVersion = %q, want %q", ref.APIVersion, computev1alpha.GroupVersion.String())
	}
	if ref.Kind != "Workload" || ref.Name != w.Name || ref.UID != w.UID {
		t.Errorf("owner ref = %+v, want Workload/%s/%s", ref, w.Name, w.UID)
	}
	if ref.Controller == nil || *ref.Controller {
		t.Errorf("owner controller = %v, want false", ref.Controller)
	}
	if ref.BlockOwnerDeletion == nil || *ref.BlockOwnerDeletion {
		t.Errorf("owner blockOwnerDeletion = %v, want false", ref.BlockOwnerDeletion)
	}
}
