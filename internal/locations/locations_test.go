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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	testCityCode      = "DFW"
	testOtherCityCode = "ORD"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	require.NoError(t, networkingv1alpha.AddToScheme(s))

	for _, gvk := range []schema.GroupVersionKind{locationGVK, servingLocationGVK} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}

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

func newUnstructuredLocation(gvk schema.GroupVersionKind, name string, topology map[string]any) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name},
		"spec":     map[string]any{"topology": topology},
	}}
	object.SetGroupVersionKind(gvk)
	return object
}

func cityTopology(cityCode string) map[string]any {
	return map[string]any{TopologyCityCodeKey: cityCode}
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
			// The Locations source must not be read when network services is
			// selected.
			newUnstructuredLocation(locationGVK, "lhr", cityTopology("LHR")),
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
			newUnstructuredLocation(locationGVK, "dfw", cityTopology(testCityCode)),
			newUnstructuredLocation(locationGVK, "ord", cityTopology(testOtherCityCode)),
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

// TestListPlacementLocations_KindNotInstalled covers a control plane that
// serves only the kinds its consumers read: the locations service kinds are
// absent, which must read as no locations rather than failing the caller.
func TestListPlacementLocations_KindNotInstalled(t *testing.T) {
	t.Parallel()

	cl := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
				return &apimeta.NoKindMatchError{GroupKind: locationGVK.GroupKind()}
			},
		}).
		Build()

	found, err := ListPlacementLocations(context.Background(), cl, SourceLocations)
	require.NoError(t, err)
	assert.Empty(t, found)
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
			newUnstructuredLocation(servingLocationGVK, "locations-ord", cityTopology(testOtherCityCode)),
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
	require.IsType(t, &unstructured.Unstructured{}, object)
	assert.Equal(t, servingLocationGVK, object.GetObjectKind().GroupVersionKind())

	_, err = ServingLocationObject("Nonsense")
	require.Error(t, err)
}
