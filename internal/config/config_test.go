package config

import (
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
