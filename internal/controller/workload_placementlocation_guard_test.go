// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"os"
	"testing"
	"time"

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

// TestWorkloadSetupWithManager_PlacementLocationKindAbsent is the regression
// test for the placement-location watch.
//
// The watch is registered against every project control plane the manager
// engages. A control plane that does not answer for the kind — because the CRD
// is absent, or because the manager is not allowed to list it — never syncs
// that informer, and controller-runtime then blocks EVERY controller on the
// manager from starting: the workload reconciler, the referenced-data
// reconciler and the deployment federator all sit on "Starting EventSource"
// forever, so nothing is federated and no finalizer is ever written. The
// manager eventually gives up waiting for cluster engagement and exits.
//
// So the watch has to be skipped for a control plane that does not serve the
// kind rather than registered and left to wedge. Here the compute CRDs are
// installed but the location CRDs are not, which is the same shape as a control
// plane that refuses the list.
func TestWorkloadSetupWithManager_PlacementLocationKindAbsent(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(os.Stderr)))

	cfg := startComputeEnvtest(t)
	// The location types resolve on the scheme but have no CRD on the API
	// server, so a registered watch fails at cache sync — the real failure —
	// rather than at Complete() with a scheme lookup error.
	scheme := newLocationsServiceScheme()

	newCluster := func(t *testing.T) cluster.Cluster {
		t.Helper()
		cl, err := cluster.New(rest.CopyConfig(cfg), func(o *cluster.Options) { o.Scheme = scheme })
		require.NoError(t, err)
		return cl
	}

	// serves asks the guard the way the builder asks it: per cluster, against
	// that cluster's live REST mapper.
	serves := func(t *testing.T, source locations.Source) bool {
		t.Helper()
		return placementLocationClusterFilter(source)(multicluster.ClusterName("single"), newCluster(t))
	}

	t.Run("neither kind served", func(t *testing.T) {
		assert.False(t, serves(t, ""),
			"the default source must not watch a kind this control plane does not serve")
		assert.False(t, serves(t, locations.SourceNetworkServices))
		assert.False(t, serves(t, locations.SourceLocations))
	})

	// The manager itself has to survive that: setup succeeds, the watch is
	// simply not engaged, and every other controller starts. A build that
	// registered the watch unconditionally exits here during cluster
	// engagement.
	t.Run("manager starts with the kind absent", func(t *testing.T) {
		deploymentCluster := newCluster(t)

		mgr, err := mcmanager.New(rest.CopyConfig(cfg),
			mcsingle.New(multicluster.ClusterName("single"), deploymentCluster),
			ctrl.Options{
				Scheme:                 scheme,
				Metrics:                metricsserver.Options{BindAddress: "0"},
				HealthProbeBindAddress: "0",
			})
		require.NoError(t, err)

		r := &WorkloadReconciler{}
		require.NoError(t, r.SetupWithManager(mgr))

		// The single provider does not start the cluster it engages, so — like
		// cmd/main.go — the cluster and the manager run as sibling goroutines.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		errCh := make(chan error, 2)
		go func() { errCh <- deploymentCluster.Start(ctx) }()
		go func() { errCh <- mgr.Start(ctx) }()

		select {
		case err := <-errCh:
			t.Fatalf("manager exited during startup, want it to stay up: %v", err)
		case <-time.After(startupObservationWindow):
		}
	})

	// Each source gates on the one kind it reads. Installing the locations
	// service must not satisfy the default source, and vice versa.
	installCRD := func(t *testing.T, path string) {
		t.Helper()
		_, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{Paths: []string{path}})
		require.NoError(t, err)
	}

	installCRD(t, moduleCRD(t, "go.miloapis.com/locations",
		"config/base/crd/bases", "locations.miloapis.com_locations.yaml"))

	t.Run("only the locations service kind served", func(t *testing.T) {
		assert.True(t, serves(t, locations.SourceLocations),
			"the selected source's kind is served, so the watch must engage")
		assert.False(t, serves(t, ""),
			"installing the locations service must not satisfy the default source")
	})

	installCRD(t, moduleCRD(t, "go.datum.net/network-services-operator",
		"config/crd/bases", "networking.datumapis.com_locationbindings.yaml"))

	t.Run("both kinds served", func(t *testing.T) {
		for _, source := range []locations.Source{"", locations.SourceNetworkServices, locations.SourceLocations} {
			assert.Truef(t, serves(t, source),
				"the watch must engage for source %q once its kind is served", source)
		}
	})
}
