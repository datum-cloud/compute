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
	// locTestCityCode; locTestOtherCityCode identifies the city a mis-delivered
	// cell serves.
	locTestCityCode      = "DFW"
	locTestOtherCityCode = "ORD"

	// locTestWDNamespace is the namespace of the deployments under test.
	locTestWDNamespace = "default"
)

// newNetworkingScheme returns a scheme with compute + networkingv1alpha types.
func newNetworkingScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = computev1alpha.AddToScheme(s)
	_ = networkingv1alpha.AddToScheme(s)
	return s
}

// newTestServingLocation builds a ServingLocation fixture shaped like the one a
// cell is delivered: cluster scoped, and carrying its city under the
// topology.datum.net/city-code key.
func newTestServingLocation(name, cityCode string) *networkingv1alpha.ServingLocation {
	return &networkingv1alpha.ServingLocation{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: networkingv1alpha.ServingLocationSpec{
			Topology: map[string]string{networkingv1alpha.TopologyCityCodeKey: cityCode},
		},
	}
}

// resolvedTestLocation is the result of a cell that knows where it is and
// serves the city the deployment asked for.
func resolvedTestLocation() servingLocationResult {
	return servingLocationResult{
		reference: &networkingv1alpha.LocationReference{Name: "loc-dfw-1"},
	}
}

func newLocationTestDeployment(name string) *computev1alpha.WorkloadDeployment {
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: locTestWDNamespace},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode: locTestCityCode,
		},
	}
}

// TestResolveLocation_ExactlyOneServingLocation verifies the ordinary case: the
// single ServingLocation delivered to the cell resolves to a LocationReference
// naming it, with no namespace (Location is cluster scoped).
func TestResolveLocation_ExactlyOneServingLocation(t *testing.T) {
	t.Parallel()

	const locationName = "loc-dfw-1"

	cl := fake.NewClientBuilder().
		WithScheme(newNetworkingScheme()).
		WithObjects(newTestServingLocation(locationName, locTestCityCode)).
		Build()

	deployment := newLocationTestDeployment("test-wd")

	r := &WorkloadDeploymentReconciler{}
	result, err := r.resolveLocation(context.Background(), cl)
	require.NoError(t, err)
	result.evaluate(deployment)

	require.NotNil(t, result.reference,
		"the cell's single serving location must resolve")
	assert.Equal(t, locationName, result.reference.Name)
	assert.Empty(t, result.reference.Namespace,
		"Location is cluster scoped, so the reference carries no namespace")
	assert.Empty(t, result.reason, "a resolved location reports no blocking reason")
	assert.False(t, result.blocked)
}

// TestResolveLocation_NoServingLocation_IsNonGating verifies that a cell which
// has not been told where it is does not block the deployment: no location is
// resolved, nothing is marked blocked, and the reason names what is missing.
func TestResolveLocation_NoServingLocation_IsNonGating(t *testing.T) {
	t.Parallel()

	cl := fake.NewClientBuilder().WithScheme(newNetworkingScheme()).Build()

	deployment := newLocationTestDeployment("test-wd")

	r := &WorkloadDeploymentReconciler{}
	result, err := r.resolveLocation(context.Background(), cl)
	require.NoError(t, err, "an unidentified cell must not surface as an error")
	result.evaluate(deployment)

	assert.Nil(t, result.reference)
	assert.False(t, result.blocked,
		"a cell that has not been identified yet must never hold instances back")
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonNoMatchingLocation, result.reason)
	assert.Contains(t, result.message, networkingv1alpha.ServingLocationTopologyLabel,
		"the message must name the cluster label that fixes it")
	assert.Nil(t, deployment.Status.Location,
		"Status.Location must be left alone when nothing resolved")
}

// TestResolveLocation_MultipleServingLocations_RefusesToGuess verifies the
// ServingLocation contract: two or more delivered locations means the cell
// cannot tell which one it serves, so it picks neither and blocks.
func TestResolveLocation_MultipleServingLocations_RefusesToGuess(t *testing.T) {
	t.Parallel()

	cl := fake.NewClientBuilder().
		WithScheme(newNetworkingScheme()).
		WithObjects(
			newTestServingLocation("loc-dfw-1", locTestCityCode),
			newTestServingLocation("loc-ord-1", locTestOtherCityCode),
		).
		Build()

	deployment := newLocationTestDeployment("test-wd")

	r := &WorkloadDeploymentReconciler{}
	result, err := r.resolveLocation(context.Background(), cl)
	require.NoError(t, err)
	result.evaluate(deployment)

	assert.Nil(t, result.reference,
		"an ambiguous cell must not resolve to either candidate")
	assert.True(t, result.blocked)
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonAmbiguousServingLocation, result.reason)
	assert.Contains(t, result.message, "loc-dfw-1")
	assert.Contains(t, result.message, "loc-ord-1")
}

