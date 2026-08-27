// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
