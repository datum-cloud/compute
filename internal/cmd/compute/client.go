package compute

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.datum.net/datumctl/plugin"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	resourceManagerGroup = "resourcemanager.miloapis.com"
	resourceManagerVersion = "v1alpha1"
)

// projectControlPlaneURL returns the virtual control-plane URL for a project.
// Each project in Datum Cloud has its own isolated Kubernetes-style API endpoint
// rooted here; all resource operations (List, Get, Create, etc.) target this URL.
func projectControlPlaneURL(apiHost, projectID string) string {
	return fmt.Sprintf("https://%s/apis/%s/%s/projects/%s/control-plane",
		apiHost, resourceManagerGroup, resourceManagerVersion, projectID)
}

// newClient builds a Kubernetes client targeting the project's virtual control plane.
// It acquires a fresh bearer token via the datumctl credentials helper on each call,
// so it must not be cached across long-running operations.
//
// project is the resolved project slug — callers should read it from the cobra
// --project persistent flag (set on the root command by plugin.NewRootCmd), which
// defaults to the DATUM_PROJECT value injected by datumctl but can be overridden
// by the user at invocation time.
//
// The returned project string must be passed as the namespace to all client operations:
//
//	c.List(ctx, list, client.InNamespace(project))
func newClient(project string) (client.Client, error) {
	if project == "" {
		return nil, fmt.Errorf("no project set — pass --project or run 'datumctl config set project <name>'")
	}

	pluginCtx := plugin.Context()
	if pluginCtx.APIHost == "" {
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

	cfg := &rest.Config{
		Host:        projectControlPlaneURL(pluginCtx.APIHost, project),
		BearerToken: token,
	}

	return client.New(cfg, client.Options{Scheme: scheme})
}

// projectFromCmd reads the --project persistent flag from the command's root,
// which plugin.NewRootCmd wires with DATUM_PROJECT as the default.
func projectFromCmd(cmd *cobra.Command) string {
	project, _ := cmd.Root().PersistentFlags().GetString("project")
	return project
}
