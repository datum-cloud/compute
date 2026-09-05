// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/agent"
)

const testToken = "caller-token"

func baseConfig() *rest.Config {
	// Shaped like an in-cluster config: endpoint and CA, plus a server identity
	// that must not survive into a caller's read.
	return &rest.Config{
		Host:            "https://api.datum.example",
		BearerToken:     "server-service-account-token",
		BearerTokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token",
		TLSClientConfig: rest.TLSClientConfig{CAFile: "/var/run/secrets/ca.crt"},
	}
}

// TestClientConfigAddressesProjectControlPlane pins the addressing: a project
// is reached by rewriting the host path, the same rewrite internal/quota,
// internal/referenceddata and the datumctl plugin perform. The namespace within
// that control plane is "default", never the project name.
func TestClientConfigAddressesProjectControlPlane(t *testing.T) {
	cfg, err := clientConfig(baseConfig(), testToken, "my-project")
	if err != nil {
		t.Fatalf("clientConfig: %v", err)
	}

	want := "https://api.datum.example/apis/resourcemanager.miloapis.com/v1alpha1/projects/my-project/control-plane"
	if cfg.Host != want {
		t.Errorf("Host = %q, want %q", cfg.Host, want)
	}
	if resourceNamespace != "default" {
		t.Errorf("resourceNamespace = %q, want \"default\"", resourceNamespace)
	}
}

// TestClientConfigCarriesOnlyTheCallerCredential is the security property the
// whole design rests on: the server must never read as itself.
func TestClientConfigCarriesOnlyTheCallerCredential(t *testing.T) {
	base := baseConfig()
	cfg, err := clientConfig(base, testToken, "my-project")
	if err != nil {
		t.Fatalf("clientConfig: %v", err)
	}

	if cfg.BearerToken != testToken {
		t.Errorf("BearerToken = %q, want the caller's token", cfg.BearerToken)
	}
	if cfg.BearerTokenFile != "" {
		t.Errorf("BearerTokenFile = %q, want it cleared", cfg.BearerTokenFile)
	}
	if cfg.Impersonate.UserName != "" {
		t.Errorf("Impersonate = %q, want none", cfg.Impersonate.UserName)
	}
	if cfg.CAFile != base.CAFile {
		t.Errorf("CAFile = %q, want the base config's %q", cfg.CAFile, base.CAFile)
	}
	// The base config must be left alone; it is shared by every request.
	if base.BearerToken != "server-service-account-token" || base.Host != "https://api.datum.example" {
		t.Error("clientConfig mutated the shared base config")
	}
}

// TestClientConfigRejectsHostileProject: the project comes off a header and is
// interpolated into a URL path, so a value that could reshape that path into
// another API route has to be refused rather than escaped.
func TestClientConfigRejectsHostileProject(t *testing.T) {
	for _, project := range []string{
		"../../../api/v1/nodes",
		"a/b",
		"proj/../other",
		"proj%2f..",
		"UPPER",
		"has space",
		"..",
		"",
	} {
		if _, err := clientConfig(baseConfig(), testToken, project); err == nil {
			t.Errorf("clientConfig(%q) succeeded, want a rejection", project)
		}
	}
}

func TestDepsFromRequestRequiresCredentials(t *testing.T) {
	tests := []struct {
		name    string
		auth    string
		project string
		want    string
	}{
		{name: "no token", project: "my-project", want: "no bearer token"},
		{name: "wrong scheme", auth: "Basic abc", project: "my-project", want: "no bearer token"},
		{name: "no project", auth: "Bearer " + testToken, want: "no project"},
		{name: "invalid project", auth: "Bearer " + testToken, project: "a/b", want: "invalid project"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			if tc.project != "" {
				r.Header.Set(projectHeader, tc.project)
			}

			_, err := depsFromRequest(r, baseConfig())(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
			// A tool error is handed back to the model; it must not carry the
			// caller's credential.
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error leaks the bearer token: %q", err)
			}
		})
	}
}

// TestDepsFromRequestIgnoresToolArguments guards the prompt-injection defense:
// the project is read from the header only. A query parameter or body field the
// model could influence must not reach the addressing.
func TestDepsFromRequestIgnoresToolArguments(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/mcp?project=attacker-project", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set(projectHeader, " my-project ")

	cfg, err := clientConfig(baseConfig(), bearerToken(r), strings.TrimSpace(r.Header.Get(projectHeader)))
	if err != nil {
		t.Fatalf("clientConfig: %v", err)
	}
	if strings.Contains(cfg.Host, "attacker-project") {
		t.Errorf("Host = %q, want the header's project", cfg.Host)
	}
	if !strings.Contains(cfg.Host, "/projects/my-project/control-plane") {
		t.Errorf("Host = %q, want the trimmed header project", cfg.Host)
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		auth string
		want string
	}{
		{auth: "Bearer " + testToken, want: testToken},
		{auth: "bearer " + testToken, want: testToken},
		{auth: "Bearer  " + testToken + " ", want: testToken},
		{auth: "Basic " + testToken, want: ""},
		{auth: testToken, want: ""},
		{auth: "", want: ""},
	}
	for _, tc := range tests {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if tc.auth != "" {
			r.Header.Set("Authorization", tc.auth)
		}
		if got := bearerToken(r); got != tc.want {
			t.Errorf("bearerToken(%q) = %q, want %q", tc.auth, got, tc.want)
		}
	}
}

// TestReadsGoThroughTheProjectControlPlane drives a real client at a stub API
// server and asserts the request line. This is the evidence behind the
// addressing: a workload read lands on the project's control-plane path with
// the caller's token, in the "default" namespace — not on a namespace named
// after the project on the base host.
func TestReadsGoThroughTheProjectControlPlane(t *testing.T) {
	var gotPath, gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"compute.datumapis.com/v1alpha","kind":"WorkloadList","items":[]}`))
	}))
	defer api.Close()

	cfg, err := clientConfig(&rest.Config{Host: api.URL}, testToken, "my-project")
	if err != nil {
		t.Fatalf("clientConfig: %v", err)
	}

	// A static mapper keeps the test off discovery; the path under test is the
	// one clientConfig put on the config, not one the mapper chose.
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{computev1alpha.GroupVersion})
	mapper.AddSpecific(
		computev1alpha.GroupVersion.WithKind("Workload"),
		computev1alpha.GroupVersion.WithResource("workloads"),
		computev1alpha.GroupVersion.WithResource("workload"),
		meta.RESTScopeNamespace,
	)

	c, err := client.New(cfg, client.Options{Scheme: scheme, Mapper: mapper})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	if _, err := agent.NewClientReader(c).ListWorkloads(context.Background(), resourceNamespace); err != nil {
		t.Fatalf("ListWorkloads: %v", err)
	}

	wantPath := "/apis/resourcemanager.miloapis.com/v1alpha1/projects/my-project/control-plane" +
		"/apis/compute.datumapis.com/v1alpha/namespaces/default/workloads"
	if gotPath != wantPath {
		t.Errorf("request path = %q, want %q", gotPath, wantPath)
	}
	if gotAuth != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want the caller's bearer token", gotAuth)
	}
}
