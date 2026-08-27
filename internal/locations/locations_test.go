// SPDX-License-Identifier: AGPL-3.0-only

package locations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

const (
	testCityCode      = "DFW"
	testOtherCityCode = "ORD"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	require.NoError(t, networkingv1alpha.AddToScheme(s))
	require.NoError(t, locationsv1alpha1.AddToScheme(s))
	return s
}

func newBinding(name, cityCode string) *networkingv1alpha.LocationBinding {
	return &networkingv1alpha.LocationBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: networkingv1alpha.LocationBindingSpec{
			LocationRef: corev1.LocalObjectReference{Name: name},
			Topology:    map[string]string{TopologyCityCodeKey: cityCode},
		},
	}
}

func newLocation(name, cityCode string) *locationsv1alpha1.Location {
	return &locationsv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: locationsv1alpha1.LocationSpec{
			LocationClassRef: locationsv1alpha1.LocationClassReference{Name: "datum-managed"},
			Topology:         map[string]string{TopologyCityCodeKey: cityCode},
		},
	}
}

// TestTopologyKeysAgreeAcrossSources guards the migration's central assumption:
// a city code means the same thing whichever source served it. If the two
// groups ever disagree, switching sources would silently repoint every
// placement.
func TestTopologyKeysAgreeAcrossSources(t *testing.T) {
	t.Parallel()

	assert.Equal(t, networkingv1alpha.TopologyCityCodeKey, TopologyCityCodeKey)
	assert.Equal(t, networkingv1alpha.ServingLocationTopologyLabel, ServingLocationTopologyLabel)
}

func TestSourceResolve(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		source Source
		want   Source
		wantOK bool
	}{
		{source: "", want: SourceNetworkServices, wantOK: true},
		{source: SourceNetworkServices, want: SourceNetworkServices, wantOK: true},
		{source: SourceLocations, want: SourceLocations, wantOK: true},
		{source: "Nonsense"},
	} {
		resolved, err := tc.source.Resolve()
		if !tc.wantOK {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, tc.want, resolved)
	}
}

func TestListPlacementLocations_NetworkServices(t *testing.T) {
	t.Parallel()

	cl := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(
			newBinding("dfw", testCityCode),
			newBinding("ord", testOtherCityCode),
			// A binding with no city code contributes no placement city.
			&networkingv1alpha.LocationBinding{ObjectMeta: metav1.ObjectMeta{Name: "nowhere"}},
			// The locations service must not be read when network services is
			// selected.
			newLocation("lhr", "LHR"),
		).
		Build()

	found, err := ListPlacementLocations(context.Background(), cl, SourceNetworkServices)
	require.NoError(t, err)
	require.Len(t, found, 3)
	assert.ElementsMatch(t, []string{testCityCode, testOtherCityCode}, CityCodes(found).UnsortedList())
}

func TestListPlacementLocations_Locations(t *testing.T) {
	t.Parallel()

	cl := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(
			newLocation("dfw", testCityCode),
			newLocation("ord", testOtherCityCode),
			// The network services source must not be read when the locations
			// service is selected.
			newBinding("lhr", "LHR"),
		).
		Build()

	found, err := ListPlacementLocations(context.Background(), cl, SourceLocations)
	require.NoError(t, err)
	require.Len(t, found, 2)
	assert.ElementsMatch(t, []string{testCityCode, testOtherCityCode}, CityCodes(found).UnsortedList())
}

func TestListPlacementLocations_UnknownSource(t *testing.T) {
	t.Parallel()

	cl := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()

	_, err := ListPlacementLocations(context.Background(), cl, "Nonsense")
	require.Error(t, err)
}

// TestKindNotInstalled_ScopedToLocationsSource pins the degrade to the source
// that needs it. A no-match reads as no locations for the locations service,
// which may not be installed yet, and still fails for network services, whose
// kinds every control plane already serves.
func TestKindNotInstalled_ScopedToLocationsSource(t *testing.T) {
	t.Parallel()

	noMatch := func(_ context.Context, _ client.WithWatch, list client.ObjectList, _ ...client.ListOption) error {
		gvk := list.GetObjectKind().GroupVersionKind()
		return &apimeta.NoKindMatchError{GroupKind: gvk.GroupKind().WithVersion("").GroupKind()}
	}

	cl := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{List: noMatch}).
		Build()

	ctx := context.Background()

	found, err := ListPlacementLocations(ctx, cl, SourceLocations)
	require.NoError(t, err)
	assert.Empty(t, found)

	serving, err := ListServingLocations(ctx, cl, SourceLocations)
	require.NoError(t, err)
	assert.Empty(t, serving)

	_, err = ListPlacementLocations(ctx, cl, SourceNetworkServices)
	require.Error(t, err)

	_, err = ListServingLocations(ctx, cl, SourceNetworkServices)
	require.Error(t, err)
}

// TestListLocations_SchemeMissingStillFails separates a control plane that does
// not serve the kind from a binary that forgot to register it. The first reads
// as empty; the second is a wiring mistake and must keep surfacing.
func TestListLocations_SchemeMissingStillFails(t *testing.T) {
	t.Parallel()

	bare := runtime.NewScheme()
	require.NoError(t, networkingv1alpha.AddToScheme(bare))

	cl := fake.NewClientBuilder().WithScheme(bare).Build()

	_, err := ListPlacementLocations(context.Background(), cl, SourceLocations)
	require.Error(t, err, "an unregistered type is a wiring mistake, not an empty control plane")
}

func TestListServingLocations(t *testing.T) {
	t.Parallel()

	cl := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(
			&networkingv1alpha.ServingLocation{
				ObjectMeta: metav1.ObjectMeta{Name: "nso-dfw"},
				Spec: networkingv1alpha.ServingLocationSpec{
					Topology: map[string]string{TopologyCityCodeKey: testCityCode},
				},
			},
			&locationsv1alpha1.ServingLocation{
				ObjectMeta: metav1.ObjectMeta{Name: "locations-ord"},
				Spec: locationsv1alpha1.ServingLocationSpec{
					Topology: map[string]string{TopologyCityCodeKey: testOtherCityCode},
				},
			},
		).
		Build()

	ctx := context.Background()

	fromNetworkServices, err := ListServingLocations(ctx, cl, SourceNetworkServices)
	require.NoError(t, err)
	require.Len(t, fromNetworkServices, 1)
	assert.Equal(t, "nso-dfw", fromNetworkServices[0].Name)
	assert.Equal(t, testCityCode, fromNetworkServices[0].CityCode())

	fromLocations, err := ListServingLocations(ctx, cl, SourceLocations)
	require.NoError(t, err)
	require.Len(t, fromLocations, 1)
	assert.Equal(t, "locations-ord", fromLocations[0].Name)
	assert.Equal(t, testOtherCityCode, fromLocations[0].CityCode())

	// An unset source reads what every deployment reads today.
	fromDefault, err := ListServingLocations(ctx, cl, "")
	require.NoError(t, err)
	assert.Equal(t, fromNetworkServices, fromDefault)
}

func TestServingLocationObject(t *testing.T) {
	t.Parallel()

	for _, source := range []Source{"", SourceNetworkServices} {
		object, err := ServingLocationObject(source)
		require.NoError(t, err)
		assert.IsType(t, &networkingv1alpha.ServingLocation{}, object)
	}

	object, err := ServingLocationObject(SourceLocations)
	require.NoError(t, err)
	assert.IsType(t, &locationsv1alpha1.ServingLocation{}, object)

	_, err = ServingLocationObject("Nonsense")
	require.Error(t, err)
}
