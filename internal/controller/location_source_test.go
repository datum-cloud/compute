// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/locations"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

const (
	// locSourceTestCityCode / locSourceTestOtherCityCode are the cities the
	// fixtures below serve. Placement is by location name; the city is only
	// what the platform projects alongside it.
	locSourceTestCityCode      = "DFW"
	locSourceTestOtherCityCode = "ORD"
)

// newLocationsServiceScheme returns the networking scheme with the locations
// service types added, mirroring what the manager registers.
func newLocationsServiceScheme() *runtime.Scheme {
	s := newNetworkingScheme()
	_ = locationsv1alpha1.AddToScheme(s)
	return s
}

// newLocationsServiceLocation returns a Ready Location, which is the only
// kind of Location a placement may reference.
func newLocationsServiceLocation(name, cityCode string) *locationsv1alpha1.Location {
	return &locationsv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: locationsv1alpha1.LocationSpec{
			LocationClassRef: locationsv1alpha1.LocationClassReference{Name: "datum-managed"},
			Topology:         map[string]string{locations.TopologyCityCodeKey: cityCode},
		},
		Status: locationsv1alpha1.LocationStatus{
			Conditions: []metav1.Condition{{Type: locationsv1alpha1.LocationConditionReady, Status: metav1.ConditionTrue}},
		},
	}
}

func newLocationsServiceServingLocation(name, cityCode string) *locationsv1alpha1.ServingLocation {
	return &locationsv1alpha1.ServingLocation{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: locationsv1alpha1.ServingLocationSpec{
			Topology: map[string]string{locations.TopologyCityCodeKey: cityCode},
		},
	}
}

// TestGetDeploymentsForWorkload_LocationsSource verifies that a workload placed
// at a location is deployed there when the location is only known to the
// locations service, which is what the Locations source reads.
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
					Locations: []locationsv1alpha1.LocationReference{{Name: testLocationName}},
					ScaleSettings: computev1alpha.HorizontalScaleSettings{
						MinReplicas: 1,
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(newLocationsServiceScheme()).
		WithObjects(newLocationsServiceLocation(testLocationName, locSourceTestCityCode), newTestComputeAvailability(testLocationName)).
		WithIndex(&computev1alpha.WorkloadDeployment{}, deploymentWorkloadUIDIndex, deploymentWorkloadUIDIndexFunc).
		Build()

	r := &WorkloadReconciler{LocationSource: locations.SourceLocations}

	desired, orphaned, err := r.getDeploymentsForWorkload(context.Background(), cl, workload)
	require.NoError(t, err)
	require.Empty(t, orphaned)
	require.Len(t, desired, 1)
	assert.Equal(t, testLocationName, desired[0].Spec.LocationRef.Name)
}

// TestResolveLocation_LocationsSource verifies a cell reads its serving
// location from the locations service when that source is selected.
func TestResolveLocation_LocationsSource(t *testing.T) {
	t.Parallel()

	const locationName = "loc-dfw-1"

	cl := fake.NewClientBuilder().
		WithScheme(newLocationsServiceScheme()).
		WithObjects(newLocationsServiceServingLocation(locationName, locSourceTestCityCode)).
		Build()

	deployment := newLocationTestDeployment("test-wd")
	deployment.Spec.LocationRef = locationsv1alpha1.LocationReference{Name: locationName}

	r := &WorkloadDeploymentReconciler{LocationSource: locations.SourceLocations}
	result, err := r.resolveLocation(context.Background(), cl)
	require.NoError(t, err)
	result.evaluate(deployment)

	require.NotNil(t, result.reference)
	assert.Equal(t, locationName, result.reference.Name)
	assert.Empty(t, result.reason)
	assert.False(t, result.blocked)
}

// TestResolveLocation_DefaultSourceReadsLocationsService: a deployment that
// does not set the source reads the locations service.
func TestResolveLocation_DefaultSourceReadsLocationsService(t *testing.T) {
	t.Parallel()

	const locationName = "loc-dfw-1"

	cl := fake.NewClientBuilder().
		WithScheme(newLocationsServiceScheme()).
		WithObjects(newLocationsServiceServingLocation(locationName, locSourceTestCityCode)).
		Build()

	r := &WorkloadDeploymentReconciler{}
	result, err := r.resolveLocation(context.Background(), cl)
	require.NoError(t, err)

	require.NotNil(t, result.reference)
	assert.Equal(t, locationName, result.reference.Name)
}
