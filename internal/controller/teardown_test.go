// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const teardownServiceName = "compute.datumapis.com"

// teardownProjectInstance builds a project-plane Instance carrying both compute
// finalizers, as one written by the cell would appear to teardown.
func teardownProjectInstance(name string) *computev1alpha.Instance {
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  testProjNS,
			Labels:     map[string]string{labelServiceName: teardownServiceName},
			Finalizers: []string{instanceControllerFinalizer, instanceQuotaFinalizer},
		},
	}
}

// TestComputeTeardown_DeletesHubCopyBeforeReleasingFinalizer asserts teardown
// honours the instance-controller finalizer instead of stripping it: the hub
// write-back copy is gone before the finalizer that guards it is released.
//
// The copy lives in the hub namespace the project namespace maps to, never in
// the project namespace, so a delete keyed by the project namespace silently
// finds nothing and leaves the copy behind — a released finalizer with its work
// undone, which is exactly the bypass this path must not take.
func TestComputeTeardown_DeletesHubCopyBeforeReleasingFinalizer(t *testing.T) {
	t.Parallel()

	instance := teardownProjectInstance(projTestInstanceName)
	projectClient := newProjectFakeClient(testProjectNamespace(), instance)

	hubCopy := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      projTestInstanceName,
			Namespace: testKarmadaNSStr,
		},
	}
	karmadaClient := newKarmadaFakeClient(hubCopy)

	teardown := NewComputeTeardown(nil, karmadaClient, projectClient.Scheme())

	require.NoError(t, teardown.TeardownConsumer(
		context.Background(), testCluster, projectClient, []string{teardownServiceName}))

	err := karmadaClient.Get(context.Background(), types.NamespacedName{
		Namespace: testKarmadaNSStr, Name: projTestInstanceName,
	}, &computev1alpha.Instance{})
	assert.True(t, apierrors.IsNotFound(err), "the hub write-back copy must be removed")

	var released computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(), client.ObjectKeyFromObject(instance), &released))
	assert.Empty(t, released.Finalizers, "both compute finalizers are released once their work is done")
}

// TestComputeTeardown_LeavesWorkloadDeploymentFinalizers asserts teardown never
// short-circuits the federator. The hub deployment is removed by the federator's
// finalizer along the ordinary deletion path; stripping finalizers here would
// let the project object vanish with its hub copy still in place.
func TestComputeTeardown_LeavesWorkloadDeploymentFinalizers(t *testing.T) {
	t.Parallel()

	wd := testWorkloadDeployment(func(wd *computev1alpha.WorkloadDeployment) {
		wd.Finalizers = []string{federatorFinalizer, workloadControllerFinalizer}
	})
	projectClient := newProjectFakeClient(testProjectNamespace(), wd)
	karmadaClient := newKarmadaFakeClient()

	teardown := NewComputeTeardown(nil, karmadaClient, projectClient.Scheme())

	require.NoError(t, teardown.TeardownConsumer(
		context.Background(), testCluster, projectClient, []string{teardownServiceName}))

	var untouched computev1alpha.WorkloadDeployment
	require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{
		Namespace: testProjNS, Name: testWDName,
	}, &untouched))
	assert.ElementsMatch(t,
		[]string{federatorFinalizer, workloadControllerFinalizer}, untouched.Finalizers,
		"teardown must not release a finalizer whose work it has not done")
}
