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

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/pkg/runtimeclass"
)

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

// registeredCatalog is the catalog a control plane holds once every provider
// has registered the classes it serves. This repository defines the class type
// and ships no class, so a test that needs a populated catalog builds one from
// invented names. A test written against the names a provider registers could
// not tell a catalog lookup from a compiled-in one.
func registeredCatalog() []computev1alpha.RuntimeClass {
	azurite := makeRuntimeClass(testClassAzurite, withDefault)
	azurite.Spec.ControllerName = "compute.datumapis.com/azurite-provider"
	azurite.Spec.Isolation.Boundary = "azurite-sandbox"

	basalt := makeRuntimeClass(testClassBasalt)
	basalt.Spec.ControllerName = "compute.datumapis.com/basalt-provider"
	basalt.Spec.Isolation.Boundary = "virtual-machine"

	return []computev1alpha.RuntimeClass{azurite, basalt}
}

// TestRuntimeClassCRD covers the schema rules the API server enforces on a
// catalog entry, and how a catalog reads once providers have registered into
// it.
func TestRuntimeClassCRD(t *testing.T) {
	ctx := context.Background()
	c := startRuntimeClassEnvtest(t)

	t.Run("a class a provider registers is accepted", func(t *testing.T) {
		for _, entry := range registeredCatalog() {
			class := entry
			require.NoError(t, c.Create(ctx, &class), class.Name)
			t.Cleanup(func() { _ = c.Delete(ctx, &class) })

			// A class is cluster-scoped so that every project reads the same
			// catalog.
			require.Empty(t, class.Namespace)

			// No controller has reported on a freshly registered class, which
			// differs from a controller rejecting it.
			require.Len(t, class.Status.Conditions, 1)
			require.Equal(t, computev1alpha.RuntimeClassConditionAccepted, class.Status.Conditions[0].Type)
			require.Equal(t, metav1.ConditionUnknown, class.Status.Conditions[0].Status)
			require.Equal(t, computev1alpha.RuntimeClassReasonPending, class.Status.Conditions[0].Reason)
		}

		// A workload that names no class runs in the class the catalog marks
		// as the default, so a catalog serving instances needs exactly one
		// such class. The property holds over whatever set of classes
		// providers register, not over anything this repository ships.
		var catalog computev1alpha.RuntimeClassList
		require.NoError(t, c.List(ctx, &catalog))
		resolved := runtimeclass.Catalog(catalog.Items).Default()
		require.NotNil(t, resolved, "a catalog with one class marked default must resolve it")
		require.Equal(t, testClassAzurite, resolved.Name)
	})

	// Two providers can each register a class marked as the default, because
	// the API server validates one object at a time and no controller
	// reconciles the catalog as a whole. An ambiguous catalog states no
	// default, so an instance naming no class is refused at admission instead
	// of landing in a tier picked arbitrarily. Refusing the second default at
	// registration time is not implemented.
	t.Run("a second class registering itself as default is not refused", func(t *testing.T) {
		for _, name := range []string{testClassAzurite, testClassBasalt} {
			class := makeRuntimeClass(name, withDefault)
			require.NoError(t, c.Create(ctx, &class), name)
			t.Cleanup(func() { _ = c.Delete(ctx, &class) })
		}

		var catalog computev1alpha.RuntimeClassList
		require.NoError(t, c.List(ctx, &catalog))
		require.Nil(t, runtimeclass.Catalog(catalog.Items).Default(),
			"a catalog with several classes marked default states no default")
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
