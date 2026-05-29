package config

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	multiclusterproviders "go.miloapis.com/milo/pkg/multicluster-runtime"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:defaulter-gen=true

type WorkloadOperator struct {
	metav1.TypeMeta

	MetricsServer MetricsServerConfig `json:"metricsServer"`

	// WebhookServer configures the admission webhook server. When unset, the
	// manager runs without an admission webhook server and no serving cert
	// is required.
	WebhookServer *WebhookServerConfig `json:"webhookServer,omitempty"`

	Discovery DiscoveryConfig `json:"discovery"`
}

// +k8s:deepcopy-gen=true

type WebhookServerConfig struct {
	// Host is the address that the server will listen on.
	// Defaults to "" - all addresses.
	Host string `json:"host"`

	// Port is the port number that the server will serve.
	// It will be defaulted to 9443 if unspecified.
	Port int `json:"port"`

	// TLS is the TLS configuration for the webhook server, allowing configuration
	// of what path to find a certificate and key in, and what file names to use.
	TLS TLSConfig `json:"tls"`

	// ClientCAName is the CA certificate name which server used to verify remote(client)'s certificate.
	// Defaults to "", which means server does not verify client's certificate.
	ClientCAName string `json:"clientCAName"`
}

func SetDefaults_WebhookServerConfig(obj *WebhookServerConfig) {
	if obj.TLS.CertDir == "" {
		obj.TLS.CertDir = filepath.Join(os.TempDir(), "k8s-webhook-server", "serving-certs")
	}
}

func (c *WebhookServerConfig) Options(ctx context.Context, secretsClient client.Client) webhook.Options {
	opts := webhook.Options{
		Host:     c.Host,
		Port:     c.Port,
		CertDir:  c.TLS.CertDir,
		CertName: c.TLS.CertName,
		KeyName:  c.TLS.KeyName,
	}

	if secretRef := c.TLS.SecretRef; secretRef != nil {
		opts.TLSOpts = c.TLS.Options(ctx, secretsClient)
	}

	return opts
}

// +k8s:deepcopy-gen=true

type MetricsServerConfig struct {
	// SecureServing enables serving metrics via https.
	// Per default metrics will be served via http.
	SecureServing *bool `json:"secureServing,omitempty"`

	// BindAddress is the bind address for the metrics server.
	// It will be defaulted to "0" if unspecified.
	// Use :8443 for HTTPS or :8080 for HTTP
	//
	// Set this to "0" to disable the metrics server.
	BindAddress string `json:"bindAddress"`

	// TLS is the TLS configuration for the metrics server, allowing configuration
	// of what path to find a certificate and key in, and what file names to use.
	TLS TLSConfig `json:"tls"`
}

func SetDefaults_MetricsServerConfig(obj *MetricsServerConfig) {
	if obj.SecureServing == nil {
		obj.SecureServing = ptr.To(true)
	}

	if obj.BindAddress == "" {
		obj.BindAddress = "0"
	}

	if len(obj.TLS.CertDir) == 0 {
		obj.TLS.CertDir = filepath.Join(os.TempDir(), "k8s-metrics-server", "serving-certs")
	}
}

func (c *MetricsServerConfig) Options(ctx context.Context, secretsClient client.Client) metricsserver.Options {
	opts := metricsserver.Options{
		SecureServing: *c.SecureServing,
		BindAddress:   c.BindAddress,
		CertDir:       c.TLS.CertDir,
		CertName:      c.TLS.CertName,
		KeyName:       c.TLS.KeyName,
	}

	if *c.SecureServing {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/components/controller_rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		opts.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	if secretRef := c.TLS.SecretRef; secretRef != nil {
		opts.TLSOpts = c.TLS.Options(ctx, secretsClient)
	}

	return opts
}

// +k8s:deepcopy-gen=true

type TLSConfig struct {
	// SecretRef is a reference to a secret that contains the server key and
	// certificate. If provided, CertDir will be ignored, and CertName and KeyName
	// will be used as key names in the secret data.
	//
	// Note: This option is not currently recommended for production, as the secret
	// will be read from the API on every request.
	SecretRef *corev1.ObjectReference `json:"secretRef,omitempty"`

	// CertDir is the directory that contains the server key and certificate. Defaults to
	// <temp-dir>/k8s-webhook-server/serving-certs.
	CertDir string `json:"certDir"`

	// CertName is the server certificate name. Defaults to tls.crt.
	//
	// Note: This option is only used when TLSOpts does not set GetCertificate.
	CertName string `json:"certName"`

	// KeyName is the server key name. Defaults to tls.key.
	//
	// Note: This option is only used when TLSOpts does not set GetCertificate.
	KeyName string `json:"keyName"`
}

func (c *TLSConfig) Options(ctx context.Context, secretsClient client.Client) []func(*tls.Config) {
	var tlsOpts []func(*tls.Config)

	if secretRef := c.SecretRef; secretRef != nil {
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			logger := ctrl.Log.WithName("webhook-tls-client")
			c.GetCertificate = func(clientHello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				logger.Info("getting certificate")

				// Look at https://github.com/cert-manager/cert-manager/blob/master/pkg/server/tls/dynamic_source.go

				// TODO(jreese) caching & background refresh

				var secret corev1.Secret
				secretObjectKey := types.NamespacedName{
					Name:      secretRef.Name,
					Namespace: secretRef.Namespace,
				}
				if err := secretsClient.Get(ctx, secretObjectKey, &secret); err != nil {
					return nil, fmt.Errorf("failed to get secret: %w", err)
				}

				cert, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
				if err != nil {
					return nil, fmt.Errorf("failed to parse certificate: %w", err)
				}

				return &cert, nil
			}
		})
	}

	return tlsOpts
}

