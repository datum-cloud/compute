// SPDX-License-Identifier: AGPL-3.0-only

package locations

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

// locationsCRDDir resolves the CRDs shipped by the locations module, so the
// test installs the same schemas the service serves rather than a local copy
// that can drift from the pinned commit.
func locationsCRDDir(t *testing.T) string {
	t.Helper()

	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "go.miloapis.com/locations").Output()
	require.NoError(t, err, "the locations module must be resolvable")

	dir := filepath.Join(strings.TrimSpace(string(out)), "config", "base", "crd", "bases")
	_, err = os.Stat(dir)
	require.NoError(t, err)
	return dir
}

// TestLocationsSource_AgainstAPIServer is the runtime half of the typed switch.
// Compiling against the locations types proves nothing about whether a client
// can resolve the kinds, so this runs both states against a real API server:
// the CRDs absent, which must read as no locations, and the CRDs installed,
// which must read the objects back.
func TestLocationsSource_AgainstAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		assets := filepath.Join("..", "..", "bin", "k8s",
			fmt.Sprintf("1.31.0-%s-%s", runtime.GOOS, runtime.GOARCH))
		if _, err := os.Stat(assets); err != nil {
			t.Skip("no envtest assets; run via make test")
		}
		t.Setenv("KUBEBUILDER_ASSETS", assets)
	}

	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = testEnv.Stop() })

	ctx := context.Background()
	scheme := testScheme(t)

	newClient := func() client.Client {
		c, err := client.New(rest.CopyConfig(cfg), client.Options{Scheme: scheme})
		require.NoError(t, err)
		return c
	}

	t.Run("CRDs absent reads as no locations", func(t *testing.T) {
		c := newClient()

		// A typed client reaches the REST mapper through discovery, which wraps
		// the no-match. Assert on the raw error so a client-go or
		// controller-runtime bump that changes the wrapping fails here, rather
		// than quietly turning the degrade into a reconcile failure.
		var absent locationsv1alpha1.LocationList
		rawErr := c.List(ctx, &absent)
		require.Error(t, rawErr, "the CRD really is absent, so the degrade is under test")
		assert.True(t, kindNotInstalled(rawErr),
			"an absent CRD must stay recognisable as such: %T / %v", rawErr, rawErr)

		found, err := ListPlacementLocations(ctx, c, SourceLocations)
		require.NoError(t, err, "a control plane without the CRDs must not fail the reconcile")
		assert.Empty(t, found)

		serving, err := ListServingLocations(ctx, c, SourceLocations)
		require.NoError(t, err)
		assert.Empty(t, serving)

		// The default source has no such degrade: its kinds are expected
		// everywhere it runs, so their absence stays an error.
		_, err = ListPlacementLocations(ctx, c, SourceNetworkServices)
		require.Error(t, err)
	})

	_, err = envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{
		Paths: []string{locationsCRDDir(t)},
	})
	require.NoError(t, err)

	t.Run("CRDs installed resolve and read back", func(t *testing.T) {
		c := newClient()

		require.NoError(t, c.Create(ctx, newLocation("dfw", testCityCode)))
		require.NoError(t, c.Create(ctx, &locationsv1alpha1.ServingLocation{
			ObjectMeta: metav1.ObjectMeta{Name: "dfw"},
			Spec: locationsv1alpha1.ServingLocationSpec{
				Topology: map[string]string{TopologyCityCodeKey: testCityCode},
			},
		}))

		found, err := ListPlacementLocations(ctx, c, SourceLocations)
		require.NoError(t, err)
		require.Len(t, found, 1)
		assert.Equal(t, []string{testCityCode}, CityCodes(found).UnsortedList())

		serving, err := ListServingLocations(ctx, c, SourceLocations)
		require.NoError(t, err)
		require.Len(t, serving, 1)
		assert.Equal(t, "dfw", serving[0].Name)
		assert.Equal(t, testCityCode, serving[0].CityCode())
	})
}
