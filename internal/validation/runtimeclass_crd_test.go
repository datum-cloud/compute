// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// bootstrapCatalogDir holds the runtime classes a control plane is installed
// with.
const bootstrapCatalogDir = "../../config/components/runtime-classes"

// startRuntimeClassEnvtest boots an API server with the compute custom
// resource definitions installed and returns a client for it. The API server,
// not Go code, enforces several RuntimeClass schema rules: immutability, the
// controller name shape, and the feature vocabulary. Testing them needs a real
// server.
func startRuntimeClassEnvtest(t *testing.T) client.Client {
	t.Helper()

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "base", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		assets := filepath.Join("..", "..", "bin", "k8s",
			fmt.Sprintf("1.31.0-%s-%s", runtime.GOOS, runtime.GOARCH))
		if _, err := os.Stat(assets); err != nil {
			t.Skip("envtest assets unavailable; run via `make test`")
		}
		env.BinaryAssetsDirectory = assets
	}

	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	scheme := k8sruntime.NewScheme()
	require.NoError(t, computev1alpha.AddToScheme(scheme))

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	return c
}

// TestRuntimeClassCRD covers the schema rules the API server enforces on a
// catalog entry, including that the catalog this repository ships satisfies
// them.
func TestRuntimeClassCRD(t *testing.T) {
	ctx := context.Background()
	c := startRuntimeClassEnvtest(t)

	t.Run("the shipped catalog is accepted", func(t *testing.T) {
		entries, err := os.ReadDir(bootstrapCatalogDir)
		require.NoError(t, err)

		published := 0
		for _, entry := range entries {
			if entry.Name() == "kustomization.yaml" {
				continue
			}

			manifest, err := os.ReadFile(filepath.Join(bootstrapCatalogDir, entry.Name()))
			require.NoError(t, err)

			var class computev1alpha.RuntimeClass
			require.NoError(t, yaml.Unmarshal(manifest, &class), entry.Name())
			require.NoError(t, c.Create(ctx, &class), entry.Name())
			published++

			// A class is cluster-scoped so that every project reads the same
			// catalog.
			require.Empty(t, class.Namespace)

			// No controller has reported on a freshly published class, which
			// differs from a controller rejecting it.
			require.Len(t, class.Status.Conditions, 1)
			require.Equal(t, computev1alpha.RuntimeClassConditionAccepted, class.Status.Conditions[0].Type)
			require.Equal(t, metav1.ConditionUnknown, class.Status.Conditions[0].Status)
			require.Equal(t, computev1alpha.RuntimeClassReasonPending, class.Status.Conditions[0].Reason)
		}
		require.Positive(t, published, "expected the bootstrap catalog to publish at least one class")

		// Exactly one class may be the default, so that a workload selecting
		// no class resolves to a single tier.
		var catalog computev1alpha.RuntimeClassList
		require.NoError(t, c.List(ctx, &catalog))
		defaults := 0
		for _, class := range catalog.Items {
			if class.Spec.Default {
				defaults++
			}
		}
		require.Equal(t, 1, defaults, "the shipped catalog must mark exactly one default class")
	})

	t.Run("the implementing controller cannot be changed", func(t *testing.T) {
		class := newCatalogEntry("immutable-controller")
		require.NoError(t, c.Create(ctx, class))

		class.Spec.ControllerName = "compute.datumapis.com/other-provider"
		require.ErrorContains(t, c.Update(ctx, class), "controllerName is immutable")
	})

	t.Run("the controller name must be domain prefixed", func(t *testing.T) {
		class := newCatalogEntry("bare-controller")
		class.Spec.ControllerName = "unikraft-provider"
		require.Error(t, c.Create(ctx, class))
	})

	t.Run("a class cannot declare a capability the platform has no name for", func(t *testing.T) {
		class := newCatalogEntry("invented-feature")
		class.Spec.Capabilities.Features = []computev1alpha.RuntimeClassFeature{"telepathy"}
		require.Error(t, c.Create(ctx, class))
	})

	t.Run("a class must state its isolation boundary", func(t *testing.T) {
		class := newCatalogEntry("no-boundary")
		class.Spec.Isolation.Boundary = ""
		require.Error(t, c.Create(ctx, class))
	})
}

func newCatalogEntry(name string) *computev1alpha.RuntimeClass {
	return &computev1alpha.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: computev1alpha.RuntimeClassSpec{
			ControllerName: "compute.datumapis.com/test-provider",
			Isolation:      computev1alpha.RuntimeClassIsolation{Boundary: "test"},
			Capabilities: computev1alpha.RuntimeClassCapabilities{
				Features: []computev1alpha.RuntimeClassFeature{
					computev1alpha.RuntimeClassFeatureSandboxRuntime,
				},
			},
		},
	}
}
