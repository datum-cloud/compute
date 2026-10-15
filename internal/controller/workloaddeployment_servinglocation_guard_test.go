// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcsingle "sigs.k8s.io/multicluster-runtime/providers/single"

	"go.datum.net/compute/internal/locations"
)

// moduleCRD resolves a single CRD manifest shipped by a dependency, so the
// test installs the schema that module actually serves.
func moduleCRD(t *testing.T, module, dir, file string) string {
	t.Helper()

	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module).Output()
	require.NoError(t, err)

	path := filepath.Join(strings.TrimSpace(string(out)), filepath.FromSlash(dir), file)
	_, err = os.Stat(path)
	require.NoError(t, err)
	return path
}

// TestSetupWithManager_ServingLocationKindGuard covers the watch guard against
// real discovery.
//
// Registering a watch on a kind the cell does not serve wedges the manager
// during cache sync, so setup refuses instead. The guard has to fire for the
// selected source and stay silent about the other one: a deployment reading
// network services must not acquire a startup dependency on the locations
// service it never opted into.
//
// The refusals run through SetupWithManager, which also settles that discovery
// answers at registration time, on a manager that has not been started. The
// permitting direction is asserted on the manager's own REST mapper, because
// controller-runtime registers controller names in a process-global set and
// TestWorkloadDeploymentSetupWithManager_CellModeNoNetworkingCRD already claims
// this reconciler's name for the package.
func TestSetupWithManager_ServingLocationKindGuard(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(os.Stderr)))

	cfg := startComputeEnvtest(t)
	scheme := newLocationsServiceScheme()

	// newManager builds a manager the way cmd/main.go does for a cell.
	newManager := func(t *testing.T) mcmanager.Manager {
		t.Helper()

		deploymentCluster, err := cluster.New(rest.CopyConfig(cfg), func(o *cluster.Options) { o.Scheme = scheme })
		require.NoError(t, err)

		mgr, err := mcmanager.New(rest.CopyConfig(cfg),
			mcsingle.New(multicluster.ClusterName("single"), deploymentCluster),
			ctrl.Options{
				Scheme:                 scheme,
				Metrics:                metricsserver.Options{BindAddress: "0"},
				HealthProbeBindAddress: "0",
			})
		require.NoError(t, err)
		return mgr
	}

	setupWithSource := func(t *testing.T, source locations.Source) error {
		t.Helper()
		r := &WorkloadDeploymentReconciler{NetworkingEnabled: true, LocationSource: source}
		return r.SetupWithManager(newManager(t))
	}

	// ensureWithSource runs the guard the way SetupWithManager runs it, against
	// the same live REST mapper.
	ensureWithSource := func(t *testing.T, source locations.Source) error {
		t.Helper()
		return locations.EnsureServingLocationKind(newManager(t).GetLocalManager().GetRESTMapper(), source)
	}

	installCRD := func(t *testing.T, path string) {
		t.Helper()
		_, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{Paths: []string{path}})
		require.NoError(t, err)
	}

	networkServicesCRD := moduleCRD(t, "go.datum.net/network-services-operator",
		"config/crd/bases", "networking.datumapis.com_servinglocations.yaml")
	locationsCRD := moduleCRD(t, "go.miloapis.com/locations",
		"config/base/crd/bases", "locations.miloapis.com_servinglocations.yaml")

	t.Run("neither kind served", func(t *testing.T) {
		err := setupWithSource(t, "")
		require.Error(t, err, "the default source must refuse to watch a kind the cell does not serve")
		assert.Contains(t, err.Error(), "servinglocations.networking.datumapis.com",
			"the error must name the CRD to install")
		assert.Contains(t, err.Error(), "NetworkServices",
			"the error must name the locationSource that requires it")
		assert.Contains(t, err.Error(), "ServingLocation")

		err = setupWithSource(t, locations.SourceLocations)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "servinglocations.locations.miloapis.com")
		assert.Contains(t, err.Error(), "Locations")
	})

	// Install only the locations service kind. The Locations source must now be
	// permitted and the default must still refuse: each source gates on the one
	// kind it watches.
	installCRD(t, locationsCRD)

	t.Run("only the locations service kind served", func(t *testing.T) {
		require.NoError(t, ensureWithSource(t, locations.SourceLocations),
			"the selected source's kind is served, so the guard must permit it")

		err := setupWithSource(t, "")
		require.Error(t, err, "installing the locations service must not satisfy the default source")
		assert.Contains(t, err.Error(), "servinglocations.networking.datumapis.com")
	})

	installCRD(t, networkServicesCRD)

	t.Run("both kinds served", func(t *testing.T) {
		for _, source := range []locations.Source{"", locations.SourceNetworkServices, locations.SourceLocations} {
			require.NoErrorf(t, ensureWithSource(t, source),
				"the guard must permit source %q once its kind is served", source)
		}
	})
}
