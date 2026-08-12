// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/federation"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// newQuarantineProjector wires a projector with a recording event sink so the
// "reported exactly once" rule can be asserted.
func newQuarantineProjector(
	karmadaClient client.Client,
	projectClient client.Client,
) (*InstanceProjector, *capturingEventRecorder) {
	recorder := newCapturingEventRecorder(10)
	projector := newTestProjector(karmadaClient, projectClient)
	projector.Recorder = recorder
	return projector, recorder
}

// TestInstanceProjector_QuarantineReportedOnce asserts a terminal object is
// reported once and then skipped: no error, no repeated event, and a latching
// gauge entry for as long as it exists.
func TestInstanceProjector_QuarantineReportedOnce(t *testing.T) {
	t.Parallel()

	instance := projTestKarmadaInstance(map[string]string{
		downstreamclient.UpstreamOwnerClusterNameLabel: "",
	})
	karmadaClient := newKarmadaFakeClient(instance)
	projectClient := newProjectFakeClient(projTestProjectNS())

	projector, recorder := newQuarantineProjector(karmadaClient, projectClient)

	_, err := projector.Reconcile(context.Background(), projectorRequest())
	require.NoError(t, err, "a terminal state is reported, not retried")
	assert.Len(t, recorder.Recorded(), 1, "exactly one event on the first verdict")
	assert.Equal(t, 1, projector.tracker().count(), "the quarantine gauge latches")

	// Every later reconcile is a quiet skip.
	for range 3 {
		_, err := projector.Reconcile(context.Background(), projectorRequest())
		require.NoError(t, err)
	}
	assert.Len(t, recorder.Recorded(), 1, "a held quarantine must not re-report")
	assert.Equal(t, 1, projector.tracker().count())
}

// TestInstanceProjector_QuarantineInvalidatedByRepair asserts an operator who
// repairs the state that produced the verdict gets an immediate retry.
func TestInstanceProjector_QuarantineInvalidatedByRepair(t *testing.T) {
	t.Parallel()

	instance := projTestKarmadaInstance(map[string]string{
		downstreamclient.UpstreamOwnerClusterNameLabel: "",
	})
	karmadaClient := newKarmadaFakeClient(instance)
	projectClient := newProjectFakeClient(projTestProjectNS(), projTestWorkloadDeployment())

	projector, _ := newQuarantineProjector(karmadaClient, projectClient)

	_, err := projector.Reconcile(context.Background(), projectorRequest())
	require.NoError(t, err)

	// Repair the label an operator can repair.
	var quarantined computev1alpha.Instance
	require.NoError(t, karmadaClient.Get(context.Background(), projectorRequest().NamespacedName, &quarantined))
	quarantined.Labels[downstreamclient.UpstreamOwnerClusterNameLabel] = encodedCluster()
	require.NoError(t, karmadaClient.Update(context.Background(), &quarantined))

	_, err = projector.Reconcile(context.Background(), projectorRequest())
	require.NoError(t, err)

	var projection computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(), client.ObjectKey{
		Namespace: projTestProjNS, Name: projTestInstanceName,
	}, &projection), "projection resumes once the state is repaired")

	var repaired computev1alpha.Instance
	require.NoError(t, karmadaClient.Get(context.Background(), projectorRequest().NamespacedName, &repaired))
	assert.Empty(t, repaired.Annotations[computev1alpha.QuarantineReasonAnnotation],
		"a stale verdict is discarded, not carried")
	assert.Equal(t, 0, projector.tracker().count(), "the gauge follows the live set")
}

// TestInstanceProjector_MissingDeploymentIsRetryableWithinGrace asserts an
// absent project WorkloadDeployment stays a retryable ordering race while the
// object is young, keeping today's error-and-backoff behaviour.
func TestInstanceProjector_MissingDeploymentIsRetryableWithinGrace(t *testing.T) {
	t.Parallel()

	instance := projTestKarmadaInstance(nil)
	instance.CreationTimestamp = metav1.NewTime(time.Now())
	karmadaClient := newKarmadaFakeClient(instance)
	projectClient := newProjectFakeClient(projTestProjectNS())

	projector, _ := newQuarantineProjector(karmadaClient, projectClient)

	_, err := projector.Reconcile(context.Background(), projectorRequest())
	require.Error(t, err, "an ordering race is retryable")

	var untouched computev1alpha.Instance
	require.NoError(t, karmadaClient.Get(context.Background(), projectorRequest().NamespacedName, &untouched))
	assert.Empty(t, untouched.Annotations[computev1alpha.QuarantineReasonAnnotation])
}

