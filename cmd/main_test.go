// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	multiclusterproviders "go.miloapis.com/milo/pkg/multicluster-runtime"

	"go.datum.net/compute/internal/locations"
)

// TestComputeWatchProviderClaims is the #171 guard: quota enforcement (and thus
// the ResourceClaim watch) is wired only in Milo mode. Single/cluster mode must
// stay false, so the manager never engages a ResourceClaim watch against a cell
// that has no quota CRD.
func TestComputeWatchProviderClaims(t *testing.T) {
	assert.True(t, computeWatchProviderClaims(multiclusterproviders.ProviderMilo),
		"milo mode wires the ResourceClaim watch")
	assert.False(t, computeWatchProviderClaims(multiclusterproviders.ProviderSingle),
		"single mode must not wire the watch (#171)")
	assert.False(t, computeWatchProviderClaims(multiclusterproviders.ProviderKind),
		"non-milo modes disable quota")
}

// TestLoadServerConfig_LocationSource covers the startup guard: a config that
// names an unknown location source fails to load rather than surfacing later
// on every reconcile.
func TestLoadServerConfig_LocationSource(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return path
	}

	const header = `apiVersion: apiserver.config.datumapis.com/v1alpha1
kind: WorkloadOperator
metricsServer:
  bindAddress: "0"
`

	cfg, err := loadServerConfig(write(t, header))
	require.NoError(t, err)
	assert.Equal(t, locations.SourceNetworkServices, cfg.LocationSource)

	cfg, err = loadServerConfig(write(t, header+"locationSource: Locations\n"))
	require.NoError(t, err)
	assert.Equal(t, locations.SourceLocations, cfg.LocationSource)

	_, err = loadServerConfig(write(t, header+"locationSource: Nonsense\n"))
	require.Error(t, err)
}

// TestSchemeResolvesLocationKinds is the runtime half of the typed locations
// dependency: compiling against the types says nothing about whether a client
// can resolve them. A kind missing here fails every list at runtime with "no
// kind is registered", not at build time.
func TestSchemeResolvesLocationKinds(t *testing.T) {
	for _, object := range []client.Object{
		&locationsv1alpha1.Location{},
		&locationsv1alpha1.ServingLocation{},
		&networkingv1alpha.LocationBinding{},
		&networkingv1alpha.ServingLocation{},
	} {
		gvk, err := apiutil.GVKForObject(object, scheme)
		require.NoErrorf(t, err, "%T must be registered on the manager scheme", object)
		assert.NotEmpty(t, gvk.Kind)
	}
}

// TestServingLocationObjectIsRegistered pins the watch the deployment
// reconciler installs: whichever source is selected, the object it watches has
// to be resolvable on the manager scheme.
func TestServingLocationObjectIsRegistered(t *testing.T) {
	for _, source := range []locations.Source{"", locations.SourceNetworkServices, locations.SourceLocations} {
		object, err := locations.ServingLocationObject(source)
		require.NoError(t, err)

		_, err = apiutil.GVKForObject(object, scheme)
		require.NoErrorf(t, err, "the watch object for source %q must be registered", source)
	}
}
