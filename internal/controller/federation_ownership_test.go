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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// TestWriteBackToUpstream_OwnerGated_NoHubDeployment asserts that write-back
// creates no copy when the hub WorkloadDeployment is absent, and treats that
// absence as the correct steady state rather than an error to retry.
func TestWriteBackToUpstream_OwnerGated_NoHubDeployment(t *testing.T) {
	t.Parallel()

	upstreamClient := fake.NewClientBuilder().
		WithScheme(newKarmadaScheme()).
		WithObjects(wbTestDownstreamNS()). // no hub WorkloadDeployment
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	r := newWriteBackReconciler(upstreamClient)

	require.NoError(t, r.writeBackToUpstream(context.Background(), wbTestCellInstance()),
		"a withdrawn deployment is a steady state, not a failure")

	var created computev1alpha.Instance
	err := upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName}, &created)
	assert.True(t, apierrors.IsNotFound(err),
		"no hub WorkloadDeployment means no write-back copy")
}

// TestWriteBackToUpstream_SetsHubControllerReference asserts a new copy is owned
// by the hub deployment that justifies its existence.
func TestWriteBackToUpstream_SetsHubControllerReference(t *testing.T) {
	t.Parallel()

	upstreamClient := fake.NewClientBuilder().
		WithScheme(newKarmadaScheme()).
		WithObjects(wbTestDownstreamNS(), wbTestHubDeployment()).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	r := newWriteBackReconciler(upstreamClient)
	require.NoError(t, r.writeBackToUpstream(context.Background(), wbTestCellInstance()))

	var created computev1alpha.Instance
	require.NoError(t, upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName}, &created))

	require.Len(t, created.OwnerReferences, 1)
	ref := created.OwnerReferences[0]
	assert.Equal(t, kindWorkloadDeployment, ref.Kind)
	assert.Equal(t, wbTestWDName, ref.Name)
	assert.Equal(t, types.UID(wbTestHubWDUID), ref.UID,
		"ownership must use the hub deployment's own UID; both objects live on the hub")
	require.NotNil(t, ref.Controller)
	assert.True(t, *ref.Controller)
}

// TestWriteBackToUpstream_AdoptsExistingCopy asserts a copy created before
// hub-local ownership existed is adopted on the next write-back, since
// owner-gated write-back will never recreate it.
func TestWriteBackToUpstream_AdoptsExistingCopy(t *testing.T) {
	t.Parallel()

	existing := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wbTestInstanceName,
			Namespace: wbTestNamespace,
			Labels:    wbTestCellInstance().Labels,
		},
		Spec: wbTestCellInstance().Spec,
	}

	upstreamClient := fake.NewClientBuilder().
		WithScheme(newKarmadaScheme()).
		WithObjects(wbTestDownstreamNS(), wbTestHubDeployment(), existing).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	r := newWriteBackReconciler(upstreamClient)
	require.NoError(t, r.writeBackToUpstream(context.Background(), wbTestCellInstance()))

	var adopted computev1alpha.Instance
	require.NoError(t, upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName}, &adopted))
	require.Len(t, adopted.OwnerReferences, 1)
	assert.Equal(t, types.UID(wbTestHubWDUID), adopted.OwnerReferences[0].UID)
}

// TestInstanceFinalize_HoldsWhenFederationConfiguredWithoutClient asserts the
// finalizer never releases without doing its work: a configured deployment with
// no client is a wiring fault, not a no-op.
func TestInstanceFinalize_HoldsWhenFederationConfiguredWithoutClient(t *testing.T) {
	t.Parallel()

	r := &InstanceReconciler{FederationConfigured: true}

	_, err := r.Finalize(context.Background(), wbTestCellInstance())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to release")
}

// TestInstanceFinalize_NoFederationConfigured asserts the one legitimate case —
// a deployment that never federates — still releases cleanly.
func TestInstanceFinalize_NoFederationConfigured(t *testing.T) {
	t.Parallel()

	r := &InstanceReconciler{}

	result, err := r.Finalize(context.Background(), wbTestCellInstance())
	require.NoError(t, err)
	assert.False(t, result.Updated)
}

// TestFederatorFinalize_HoldsWhenFederationConfiguredWithoutClient asserts the
// federator's finalizer applies the same rule to the hub WorkloadDeployment.
func TestFederatorFinalize_HoldsWhenFederationConfiguredWithoutClient(t *testing.T) {
	t.Parallel()

	r := &WorkloadDeploymentFederator{FederationConfigured: true}

	ctx := mccontext.WithCluster(context.Background(), testCluster)
	_, err := r.Finalize(ctx, testWorkloadDeployment(withFinalizer))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to release")
}

// TestFederatorFinalize_NoFederationConfigured asserts a never-federating
// deployment releases cleanly.
func TestFederatorFinalize_NoFederationConfigured(t *testing.T) {
	t.Parallel()

	r := &WorkloadDeploymentFederator{}

	ctx := mccontext.WithCluster(context.Background(), testCluster)
	result, err := r.Finalize(ctx, testWorkloadDeployment(withFinalizer))
	require.NoError(t, err)
	assert.False(t, result.Updated)
}
