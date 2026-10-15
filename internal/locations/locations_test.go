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
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

const (
	testCityCode      = "DFW"
	testOtherCityCode = "ORD"

	testLocationORD  = "ord"
	testLocationDFWA = "dfw-a"
	testLocationDFWB = "dfw-b"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	require.NoError(t, networkingv1alpha.AddToScheme(s))
	require.NoError(t, locationsv1alpha1.AddToScheme(s))
	require.NoError(t, servicesv1alpha1.AddToScheme(s))
	return s
}

// newAvailability returns the mirrored record saying the named service is
// deployed at the named location, Available or not.
func newAvailability(service, location string, available bool) *servicesv1alpha1.ServiceAvailability {
	status := metav1.ConditionFalse
	if available {
		status = metav1.ConditionTrue
	}
	return &servicesv1alpha1.ServiceAvailability{
		ObjectMeta: metav1.ObjectMeta{Name: service + "--" + location},
		Spec: servicesv1alpha1.ServiceAvailabilitySpec{
			ServiceRef:  servicesv1alpha1.ServiceRef{Name: service},
			LocationRef: servicesv1alpha1.LocationRef{Name: location},
		},
		Status: servicesv1alpha1.ServiceAvailabilityStatus{
			Conditions: []metav1.Condition{{Type: "Available", Status: status}},
		},
	}
}

// newComputeAvailability returns an Available record for compute at the
// location.
func newComputeAvailability(location string) *servicesv1alpha1.ServiceAvailability {
	return newAvailability(ComputeServiceName, location, true)
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

// newReadyLocation is newLocation with its Ready condition set.
func newReadyLocation(name, cityCode string) *locationsv1alpha1.Location {
	location := newLocation(name, cityCode)
	location.Status.Conditions = []metav1.Condition{{Type: locationsv1alpha1.LocationConditionReady, Status: metav1.ConditionTrue}}
	return location
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
			newBinding(testLocationORD, testOtherCityCode),
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
			newLocation(testLocationORD, testOtherCityCode),
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

// TestEnsureServingLocationKind_UnknownSource keeps an unreadable config from
// reaching the REST mapper at all.
func TestEnsureServingLocationKind_UnknownSource(t *testing.T) {
	t.Parallel()

	require.Error(t, EnsureServingLocationKind(apimeta.NewDefaultRESTMapper(nil), "Nonsense"))
}

func TestServingLocationGVK(t *testing.T) {
	t.Parallel()

	for _, source := range []Source{"", SourceNetworkServices} {
		gvk, err := ServingLocationGVK(source)
		require.NoError(t, err)
		assert.Equal(t, networkingv1alpha.GroupVersion.WithKind("ServingLocation"), gvk)
		assert.Equal(t, "servinglocations.networking.datumapis.com", crdName(gvk))
	}

	gvk, err := ServingLocationGVK(SourceLocations)
	require.NoError(t, err)
	assert.Equal(t, locationsv1alpha1.GroupVersion.WithKind("ServingLocation"), gvk)
	assert.Equal(t, "servinglocations.locations.miloapis.com", crdName(gvk))

	_, err = ServingLocationGVK("Nonsense")
	require.Error(t, err)
}

// TestSelect covers selection over topology: a selector matches Ready
// locations by their topology, the result is ordered by name, and an empty or
// malformed selector is refused rather than matching everything.
func TestSelect(t *testing.T) {
	t.Parallel()

	region := "topology.datum.net/region"
	found := []PlacementLocation{
		{Name: testLocationORD, Topology: map[string]string{TopologyCityCodeKey: testOtherCityCode, region: "us-central"}, Ready: true, ServiceAvailable: true},
		{Name: testLocationDFWB, Topology: map[string]string{TopologyCityCodeKey: testCityCode, region: "us-south"}, Ready: true, ServiceAvailable: true},
		{Name: testLocationDFWA, Topology: map[string]string{TopologyCityCodeKey: testCityCode, region: "us-south"}, Ready: true, ServiceAvailable: true},
		{Name: "dfw-down", Topology: map[string]string{TopologyCityCodeKey: testCityCode}, Ready: false},
		{Name: "nowhere", Ready: true, ServiceAvailable: true},
	}

	names := func(locations []PlacementLocation) []string {
		out := make([]string, 0, len(locations))
		for _, location := range locations {
			out = append(out, location.Name)
		}
		return out
	}

	t.Run("match labels, sorted, Ready only", func(t *testing.T) {
		t.Parallel()
		matched, err := Select(found, &metav1.LabelSelector{
			MatchLabels: map[string]string{TopologyCityCodeKey: testCityCode},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{testLocationDFWA, testLocationDFWB}, names(matched))
	})

	t.Run("match expressions", func(t *testing.T) {
		t.Parallel()
		matched, err := Select(found, &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: region, Operator: metav1.LabelSelectorOpIn, Values: []string{"us-central", "eu-west"},
			}},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{testLocationORD}, names(matched))
	})

	t.Run("exists selects every location with the key", func(t *testing.T) {
		t.Parallel()
		matched, err := Select(found, &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: TopologyCityCodeKey, Operator: metav1.LabelSelectorOpExists,
			}},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{testLocationDFWA, testLocationDFWB, testLocationORD}, names(matched))
	})

	t.Run("no match is empty, not an error", func(t *testing.T) {
		t.Parallel()
		matched, err := Select(found, &metav1.LabelSelector{
			MatchLabels: map[string]string{TopologyCityCodeKey: "LHR"},
		})
		require.NoError(t, err)
		assert.Empty(t, matched)
	})

	t.Run("empty selector is refused", func(t *testing.T) {
		t.Parallel()
		_, err := Select(found, &metav1.LabelSelector{})
		require.Error(t, err)
		_, err = Select(found, nil)
		require.Error(t, err)
	})

	t.Run("malformed selector is refused", func(t *testing.T) {
		t.Parallel()
		_, err := Select(found, &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: TopologyCityCodeKey, Operator: metav1.LabelSelectorOpIn,
			}},
		})
		require.Error(t, err)
	})
}

