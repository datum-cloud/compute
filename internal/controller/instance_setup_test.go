// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcsingle "sigs.k8s.io/multicluster-runtime/providers/single"
)

// startupObservationWindow is how long the manager must stay up before we accept
// that it started cleanly. The #171 failure (a watch whose kind has no CRD)
// surfaces during cache sync well under a second, so this only needs to be long
// enough to give a regressed build the chance to crash.
const startupObservationWindow = 5 * time.Second

// startComputeEnvtest boots an API server with only the compute CRDs installed —
// deliberately WITHOUT quota.miloapis.com — reproducing the edge-cell topology
// from #171 where ResourceClaim has no CRD on the cell cluster.
func startComputeEnvtest(t *testing.T) *rest.Config {
	t.Helper()
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "base", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		CRDs: []*apiextensionsv1.CustomResourceDefinition{{
			ObjectMeta: metav1.ObjectMeta{Name: "servinglocations.locations.miloapis.com"},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "locations.miloapis.com",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural: "servinglocations", Singular: "servinglocation", Kind: "ServingLocation", ListKind: "ServingLocationList",
				},
				Scope: apiextensionsv1.ClusterScoped,
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
					Name: "v1alpha1", Served: true, Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object", XPreserveUnknownFields: new(true),
					}},
				}},
			},
		}},
	}
	// Respect KUBEBUILDER_ASSETS (set by `make test`); fall back to the assets
	// `setup-envtest` writes under bin/ for local `go test` runs.
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		env.BinaryAssetsDirectory = filepath.Join("..", "..", "bin", "k8s",
			fmt.Sprintf("1.31.0-%s-%s", runtime.GOOS, runtime.GOARCH))
	}

	cfg, err := env.Start()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	t.Cleanup(func() { _ = env.Stop() })
	return cfg
}

// TestInstanceSetupWithManager_SingleModeNoQuotaCRD is the #171 regression test.
// On an edge cell (single mode) the only registered cluster is the local cell,
// which has no quota.miloapis.com CRD. With quota configured but the watch gated
// off (watchProviderClaims=false), the quota client (claim writes) must be wired
// while the manager still starts cleanly — a build that registered the watch
// would crash here during cache sync with `no matches for kind "ResourceClaim"`.
func TestInstanceSetupWithManager_SingleModeNoQuotaCRD(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(os.Stderr)))

	cfg := startComputeEnvtest(t)
	scheme := newTestScheme(t)

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

	// A cell always runs with federation, so mirror that here: SetupWithManager
	// requires the client that write-back and finalization depend on.
	r := &InstanceReconciler{FederationClient: newKarmadaFakeClient()}
	require.NoError(t, r.SetupWithManager(
		mgr,
		cfg, // quotaRestConfig set: claim writes are wired even in single mode
		NewSingleModeProjectID(mgr),
		NewSingleModeProjectNamespace(mgr),
		"test-edge",
		func(string) multicluster.ClusterName { return multicluster.ClusterName("single") },
		false, // watchProviderClaims: the fix — do not engage the ResourceClaim watch on the cell
	))

	// SetupWithManager must wire the events-API recorder; without this assertion a
	// refactor dropping the wiring would pass every unit test while silently
	// stopping event emission in production.
	require.NotNil(t, r.recorder, "SetupWithManager must wire the events-API recorder")

	// Claim writes (CRUD) are wired in single mode; only the watch is skipped.
	require.NotNil(t, r.quotaClientManager, "quota client must be wired for claim writes in single mode")

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
		// Stayed up with no ResourceClaim watch engaged despite quota being
		// configured: writes are on, the watch is off, #171 is fixed.
	}
}