// TestResolveLocation_CityCodeMismatch verifies that a deployment which reaches
// a cell serving another city is reported as a placement fault rather than
// quietly stamped with the wrong location.
func TestResolveLocation_CityCodeMismatch(t *testing.T) {
	t.Parallel()

	cl := fake.NewClientBuilder().
		WithScheme(newNetworkingScheme()).
		WithObjects(newTestServingLocation("loc-ord-1", locTestOtherCityCode)).
		Build()

	deployment := newLocationTestDeployment("test-wd")

	r := &WorkloadDeploymentReconciler{}
	result, err := r.resolveLocation(context.Background(), cl)
	require.NoError(t, err)
	result.evaluate(deployment)

	assert.Nil(t, result.reference,
		"the wrong cell's location must never be stamped on the deployment")
	assert.True(t, result.blocked, "a misplaced deployment must not proceed silently")
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonCityCodeMismatch, result.reason)
	assert.Contains(t, result.message, locTestCityCode)
	assert.Contains(t, result.message, locTestOtherCityCode)
}

// newLocationTestWDReconciler builds a WorkloadDeploymentReconciler with
// networking enabled, wired to a fake cluster, with the controller finalizer
// pre-registered the same way SetupWithManager does. Networking must be enabled
// so Reconcile exercises location resolution.
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

// newLocationTestReconcilableWD builds a deployment shaped the way Reconcile
// expects to find one already running: finalized, with replicas and the
// defaulted management policy.
func newLocationTestReconcilableWD(name string) *computev1alpha.WorkloadDeployment {
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: locTestWDNamespace,
			UID:       types.UID(name + "-uid"),
			// Pre-set the finalizer so Reconcile proceeds past the finalizer-add
			// branch.
			Finalizers: []string{workloadControllerFinalizer},
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode:    locTestCityCode,
			WorkloadRef: computev1alpha.WorkloadReference{Name: "location-test-workload"},
			Replicas:    new(int32(1)),
			ScaleSettings: computev1alpha.HorizontalScaleSettings{
				MinReplicas: 1,
				// Production deployments always carry the kubebuilder-defaulted
				// policy; without it the instance-control strategy emits no actions.
				InstanceManagementPolicy: computev1alpha.OrderedReadyInstanceManagementPolicyType,
			},
		},
	}
}

// newLocationTestInstance builds an instance shaped the way the instance-control
// strategy creates it: ordinal name, deployment UID label, and the scheduling
// gates stamped at creation. The CreationTimestamp (which the fake client does
// not stamp on Create) keeps the strategy in its wait path.
func newLocationTestInstance(deployment *computev1alpha.WorkloadDeployment) *computev1alpha.Instance {
	return &computev1alpha.Instance{
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
}

func locationTestRequest(deployment *computev1alpha.WorkloadDeployment) mcreconcile.Request {
	return mcreconcile.Request{
		ClusterName: testCluster,
		Request: ctrl.Request{
			NamespacedName: types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace},
		},
	}
}

// TestWorkloadDeploymentReconcile_UnidentifiedCell_SetsCondition verifies the
// user-visible surface while a cell has not learned where it is: the Available
// condition explains what is missing, and once the cell is told, the next
// reconcile resolves the location and replaces the reason — the waiting signal
// must not outlive its cause.
func TestWorkloadDeploymentReconcile_UnidentifiedCell_SetsCondition(t *testing.T) {
	t.Parallel()

	deployment := newLocationTestReconcilableWD("location-test-wd")
	instance := newLocationTestInstance(deployment)

	cl := fake.NewClientBuilder().
		WithScheme(newNetworkingScheme()).
		WithObjects(deployment, instance).
		WithStatusSubresource(deployment).
		Build()
	r := newLocationTestWDReconciler(cl)
	req := locationTestRequest(deployment)

	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var updated computev1alpha.WorkloadDeployment
	require.NoError(t, cl.Get(context.Background(), req.NamespacedName, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.WorkloadDeploymentAvailable)
	require.NotNil(t, cond, "Available must be set while the cell has no location")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonNoMatchingLocation, cond.Reason)
	assert.Contains(t, cond.Message, networkingv1alpha.ServingLocationTopologyLabel,
		"the condition message must name the label that identifies the cell")
	assert.Nil(t, updated.Status.Location)

	// Deliver the cell's location; the next reconcile resolves it and must
	// replace the waiting reason.
	servingLocation := newTestServingLocation("loc-dfw-2", locTestCityCode)
	require.NoError(t, cl.Create(context.Background(), servingLocation))

	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	require.NoError(t, cl.Get(context.Background(), req.NamespacedName, &updated))
	cond = apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.WorkloadDeploymentAvailable)
	require.NotNil(t, cond)
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonInstancesProvisioning, cond.Reason,
		"the waiting reason must give way once the cell knows where it is")
	require.NotNil(t, updated.Status.Location)
	assert.Equal(t, servingLocation.Name, updated.Status.Location.Name)
}

