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
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
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

// envtestLocationName is the one location every subtest below creates and
// reads back.
const envtestLocationName = "dfw"

// serviceAvailabilityCRD resolves the ServiceAvailability CRD shipped by the
// service-catalog module, for the same reason as locationsCRDDir.
func serviceAvailabilityCRD(t *testing.T) string {
	t.Helper()

	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "go.miloapis.com/service-catalog").Output()
	require.NoError(t, err, "the service-catalog module must be resolvable")

	path := filepath.Join(strings.TrimSpace(string(out)), "config", "base", "crd", "bases",
		"services.miloapis.com_serviceavailabilities.yaml")
	_, err = os.Stat(path)
	require.NoError(t, err)
	return path
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

		require.NoError(t, c.Create(ctx, newLocation(envtestLocationName, testCityCode)))
		require.NoError(t, c.Create(ctx, &locationsv1alpha1.ServingLocation{
			ObjectMeta: metav1.ObjectMeta{Name: envtestLocationName},
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
		assert.Equal(t, envtestLocationName, serving[0].Name)
		assert.Equal(t, testCityCode, serving[0].CityCode())
	})

	// markReady sets the Ready condition on the location through the status
	// subresource, the way the locations controller does.
	markReady := func(t *testing.T, c client.Client) {
		t.Helper()
		var location locationsv1alpha1.Location
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: envtestLocationName}, &location))
		location.Status.Conditions = []metav1.Condition{{
			Type: locationsv1alpha1.LocationConditionReady, Status: metav1.ConditionTrue,
			Reason: "Serving", LastTransitionTime: metav1.Now(),
		}}
		require.NoError(t, c.Status().Update(ctx, &location))
	}

	t.Run("availability CRD absent enforces no gate", func(t *testing.T) {
		c := newClient()
		markReady(t, c)

		// The absence must be recognisable for the same reason as above: a
		// wrapping change would turn every project without the mirror into
		// one where nothing can be placed.
		var absent servicesv1alpha1.ServiceAvailabilityList
		rawErr := c.List(ctx, &absent)
		require.Error(t, rawErr, "the CRD really is absent, so the degrade is under test")
		assert.True(t, kindNotInstalled(rawErr), "an absent CRD must stay recognisable as such: %T / %v", rawErr, rawErr)

		found, err := ListPlacementLocations(ctx, c, SourceLocations)
		require.NoError(t, err)
		require.Len(t, found, 1)
		assert.True(t, found[0].Placeable(), "with no availability to consult, Ready alone decides")
	})

	_, err = envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{
		Paths: []string{serviceAvailabilityCRD(t)},
	})
	require.NoError(t, err)

	t.Run("availability CRD installed gates on a compute record", func(t *testing.T) {
		c := newClient()

		found, err := ListPlacementLocations(ctx, c, SourceLocations)
		require.NoError(t, err)
		require.Len(t, found, 1)
		assert.False(t, found[0].Placeable(), "the kind is served but no record says compute runs here")

		availability := newComputeAvailability(envtestLocationName)
		conditions := availability.Status.Conditions
		availability.Status = servicesv1alpha1.ServiceAvailabilityStatus{}
		require.NoError(t, c.Create(ctx, availability))
		for i := range conditions {
			conditions[i].Reason = "Available"
			conditions[i].LastTransitionTime = metav1.Now()
		}
		availability.Status.Conditions = conditions
		require.NoError(t, c.Status().Update(ctx, availability))

		found, err = ListPlacementLocations(ctx, c, SourceLocations)
		require.NoError(t, err)
		require.Len(t, found, 1)
		assert.True(t, found[0].Placeable(), "an Available compute record opens the gate")

		available, enforced, err := AvailableLocations(ctx, c)
		require.NoError(t, err)
		assert.True(t, enforced)
		assert.Equal(t, []string{envtestLocationName}, available.UnsortedList())
	})
}
