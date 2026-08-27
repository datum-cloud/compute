// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/locations"
)

var locationsServiceGroupVersion = schema.GroupVersion{Group: "locations.miloapis.com", Version: "v1alpha1"}

// newLocationsServiceScheme returns the networking scheme with the locations
// service kinds registered as unstructured, which is how compute reads them.
func newLocationsServiceScheme() *runtime.Scheme {
	s := newNetworkingScheme()
	for _, kind := range []string{"Location", "ServingLocation"} {
		s.AddKnownTypeWithName(locationsServiceGroupVersion.WithKind(kind), &unstructured.Unstructured{})
		s.AddKnownTypeWithName(locationsServiceGroupVersion.WithKind(kind+"List"), &unstructured.UnstructuredList{})
	}
	return s
}

func newLocationsServiceObject(kind, name, cityCode string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"topology": map[string]any{locations.TopologyCityCodeKey: cityCode},
		},
	}}
	object.SetGroupVersionKind(locationsServiceGroupVersion.WithKind(kind))
	return object
}

// TestGetDeploymentsForWorkload_LocationsSource verifies that a workload placed
// in a city is deployed there when the city is only known to the locations
// service, which is what the Locations source reads.
func TestGetDeploymentsForWorkload_LocationsSource(t *testing.T) {
	t.Parallel()

	workload := &computev1alpha.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rdTestWorkloadName,
			Namespace: testDefaultNamespace,
			UID:       types.UID("workload-uid"),
		},
		Spec: computev1alpha.WorkloadSpec{
			Placements: []computev1alpha.WorkloadPlacement{
				{
					Name:      testDefaultPlacement,
					CityCodes: []string{locTestCityCode},
					ScaleSettings: computev1alpha.HorizontalScaleSettings{
						MinReplicas: 1,
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(newLocationsServiceScheme()).
		WithObjects(newLocationsServiceObject("Location", "dfw", locTestCityCode)).
		WithIndex(&computev1alpha.WorkloadDeployment{}, deploymentWorkloadUIDIndex, deploymentWorkloadUIDIndexFunc).
		Build()

	r := &WorkloadReconciler{LocationSource: locations.SourceLocations}

	desired, orphaned, err := r.getDeploymentsForWorkload(context.Background(), cl, workload)
	require.NoError(t, err)
	require.Empty(t, orphaned)
	require.Len(t, desired, 1)
	assert.Equal(t, locTestCityCode, desired[0].Spec.CityCode)
}

// TestGetDeploymentsForWorkload_LocationsSourceIgnoresBindings verifies the
// sources do not leak into each other: LocationBindings are invisible to a
// deployment reading the locations service.
func TestGetDeploymentsForWorkload_LocationsSourceIgnoresBindings(t *testing.T) {
	t.Parallel()

	workload := &computev1alpha.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rdTestWorkloadName,
			Namespace: testDefaultNamespace,
			UID:       types.UID("workload-uid"),
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(newLocationsServiceScheme()).
		WithObjects(newTestLocationBinding("dfw", locTestCityCode)).
		WithIndex(&computev1alpha.WorkloadDeployment{}, deploymentWorkloadUIDIndex, deploymentWorkloadUIDIndexFunc).
		Build()

	r := &WorkloadReconciler{LocationSource: locations.SourceLocations}

	_, _, err := r.getDeploymentsForWorkload(context.Background(), cl, workload)
	require.ErrorContains(t, err, "no locations are registered with the system")
}

// TestResolveLocation_LocationsSource verifies a cell reads its serving
// location from the locations service when that source is selected.
func TestResolveLocation_LocationsSource(t *testing.T) {
	t.Parallel()

	const locationName = "loc-dfw-1"

	cl := fake.NewClientBuilder().
		WithScheme(newLocationsServiceScheme()).
		WithObjects(
			newLocationsServiceObject("ServingLocation", locationName, locTestCityCode),
			// The network services copy must be ignored by this source.
			newTestServingLocation("nso-"+locationName, locTestOtherCityCode),
		).
		Build()

	deployment := newLocationTestDeployment("test-wd")

	r := &WorkloadDeploymentReconciler{LocationSource: locations.SourceLocations}
	result, err := r.resolveLocation(context.Background(), cl)
	require.NoError(t, err)
	result.evaluate(deployment)

	require.NotNil(t, result.reference)
	assert.Equal(t, locationName, result.reference.Name)
	assert.Empty(t, result.reason)
	assert.False(t, result.blocked)
}

// TestResolveLocation_NetworkServicesSourceIgnoresLocationsService is the
// safety property behind the flag: a deployment that does not set it reads
// exactly what it reads today.
func TestResolveLocation_NetworkServicesSourceIgnoresLocationsService(t *testing.T) {
	t.Parallel()

	cl := fake.NewClientBuilder().
		WithScheme(newLocationsServiceScheme()).
		WithObjects(newLocationsServiceObject("ServingLocation", "loc-dfw-1", locTestCityCode)).
		Build()

	r := &WorkloadDeploymentReconciler{}
	result, err := r.resolveLocation(context.Background(), cl)
	require.NoError(t, err)

	assert.Nil(t, result.reference)
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonNoMatchingLocation, result.reason)
}
