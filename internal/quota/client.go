// SPDX-License-Identifier: AGPL-3.0-only

package quota

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ProjectQuotaClientManager builds and caches controller-runtime clients that
// target individual Milo project control planes. It is safe for concurrent use.
type ProjectQuotaClientManager struct {
	baseRestConfig *rest.Config
	clients        sync.Map // key: projectID (string), value: client.Client
}

// New returns a ProjectQuotaClientManager that derives per-project REST configs
// from baseRestConfig by rewriting the host path.
func New(baseRestConfig *rest.Config) *ProjectQuotaClientManager {
	return &ProjectQuotaClientManager{baseRestConfig: baseRestConfig}
}

// StoreClient pre-populates the cache with a pre-built client for projectID.
// This is intended for use in unit tests where a real REST server is unavailable.
func (m *ProjectQuotaClientManager) StoreClient(projectID string, cl client.Client) {
	m.clients.Store(projectID, cl)
}

// ClientForProject returns a client.Client targeting the Milo project control
// plane for projectID. The client is constructed once and cached for subsequent
// calls. scheme must include all types the caller intends to operate on,
// including quotav1alpha1.
func (m *ProjectQuotaClientManager) ClientForProject(
	ctx context.Context,
	projectID string,
	scheme *runtime.Scheme,
) (client.Client, error) {
	if v, ok := m.clients.Load(projectID); ok {
		return v.(client.Client), nil
	}

	cfg := rest.CopyConfig(m.baseRestConfig)
	apiHost, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to parse base host: %w", err)
	}
	apiHost.Path = fmt.Sprintf(
		"/apis/resourcemanager.miloapis.com/v1alpha1/projects/%s/control-plane",
		projectID,
	)
	cfg.Host = apiHost.String()

	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create client for project %q: %w", projectID, err)
	}

	m.clients.Store(projectID, cl)
	return cl, nil
}