// TestWorkloadDeploymentReconcile_CityCodeMismatch_HoldsInstances verifies that a
// deployment delivered to the wrong cell reports the fault and is kept out of
// service: its instances keep the Network scheduling gate rather than booting in
// a city the user did not ask for.
func TestWorkloadDeploymentReconcile_CityCodeMismatch_HoldsInstances(t *testing.T) {
	t.Parallel()

	deployment := newLocationTestReconcilableWD("mismatch-test-wd")
	instance := newLocationTestInstance(deployment)

	cl := fake.NewClientBuilder().
		WithScheme(newNetworkingScheme()).
		WithObjects(deployment, instance, newTestServingLocation("loc-ord-1", locTestOtherCityCode)).
		WithStatusSubresource(deployment).
		Build()
	r := newLocationTestWDReconciler(cl)
	req := locationTestRequest(deployment)

	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var updated computev1alpha.WorkloadDeployment
	require.NoError(t, cl.Get(context.Background(), req.NamespacedName, &updated))

	cond := apimeta.FindStatusCondition(updated.Status.Conditions, computev1alpha.WorkloadDeploymentAvailable)
	require.NotNil(t, cond)
	assert.Equal(t, computev1alpha.WorkloadDeploymentReasonCityCodeMismatch, cond.Reason,
		"a misplaced deployment must report the placement fault over any other blocker")
	assert.Nil(t, updated.Status.Location,
		"the wrong cell's location must never be written to status")

	var updatedInstance computev1alpha.Instance
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
		Name: instance.Name, Namespace: instance.Namespace,
	}, &updatedInstance))
	require.NotNil(t, updatedInstance.Spec.Controller)
	assert.Contains(t, updatedInstance.Spec.Controller.SchedulingGates,
		computev1alpha.SchedulingGate{Name: instancecontrol.NetworkSchedulingGate.String()},
		"the Network gate must stay held while the deployment is on the wrong cell")
}

// TestWorkloadDeploymentReconcile_BackfillsInstanceLocation verifies that an
// instance created before the cell knew its location does not stay without one:
// the reconcile that resolves the location also stamps it on the existing
// instance.
func TestWorkloadDeploymentReconcile_BackfillsInstanceLocation(t *testing.T) {
	t.Parallel()

	deployment := newLocationTestReconcilableWD("backfill-test-wd")
	instance := newLocationTestInstance(deployment)
	require.Nil(t, instance.Spec.Location, "the fixture must start without a location")

	servingLocation := newTestServingLocation("loc-dfw-1", locTestCityCode)

	cl := fake.NewClientBuilder().
		WithScheme(newNetworkingScheme()).
		WithObjects(deployment, instance, servingLocation).
		WithStatusSubresource(deployment).
		Build()
	r := newLocationTestWDReconciler(cl)

	_, err := r.Reconcile(context.Background(), locationTestRequest(deployment))
	require.NoError(t, err)

	var updatedInstance computev1alpha.Instance
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
		Name: instance.Name, Namespace: instance.Namespace,
	}, &updatedInstance))

	require.NotNil(t, updatedInstance.Spec.Location,
		"an instance predating the cell's location must be backfilled")
	assert.Equal(t, servingLocation.Name, updatedInstance.Spec.Location.Name)
}

// TestEnqueueWorkloadDeploymentsForServingLocation verifies the ServingLocation
// watch mapping: the cell's identity applies to every deployment on it, so all
// of them are enqueued regardless of the city they target.
func TestEnqueueWorkloadDeploymentsForServingLocation(t *testing.T) {
	t.Parallel()

	wdDFW := newLocationTestDeployment("wd-dfw")
	wdORD := newLocationTestDeployment("wd-ord")
	wdORD.Spec.CityCode = locTestOtherCityCode

	cl := fake.NewClientBuilder().
		WithScheme(newNetworkingScheme()).
		WithObjects(wdDFW, wdORD).
		Build()

	requests := enqueueWorkloadDeploymentsForServingLocation(context.Background(), cl, testCluster)
	require.Len(t, requests, 2)

	names := []string{requests[0].Name, requests[1].Name}
	assert.ElementsMatch(t, []string{wdDFW.Name, wdORD.Name}, names)
	assert.Equal(t, locTestWDNamespace, requests[0].Namespace)
	assert.Equal(t, multicluster.ClusterName(testCluster), requests[0].ClusterName)
}