// TestPlacementLocationObject pins which kind each source watches for the
// locations a project may place at.
func TestPlacementLocationObject(t *testing.T) {
	t.Parallel()

	obj, err := PlacementLocationObject("")
	require.NoError(t, err)
	assert.IsType(t, &networkingv1alpha.LocationBinding{}, obj)

	obj, err = PlacementLocationObject(SourceLocations)
	require.NoError(t, err)
	assert.IsType(t, &locationsv1alpha1.Location{}, obj)

	_, err = PlacementLocationObject("Nonsense")
	require.Error(t, err)
}

// TestListPlacementLocations_ServiceAvailability covers the availability gate
// under both sources: a location is placeable only when a record for compute
// names it and reports Available. A record for another service, or one that
// is not Available, leaves the location Ready but not placeable.
func TestListPlacementLocations_ServiceAvailability(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		source  Source
		objects []client.Object
	}{
		{
			name:   "network services",
			source: SourceNetworkServices,
			objects: []client.Object{
				newBinding(testLocationDFWA, testCityCode),
				newBinding(testLocationORD, testOtherCityCode),
				newBinding("lhr", "LHR"),
			},
		},
		{
			name:   "locations service",
			source: SourceLocations,
			objects: []client.Object{
				newReadyLocation(testLocationDFWA, testCityCode),
				newReadyLocation(testLocationORD, testOtherCityCode),
				newReadyLocation("lhr", "LHR"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := append(tt.objects,
				newComputeAvailability(testLocationDFWA),
				newAvailability(ComputeServiceName, testLocationORD, false),
				newAvailability("networking-datumapis-com", "lhr", true),
			)
			cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objects...).Build()

			found, err := ListPlacementLocations(context.Background(), cl, tt.source)
			require.NoError(t, err)
			require.Len(t, found, 3, "availability narrows what is placeable, not what is listed")

			byName := map[string]PlacementLocation{}
			for _, location := range found {
				byName[location.Name] = location
			}
			assert.True(t, byName[testLocationDFWA].Placeable(), "an Available compute record makes the location placeable")
			assert.False(t, byName[testLocationORD].Placeable(), "a compute record that is not Available does not")
			assert.False(t, byName["lhr"].Placeable(), "another service's availability says nothing about compute")
			assert.Equal(t, []string{testLocationDFWA}, sets.List(PlaceableNames(found)))
		})
	}
}
