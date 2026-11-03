// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcsingle "sigs.k8s.io/multicluster-runtime/providers/single"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

// TestWorkloadDeploymentSetupWithManager_CellModeNoNetworkingCRD asserts that with
// the networking integration disabled the WorkloadDeployment reconciler starts
// cleanly on an edge cell that carries no networking.datumapis.com CRDs. The only
// registered cluster is the local cell; a build that left networking enabled would
// register the NetworkInterfaceClaim/Location watches and crash during cache sync
// with no matches for those kinds.
func TestWorkloadDeploymentSetupWithManager_CellModeNoNetworkingCRD(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(os.Stderr)))

	cfg := startComputeEnvtest(t)

	// Register the networking types in the scheme but leave their CRDs absent from
	// the API server (startComputeEnvtest installs compute CRDs only). Enabling the
	// watches then fails at cache sync — the real cell failure — rather than at
	// Complete() with a scheme lookup error.
	scheme := runtime.NewScheme()
	require.NoError(t, computev1alpha.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, networkingv1alpha.AddToScheme(scheme))
	require.NoError(t, locationsv1alpha1.AddToScheme(scheme))

	deploymentCluster, err := cluster.New(cfg, func(o *cluster.Options) { o.Scheme = scheme })
	require.NoError(t, err)

	// The single provider registers only the local cell cluster, mirroring
	// cmd/main.go's mcsingle.New for an edge cell.
	provider := mcsingle.New(multicluster.ClusterName("single"), deploymentCluster)
	mgr, err := mcmanager.New(cfg, provider, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	r := &WorkloadDeploymentReconciler{NetworkingEnabled: false}
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
		// Stayed up with no networking watches engaged despite the networking types
		// having no CRD on the cell: the WD reconciler is crash-safe by default.
	}
}
