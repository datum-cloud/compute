// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"fmt"
	"net/http"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	consumerprovider "go.miloapis.com/service-catalog/pkg/multicluster-runtime/consumer"
)

// ProjectReaders hands out uncached readers for project control planes.
//
// Admission for a project can arrive before the multicluster provider has
// engaged that project. Service-catalog installs a service's resources the
// moment the consumer is entitled, and the provider only learns of the
// entitlement through its own watch, so the first install usually wins the
// race. A webhook that needs only live reads does not need the engaged
// cluster, and reading through these readers lets it admit the install
// instead of denying it until engagement catches up.
//
// All readers share one HTTP client and one REST mapper, so building one per
// request costs no connections or discovery round trips.
type ProjectReaders struct {
	base       *rest.Config
	scheme     *runtime.Scheme
	httpClient *http.Client
	mapper     meta.RESTMapper
}

// NewProjectReaders builds readers for the project control planes behind
// base, the Milo root API config. mapperProject names a project whose control
// plane serves the same APIs as every other project; it backs the shared REST
// mapper, which resolves kinds lazily on first use.
func NewProjectReaders(base *rest.Config, mapperProject string, scheme *runtime.Scheme) (*ProjectReaders, error) {
	httpClient, err := rest.HTTPClientFor(base)
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP client for project readers: %w", err)
	}
	mapperConfig, err := consumerprovider.ProjectRestConfig(base, mapperProject)
	if err != nil {
		return nil, err
	}
	mapper, err := apiutil.NewDynamicRESTMapper(mapperConfig, httpClient)
	if err != nil {
		return nil, fmt.Errorf("failed to build REST mapper for project readers: %w", err)
	}
	return &ProjectReaders{
		base:       base,
		scheme:     scheme,
		httpClient: httpClient,
		mapper:     mapper,
	}, nil
}

// Reader returns an uncached reader for the named project's control plane.
func (r *ProjectReaders) Reader(project string) (client.Reader, error) {
	cfg, err := consumerprovider.ProjectRestConfig(r.base, project)
	if err != nil {
		return nil, err
	}
	cl, err := client.New(cfg, client.Options{
		Scheme:     r.scheme,
		HTTPClient: r.httpClient,
		Mapper:     r.mapper,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build reader for project %q: %w", project, err)
	}
	return cl, nil
}
