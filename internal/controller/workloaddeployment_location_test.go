// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"

	"go.datum.net/compute/internal/controller/instancecontrol"
)

const (
	// locTestCityCode / locTestOtherCityCode: deployments under test target
	// locTestCityCode; locTestOtherCityCode identifies a decoy Location that
	// must never match.
	locTestCityCode      = "DFW"
	locTestOtherCityCode = "ORD"

	// locTestNamespace mirrors where Location objects live in real clusters.
	locTestNamespace = "networking-system"

	// locTestWDNamespace is the namespace of the deployments under test.
	locTestWDNamespace = "default"

	// locTestTopologyKey is the production topology key that carries a
	// Location's city code.
	locTestTopologyKey = "topology.datum.net/city-code"
)

// newNetworkingScheme returns a scheme with compute + networkingv1alpha types.
func newNetworkingScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = computev1alpha.AddToScheme(s)
	_ = networkingv1alpha.AddToScheme(s)
	return s
}

// newTestLocation builds a Location fixture shaped like production: the city
// code is carried in Spec.Topology under the topology.datum.net/city-code key.
func newTestLocation(name, cityCode string) *networkingv1alpha.Location {
	return &networkingv1alpha.Location{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: locTestNamespace},
		Spec: networkingv1alpha.LocationSpec{
			Topology: map[string]string{locTestTopologyKey: cityCode},
		},
	}
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

	location := newTestLocation(locationName, locTestCityCode)

	s := newNetworkingScheme()
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(location).Build()

	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "test-wd", Namespace: locTestWDNamespace},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode: locTestCityCode,
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
	assert.Equal(t, locTestNamespace, resolvedLocation.Namespace)

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
	otherLocation := newTestLocation("loc-ord-1", locTestOtherCityCode)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(otherLocation).Build()

	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "test-wd", Namespace: locTestWDNamespace},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode: locTestCityCode, // no matching Location
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

// newLocationTestWDReconciler builds a WorkloadDeploymentReconciler with
// networking enabled, wired to a fake cluster, with the controller finalizer
// pre-registered the same way SetupWithManager does. Networking must be enabled
// so Reconcile exercises Location resolution.
func newLocationTestWDReconciler(cl client.Client) *WorkloadDeploymentReconciler {
	r := &WorkloadDeploymentReconciler{
		mgr:               newFakeMCManager(testCluster, newFakeCluster(cl)),
		NetworkingEnabled: true,
	}
	feds := finalizer.NewFinalizers()
	if err := feds.Register(workloadControllerFinalizer, r); err != nil {
		panic("failed to register test finalizer: " + err.Error())
	}
	r.finalizers = feds
	return r
}

