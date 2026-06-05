package config

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

func decode(t *testing.T, data string) *WorkloadOperator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if err := RegisterDefaults(scheme); err != nil {
		t.Fatalf("RegisterDefaults: %v", err)
	}
	codecs := serializer.NewCodecFactory(scheme)
	var cfg WorkloadOperator
	if err := runtime.DecodeInto(codecs.UniversalDecoder(), []byte(data), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &cfg
}

func TestWebhookServer_Absent(t *testing.T) {
	cfg := decode(t, `
apiVersion: apiserver.config.datumapis.com/v1alpha1
kind: WorkloadOperator
metricsServer:
  bindAddress: "0"
`)
	if cfg.WebhookServer != nil {
		t.Fatalf("WebhookServer = %+v, want nil", cfg.WebhookServer)
	}
}

func TestWebhookServer_Present(t *testing.T) {
	cfg := decode(t, `
apiVersion: apiserver.config.datumapis.com/v1alpha1
kind: WorkloadOperator
metricsServer:
  bindAddress: "0"
webhookServer:
  port: 9443
`)
	if cfg.WebhookServer == nil {
		t.Fatal("WebhookServer = nil, want non-nil")
	}
	if cfg.WebhookServer.Port != 9443 {
		t.Errorf("Port = %d, want 9443", cfg.WebhookServer.Port)
	}
	// Defaulter should have populated CertDir.
	if cfg.WebhookServer.TLS.CertDir == "" {
		t.Error("TLS.CertDir was not defaulted")
	}
}

// TestQuotaRestConfig_NilWhenNoPath verifies that omitting quotaKubeconfigPath
// returns (nil, nil) — the intentional opt-out / enforcement-disabled case.
func TestQuotaRestConfig_NilWhenNoPath(t *testing.T) {
	cfg := &DiscoveryConfig{}
	restCfg, err := cfg.QuotaRestConfig()
	if err != nil {
		t.Fatalf("QuotaRestConfig() error = %v, want nil", err)
	}
	if restCfg != nil {
		t.Errorf("QuotaRestConfig() = non-nil, want nil (no path configured)")
	}
}

// TestQuotaRestConfig_ErrorWhenPathMissing verifies that explicitly setting a
// kubeconfig path that does not exist on disk returns a non-nil error (fail-loud).
// This reverses the old da63916 behavior of silently returning (nil, nil).
func TestQuotaRestConfig_ErrorWhenPathMissing(t *testing.T) {
	cfg := &DiscoveryConfig{
		QuotaKubeconfigPath: "/nonexistent/path/quota.kubeconfig",
	}
	restCfg, err := cfg.QuotaRestConfig()
	if err == nil {
		t.Fatal("QuotaRestConfig() error = nil, want non-nil error when path is configured but file absent")
	}
	if restCfg != nil {
		t.Errorf("QuotaRestConfig() returned non-nil config alongside error")
	}
}

// TestQuotaRestConfig_SuccessWhenFileExists verifies that a configured path
// pointing to an existing (though minimal) kubeconfig file succeeds.
func TestQuotaRestConfig_SuccessWhenFileExists(t *testing.T) {
	// Write a minimal kubeconfig that clientcmd can parse.
	dir := t.TempDir()
	kubeconfigPath := filepath.Join(dir, "quota.kubeconfig")
	minimalKubeconfig := []byte(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://localhost:1234
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user: {}
`)
	if err := os.WriteFile(kubeconfigPath, minimalKubeconfig, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := &DiscoveryConfig{QuotaKubeconfigPath: kubeconfigPath}
	restCfg, err := cfg.QuotaRestConfig()
	if err != nil {
		t.Fatalf("QuotaRestConfig() error = %v, want nil", err)
	}
	if restCfg == nil {
		t.Error("QuotaRestConfig() = nil, want non-nil when file exists")
	}
}
