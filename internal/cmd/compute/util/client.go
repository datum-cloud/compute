package util

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	computev1alpha "go.datum.net/compute/api/v1alpha"
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

	apiHost := os.Getenv("DATUM_API_HOST")
	if apiHost == "" {
		return nil, fmt.Errorf("DATUM_API_HOST is not set; is this plugin running via datumctl?")
	}

	token, err := pluginToken()
	if err != nil {
		return nil, fmt.Errorf("getting credentials: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := computev1alpha.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering compute scheme: %w", err)
	}

	cfg := &rest.Config{
		Host:        ProjectControlPlaneURL(apiHost, project),
		BearerToken: token,
	}

	return client.New(cfg, client.Options{Scheme: scheme})
}

// pluginToken retrieves a bearer token by calling the datumctl credentials
// helper injected via DATUM_CREDENTIALS_HELPER.
func pluginToken() (string, error) {
	helper := os.Getenv("DATUM_CREDENTIALS_HELPER")
	if helper == "" {
		return "", fmt.Errorf("DATUM_CREDENTIALS_HELPER is not set; is this plugin running via datumctl?")
	}
	out, err := exec.Command(helper, "auth", "get-token").Output()
	if err != nil {
		return "", fmt.Errorf("credentials helper: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ProjectFromCmd reads the --project persistent flag from the command's root.
func ProjectFromCmd(cmd *cobra.Command) string {
	project, _ := cmd.Root().PersistentFlags().GetString("project")
	return project
}