// TestWorkloadDeploymentReconcile_NoMatchingLocation_SetsCondition verifies the
// user-visible surface while a deployment waits for its city's Location: the
// Available condition must name the unresolved city (reason NoMatchingLocation),
// and once a matching Location appears the next reconcile must replace that
// reason — the unresolved-city signal must not outlive its cause.
func TestWorkloadDeploymentReconcile_NoMatchingLocation_SetsCondition(t *testing.T) {
	t.Parallel()

	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "location-test-wd",
			Namespace: locTestWDNamespace,
			UID:       "location-test-wd-uid",
			// Pre-set the finalizer so Reconcile proceeds past the finalizer-add
			// branch.
			Finalizers: []string{workloadControllerFinalizer},
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode:    locTestCityCode,
			WorkloadRef: computev1alpha.WorkloadReference{Name: "location-test-workload"},
			ScaleSettings: computev1alpha.HorizontalScaleSettings{
				MinReplicas: 1,
				// Production deployments always carry the kubebuilder-defaulted
				// policy; without it the instance-control strategy emits no actions.
				InstanceManagementPolicy: computev1alpha.OrderedReadyInstanceManagementPolicyType,
			},
		},
	}

	// An instance shaped the way the instance-control strategy creates it:
	// ordinal name, controller labels, and the scheduling gates stamped at
	// creation. Pre-seeding it (with a CreationTimestamp, which the fake client
	// does not stamp on Create) keeps the strategy in its wait path so the test
	// exercises only the condition transitions.
	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:              deployment.Name + "-0",
			Namespace:         deployment.Namespace,
			CreationTimestamp: metav1.Now(),
			Labels: map[string]string{
				computev1alpha.WorkloadDeploymentUIDLabel: string(deployment.UID),
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Controller: &computev1alpha.InstanceController{
				SchedulingGates: []computev1alpha.SchedulingGate{
					{Name: instancecontrol.NetworkSchedulingGate.String()},
					{Name: instancecontrol.QuotaSchedulingGate.String()},
				},
			},
		},
	}

	// The only Location in the cluster serves a different city.
	otherLocation := newTestLocation("loc-ord-1", locTestOtherCityCode)

	cl := fake.NewClientBuilder().
		WithScheme(newNetworkingScheme()).
		WithObjects(deployment, instance, otherLocation).
		WithStatusSubresource(deployment).
		Build()
	r := newLocationTestWDReconciler(cl)

	req := mcreconcile.Request{
		ClusterName: testCluster,
		Request: ctrl.Request{
			NamespacedName: types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace},
		},
	}

	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var updated computev1alpha.WorkloadDeployment
	require.NoError(t, cl.Get(context.Background(), req.NamespacedName, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.WorkloadDeploymentAvailable)
	require.NotNil(t, cond, "Available must be set while the city has no Location")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "NoMatchingLocation", cond.Reason)
	assert.Contains(t, cond.Message, locTestCityCode,
		"the condition message must name the unresolved city code")
	assert.Nil(t, updated.Status.Location)

	// Provision the city's Location; the next reconcile resolves it and must
	// replace the NoMatchingLocation reason.
	matchingLocation := newTestLocation("loc-dfw-2", locTestCityCode)
	require.NoError(t, cl.Create(context.Background(), matchingLocation))

	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	require.NoError(t, cl.Get(context.Background(), req.NamespacedName, &updated))
	cond = apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.WorkloadDeploymentAvailable)
	require.NotNil(t, cond)
	assert.Equal(t, "ProvisioningInstances", cond.Reason,
		"the unresolved-city reason must give way once the Location resolves")
	require.NotNil(t, updated.Status.Location)
	assert.Equal(t, matchingLocation.Name, updated.Status.Location.Name)
}

// TestEnqueueWorkloadDeploymentsForLocation verifies the Location watch mapping:
// a Location event must enqueue exactly the WorkloadDeployments whose CityCode
// matches the Location's topology (via deploymentCityCodeIndex), and a Location
// without a city code in its topology must map to nothing.
func TestEnqueueWorkloadDeploymentsForLocation(t *testing.T) {
	t.Parallel()

	wdDFW := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "wd-dfw", Namespace: locTestWDNamespace},
		Spec:       computev1alpha.WorkloadDeploymentSpec{CityCode: locTestCityCode},
	}
	wdORD := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "wd-ord", Namespace: locTestWDNamespace},
		Spec:       computev1alpha.WorkloadDeploymentSpec{CityCode: locTestOtherCityCode},
	}

	cl := fake.NewClientBuilder().
		WithScheme(newNetworkingScheme()).
		WithIndex(&computev1alpha.WorkloadDeployment{}, deploymentCityCodeIndex, deploymentCityCodeIndexFunc).
		WithObjects(wdDFW, wdORD).
		Build()

	location := newTestLocation("loc-dfw-1", locTestCityCode)

	requests := enqueueWorkloadDeploymentsForLocation(context.Background(), cl, testCluster, location)
	require.Len(t, requests, 1, "only deployments whose CityCode matches the Location must be enqueued")
	assert.Equal(t, wdDFW.Name, requests[0].Name)
	assert.Equal(t, locTestWDNamespace, requests[0].Namespace)
	assert.Equal(t, multicluster.ClusterName(testCluster), requests[0].ClusterName)

	// A Location without a city code in its topology identifies no city, so no
	// deployment can match it.
	noCityLocation := &networkingv1alpha.Location{
		ObjectMeta: metav1.ObjectMeta{Name: "loc-no-city", Namespace: locTestNamespace},
		Spec:       networkingv1alpha.LocationSpec{Topology: map[string]string{}},
	}
	assert.Empty(t, enqueueWorkloadDeploymentsForLocation(context.Background(), cl, testCluster, noCityLocation))
}
