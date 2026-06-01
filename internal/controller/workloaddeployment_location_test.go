// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// newNetworkingScheme returns a scheme with compute + networkingv1alpha types.
func newNetworkingScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = computev1alpha.AddToScheme(s)
	_ = networkingv1alpha.AddToScheme(s)
	return s
}

// TestReconcileNetworks_PersistsLocation_WhenLocationFound verifies that when a
// Location object matching the deployment's city code exists in the cluster, the
// resolved LocationReference is returned by reconcileNetworks and can be persisted
// to deployment.Status.Location. Instance creation must not be blocked — the
// function returns networkReady=false only because no NetworkInterfaces exist on
// the deployment in this scenario (short-circuit before bindings), not because
// Location was absent.
func TestReconcileNetworks_PersistsLocation_WhenLocationFound(t *testing.T) {
	t.Parallel()

	const locationName = "loc-dfw-1"
	const locationNamespace = "networking-system"

	location := &networkingv1alpha.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name:      locationName,
			Namespace: locationNamespace,
		},
		Spec: networkingv1alpha.LocationSpec{
			Topology: map[string]string{
				"topology.datum.net/city-code": wbTestCityCode,
			},
		},
	}

	s := newNetworkingScheme()
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(location).Build()

	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: wdControllerTestName, Namespace: gcTestProjectNamespace},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode: wbTestCityCode,
			// No NetworkInterfaces — the function returns false,locationRef,nil
			// after the location is found but before bindings are checked.
		},
	}

	r := &WorkloadDeploymentReconciler{}
	_, resolvedLocation, err := r.reconcileNetworks(context.Background(), cl, deployment)

	require.NoError(t, err)
	require.NotNil(t, resolvedLocation,
		"resolved location must be non-nil when a matching Location object exists")
	assert.Equal(t, locationName, resolvedLocation.Name)
	assert.Equal(t, locationNamespace, resolvedLocation.Namespace)

	// Simulate what the Reconcile loop does: persist resolvedLocation to Status.
	deployment.Status.Location = resolvedLocation
	assert.Equal(t, locationName, deployment.Status.Location.Name,
		"Status.Location.Name must match the resolved Location object name")
}

// TestReconcileNetworks_ReturnsNilLocation_WhenNoLocationFound verifies that
// when no Location object in the cluster matches the deployment's city code,
// reconcileNetworks returns (false, nil, nil) — no error and no resolved
// location. The caller must treat nil location as best-effort and must NOT block
// instance creation.
func TestReconcileNetworks_ReturnsNilLocation_WhenNoLocationFound(t *testing.T) {
	t.Parallel()

	s := newNetworkingScheme()
	// Cluster has a Location for a DIFFERENT city code.
	otherLocation := &networkingv1alpha.Location{
		ObjectMeta: metav1.ObjectMeta{Name: "loc-ord-1", Namespace: "networking-system"},
		Spec: networkingv1alpha.LocationSpec{
			Topology: map[string]string{
				"topology.datum.net/city-code": "ORD",
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(otherLocation).Build()

	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: wdControllerTestName, Namespace: gcTestProjectNamespace},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode: wbTestCityCode, // no matching Location
		},
	}

	r := &WorkloadDeploymentReconciler{}
	networkReady, resolvedLocation, err := r.reconcileNetworks(context.Background(), cl, deployment)

	require.NoError(t, err, "missing location must not cause an error")
	assert.False(t, networkReady, "network is not ready when no location is found")
	assert.Nil(t, resolvedLocation,
		"resolved location must be nil when no matching Location object exists")

	// Status.Location remains nil — callers must not update it in this case.
	// Confirm the deployment's Status.Location is unaffected (nil → nil).
	assert.Nil(t, deployment.Status.Location,
		"Status.Location must remain nil when no Location matches the city code")
}
