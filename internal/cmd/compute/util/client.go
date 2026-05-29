package util

import (
	"fmt"

	"github.com/spf13/cobra"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/datumctl/plugin"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	resourceManagerGroup   = "resourcemanager.miloapis.com"
	resourceManagerVersion = "v1alpha1"

	// ResourceNamespace is the namespace used for all resource operations within
	// a project's virtual control plane. The project slug routes to the right
	// control plane; within it, everything lives in "default".
	ResourceNamespace = "default"
)

// ProjectControlPlaneURL returns the virtual control-plane URL for a project.
func ProjectControlPlaneURL(apiHost, projectID string) string {
	return fmt.Sprintf("https://%s/apis/%s/%s/projects/%s/control-plane",
		apiHost, resourceManagerGroup, resourceManagerVersion, projectID)
}

// NewClient builds a Kubernetes client targeting the project's virtual control plane.
func NewClient(project string) (client.Client, error) {
	if project == "" {
		return nil, fmt.Errorf("no project set — pass --project or run 'datumctl config set project <name>'")
	}

	ctx := plugin.Context()
	if ctx.APIHost == "" {
		return nil, fmt.Errorf("DATUM_API_HOST is not set; is this plugin running via datumctl?")
	}

	token, err := plugin.Token()
	if err != nil {
		return nil, fmt.Errorf("getting credentials: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := computev1alpha.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering compute scheme: %w", err)
	}
	if err := networkingv1alpha.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering networking scheme: %w", err)
	}
	if err := quotav1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering quota scheme: %w", err)
	}

	cfg := &rest.Config{
		Host:        ProjectControlPlaneURL(ctx.APIHost, project),
		BearerToken: token,
	}

	return client.New(cfg, client.Options{Scheme: scheme})
}

// NewPlatformClient builds a Kubernetes client targeting the platform API server
// (not a project-scoped virtual control plane).
func NewPlatformClient() (client.Client, error) {
	ctx := plugin.Context()
	if ctx.APIHost == "" {
		return nil, fmt.Errorf("DATUM_API_HOST is not set; is this plugin running via datumctl?")
	}

	token, err := plugin.Token()
	if err != nil {
		return nil, fmt.Errorf("getting credentials: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := quotav1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering quota scheme: %w", err)
	}

	cfg := &rest.Config{
		Host:        "https://" + ctx.APIHost,
		BearerToken: token,
	}

	return client.New(cfg, client.Options{Scheme: scheme})
}

// ProjectFromCmd reads the --project persistent flag from the command's root.
func ProjectFromCmd(cmd *cobra.Command) string {
	project, _ := cmd.Root().PersistentFlags().GetString("project")
	return project
}
