// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// resetSessionMetrics clears the package-level metrics, which every test in
// the package shares.
func resetSessionMetrics() {
	sessionsCreated.Reset()
	sessionsEnded.Reset()
	sessionConnectSeconds.Reset()
	sessionDurationSeconds.Reset()
	openSessionsByKey.reset()
}

// histogramSample returns one histogram series' sample count and sum.
func histogramSample(t *testing.T, observer prometheus.Observer) (uint64, float64) {
	t.Helper()
	var m dto.Metric
	require.NoError(t, observer.(prometheus.Metric).Write(&m))
	return m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum()
}

func TestInstanceConsoleSessionMetricsFollowTheLifecycle(t *testing.T) {
	resetSessionMetrics()
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)
	env.reconcile(t)

	session, ok := env.projectSession(t)
	require.True(t, ok)
	assert.Equal(t, testMemberCluster, session.Annotations[computev1alpha.InstanceConsoleSessionCellAnnotation])
	assert.Equal(t, testFederatorLocation, session.Annotations[computev1alpha.InstanceConsoleSessionLocationAnnotation])
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsCreated.WithLabelValues(testProjNS, testFederatorLocation)),
		"a session is created once however often it reconciles")
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsOpen.WithLabelValues(testProjNS, testFederatorLocation, sessionPhasePending)))

	claimOnHub(t, env)
	env.reconcile(t)
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsOpen.WithLabelValues(testProjNS, testFederatorLocation, sessionPhaseReady)))
	assert.Equal(t, 1, testutil.CollectAndCount(sessionsOpen), "an open session is in exactly one phase")

	env.now = env.now.Add(5 * time.Second)
	startOnHub(t, env)
	env.reconcile(t)
	env.reconcile(t)
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsOpen.WithLabelValues(testProjNS, testFederatorLocation, sessionPhaseConnected)))
	count, sum := histogramSample(t, sessionConnectSeconds.WithLabelValues(testFederatorLocation))
	assert.Equal(t, uint64(1), count, "the connect time is observed once")
	assert.Equal(t, 5.0, sum)

	env.now = env.now.Add(time.Minute)
	endOnHub(t, env, computev1alpha.InstanceConsoleSessionReasonCompleted, ptr.To(int32(0)))
	for range 3 {
		env.reconcile(t)
	}
	_, ok = env.projectSession(t)
	require.False(t, ok)
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsEnded.WithLabelValues(testFederatorLocation, computev1alpha.InstanceConsoleSessionReasonCompleted)),
		"an end is counted once however often the ended session reconciles")
	count, sum = histogramSample(t, sessionDurationSeconds.WithLabelValues(testFederatorLocation, computev1alpha.InstanceConsoleSessionReasonCompleted))
	assert.Equal(t, uint64(1), count)
	assert.Equal(t, 60.0, sum)
	assert.Zero(t, testutil.CollectAndCount(sessionsOpen), "an ended session is no longer open")
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsCreated.WithLabelValues(testProjNS, testFederatorLocation)))

	ended := env.events(t)[EventReasonSessionEnded]
	assert.Equal(t, testMemberCluster, ended.Annotations[computev1alpha.InstanceConsoleSessionCellAnnotation])
	assert.Equal(t, testFederatorLocation, ended.Annotations[computev1alpha.InstanceConsoleSessionLocationAnnotation])
	assert.Equal(t, "1m0s", ended.Annotations[sessionEventDurationAnnotation])
}

func TestInstanceConsoleSessionMetricsForASessionDeletedBeforeItStarted(t *testing.T) {
	resetSessionMetrics()
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconcile(t)
	require.Equal(t, 1, testutil.CollectAndCount(sessionsOpen))

	env.deleteProjectSession(t)
	env.reconcile(t)
	env.reconcile(t)

	assert.Zero(t, testutil.CollectAndCount(sessionsOpen), "a deleted session is no longer open")
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsEnded.WithLabelValues(testFederatorLocation, computev1alpha.InstanceConsoleSessionReasonRevoked)))
	assert.Zero(t, testutil.CollectAndCount(sessionDurationSeconds), "a session no client connected to has no duration")
	assert.Zero(t, testutil.CollectAndCount(sessionConnectSeconds))
	ended := env.events(t)[EventReasonSessionEnded]
	assert.Equal(t, testMemberCluster, ended.Annotations[computev1alpha.InstanceConsoleSessionCellAnnotation])
	assert.NotContains(t, ended.Annotations, sessionEventDurationAnnotation)
}

func TestInstanceConsoleSessionMetricsForAnUndeliverableSession(t *testing.T) {
	resetSessionMetrics()
	env := newSessionTestEnv(t,
		[]client.Object{testSession(), testSessionInstanceObj(), testWorkloadDeployment()},
		[]client.Object{testHubWD(testRuntimeClass)},
	)
	require.Error(t, env.reconcileErr(), "delivery retries until the deadline")

	env.now = testSessionCreated.Add(DefaultSessionClaimTimeout)
	env.reconcile(t)
	env.reconcile(t)

	assert.Zero(t, testutil.CollectAndCount(sessionsCreated), "a session never delivered was not created in any location")
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsEnded.WithLabelValues("", computev1alpha.InstanceConsoleSessionReasonUnavailable)),
		"an end before delivery has no location")
	assert.Zero(t, testutil.CollectAndCount(sessionsOpen))
}