func SetDefaults_TLSConfig(obj *TLSConfig) {
	if len(obj.CertName) == 0 {
		obj.CertName = "tls.crt"
	}

	if len(obj.KeyName) == 0 {
		obj.KeyName = "tls.key"
	}
}

// +k8s:deepcopy-gen=true

type DiscoveryConfig struct {
	// Mode is the mode that the operator should use to discover clusters.
	//
	// Defaults to "single"
	Mode multiclusterproviders.Provider `json:"mode"`

	// InternalServiceDiscovery will result in the operator to connect to internal
	// service addresses for projects.
	InternalServiceDiscovery bool `json:"internalServiceDiscovery"`

	// DiscoveryKubeconfigPath is the path to the kubeconfig file to use for
	// project discovery. When not provided, the operator will use the in-cluster
	// config.
	DiscoveryKubeconfigPath string `json:"discoveryKubeconfigPath"`

	// ProjectKubeconfigPath is the path to the kubeconfig file to use as a
	// template when connecting to project control planes. When not provided,
	// the operator will use the in-cluster config.
	ProjectKubeconfigPath string `json:"projectKubeconfigPath"`

	// ClusterName is the stable, unique name for this edge cluster. It is
	// stamped onto ResourceClaim objects so that each edge controller can
	// distinguish its own claims from those created by other edge controllers
	// in the same project control planes.
	//
	// Required when Mode is "milo". Optional in single mode; defaults to "single".
	ClusterName string `json:"clusterName"`

	// QuotaKubeconfigPath is the path to the kubeconfig file used when creating
	// ResourceClaim objects against Milo project control planes. When set it
	// takes precedence over ProjectKubeconfigPath for quota calls. When both are
	// unset, quota accounting is disabled.
	//
	// Use this field in deployments (mode: single or mode: milo) that need to
	// talk to api.datum.net for quota enforcement.
	QuotaKubeconfigPath string `json:"quotaKubeconfigPath"`
}

func SetDefaults_DiscoveryConfig(obj *DiscoveryConfig) {
	if obj.Mode == "" {
		obj.Mode = multiclusterproviders.ProviderSingle
	}
}

func (c *DiscoveryConfig) DiscoveryRestConfig() (*rest.Config, error) {
	if c.DiscoveryKubeconfigPath == "" {
		return ctrl.GetConfig()
	}

	return clientcmd.BuildConfigFromFlags("", c.DiscoveryKubeconfigPath)
}

func (c *DiscoveryConfig) ProjectRestConfig() (*rest.Config, error) {
	if c.ProjectKubeconfigPath == "" {
		return ctrl.GetConfig()
	}

	return clientcmd.BuildConfigFromFlags("", c.ProjectKubeconfigPath)
}

// QuotaRestConfig returns the REST config for quota ResourceClaim management
// against Milo project control planes. QuotaKubeconfigPath is preferred; if
// unset, ProjectKubeconfigPath is used as a fallback.
//
// Returns (nil, nil) when no credential path is configured at all — this is
// the intentional opt-out case and the caller should disable quota enforcement.
//
// Returns (nil, error) when a credential path IS configured but the file does
// not exist on disk. This is a misconfiguration (Secret not mounted, wrong
// path) that must not silently disable enforcement; callers should treat this
// as a fatal startup error.
func (c *DiscoveryConfig) QuotaRestConfig() (*rest.Config, error) {
	path := c.QuotaKubeconfigPath
	if path == "" {
		path = c.ProjectKubeconfigPath
	}
	if path == "" {
		// No credential path configured: intentional opt-out. Caller logs and
		// disables enforcement.
		return nil, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Path explicitly configured but file absent: operator intended enforcement
		// but the credential is missing (unmounted Secret, wrong path). Fail loud.
		return nil, fmt.Errorf("quota kubeconfig path %q is configured but file does not exist: "+
			"ensure the quota credential Secret is mounted correctly", path)
	}
	return clientcmd.BuildConfigFromFlags("", path)
}

func init() {
	SchemeBuilder.Register(&WorkloadOperator{})
}