// TestInstanceProjector_MissingDeploymentTerminalPastGrace asserts the same
// absence becomes terminal past the grace window, so a leftover can no longer
// pin the reconcile error ratio.
func TestInstanceProjector_MissingDeploymentTerminalPastGrace(t *testing.T) {
	t.Parallel()

	instance := projTestKarmadaInstance(nil)
	instance.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	karmadaClient := newKarmadaFakeClient(instance)
	projectClient := newProjectFakeClient(projTestProjectNS())

	projector, recorder := newQuarantineProjector(karmadaClient, projectClient)
	projector.GracePeriod = time.Minute

	_, err := projector.Reconcile(context.Background(), projectorRequest())
	require.NoError(t, err, "a broken invariant is reported once, not retried forever")
	assert.Len(t, recorder.Recorded(), 1)

	var quarantined computev1alpha.Instance
	require.NoError(t, karmadaClient.Get(context.Background(), projectorRequest().NamespacedName, &quarantined))
	assert.Equal(t, federation.QuarantineReasonDeploymentAbsent,
		quarantined.Annotations[computev1alpha.QuarantineReasonAnnotation])
}

// TestInstanceProjector_TerminatingObjectSkipped asserts an object on its way
// out is neither projected nor classified.
func TestInstanceProjector_TerminatingObjectSkipped(t *testing.T) {
	t.Parallel()

	instance := projTestKarmadaInstance(map[string]string{
		downstreamclient.UpstreamOwnerClusterNameLabel: "",
	})
	instance.Finalizers = []string{"test.datumapis.com/hold"}
	now := metav1.Now()
	instance.DeletionTimestamp = &now

	karmadaClient := fake.NewClientBuilder().
		WithScheme(newKarmadaScheme()).
		WithObjects(instance).
		Build()

	projector, recorder := newQuarantineProjector(karmadaClient, newProjectFakeClient(projTestProjectNS()))

	_, err := projector.Reconcile(context.Background(), projectorRequest())
	require.NoError(t, err)
	assert.Empty(t, recorder.Recorded(), "a terminating object needs no verdict")
}

// TestInstanceProjector_UnresolvableClusterStaysRetryable asserts that a
// project cluster the manager cannot resolve is never a terminal verdict.
// Engagement is asynchronous, and a project that is genuinely gone takes its
// hub objects with it — the federator finalizer removes the hub deployment and
// the hub garbage collector reclaims every copy it owns — so this reconcile has
// nothing to conclude and simply retries.
func TestInstanceProjector_UnresolvableClusterStaysRetryable(t *testing.T) {
	t.Parallel()

	instance := projTestKarmadaInstance(map[string]string{
		downstreamclient.UpstreamOwnerClusterNameLabel: EncodeClusterName("vanished-project"),
	})
	karmadaClient := newKarmadaFakeClient(instance)
	projectClient := newProjectFakeClient(projTestProjectNS())

	projector, recorder := newQuarantineProjector(karmadaClient, projectClient)

	for range 3 {
		_, err := projector.Reconcile(context.Background(), projectorRequest())
		require.Error(t, err, "an unresolvable cluster is retryable, never terminal")
	}

	var untouched computev1alpha.Instance
	require.NoError(t, karmadaClient.Get(context.Background(), projectorRequest().NamespacedName, &untouched))
	assert.Empty(t, untouched.Annotations[computev1alpha.QuarantineReasonAnnotation],
		"absence of an engaged cluster is not proof of anything")
	assert.Empty(t, recorder.Recorded())
	assert.Equal(t, 0, projector.tracker().count())
}

// TestQuarantineFingerprint_TracksIdentityOnly asserts the fingerprint follows
// the state a verdict is drawn from and ignores unrelated churn.
func TestQuarantineFingerprint_TracksIdentityOnly(t *testing.T) {
	t.Parallel()

	base := projTestKarmadaInstance(nil)
	same := base.DeepCopy()
	same.ResourceVersion = "999"
	same.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}

	assert.Equal(t, quarantineFingerprint(base), quarantineFingerprint(same),
		"status churn must not invalidate a verdict")

	changed := base.DeepCopy()
	changed.Labels[computev1alpha.WorkloadDeploymentNameLabel] = "repaired"
	assert.NotEqual(t, quarantineFingerprint(base), quarantineFingerprint(changed),
		"repairing an identity label must invalidate a verdict")
}
