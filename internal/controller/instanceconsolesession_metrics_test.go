// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// resetSessionMetrics clears the package-level metrics, which every test in
// the package shares.
func resetSessionMetrics() {
	sessionsCreated.Reset()
	sessionsEnded.Reset()
	sessionConnectSeconds.Reset()
	sessionDurationSeconds.Reset()
	activityEvents.Reset()
	openSessionsByKey.reset()
	sessionsOpen.Reset()
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
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsCreated.WithLabelValues(testCluster, testFederatorLocation)),
		"a session is created once however often it reconciles")
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsOpen.WithLabelValues(testCluster, testFederatorLocation, sessionPhasePending)))

	claimOnHub(t, env)
	env.reconcile(t)
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsOpen.WithLabelValues(testCluster, testFederatorLocation, sessionPhaseReady)))
	assert.Equal(t, 1, testutil.CollectAndCount(sessionsOpen), "an open session is in exactly one phase")

	env.now = env.now.Add(5 * time.Second)
	startOnHub(t, env)
	env.reconcile(t)
	env.reconcile(t)
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsOpen.WithLabelValues(testCluster, testFederatorLocation, sessionPhaseConnected)))
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
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsCreated.WithLabelValues(testCluster, testFederatorLocation)))

	ended := env.events(t)[EventReasonSessionEnded]
	assert.Equal(t, testMemberCluster, ended.Annotations[computev1alpha.InstanceConsoleSessionCellAnnotation])
	assert.Equal(t, testFederatorLocation, ended.Annotations[computev1alpha.InstanceConsoleSessionLocationAnnotation])
	assert.Equal(t, "1m0s", ended.Annotations[sessionEventDurationAnnotation])

	for _, event := range []string{EventReasonSessionStarted, EventReasonSessionEnded} {
		assert.Equal(t, 1.0, testutil.ToFloat64(activityEvents.WithLabelValues(event, activityEventRecorded)),
			"%s is recorded once however often the session reconciles", event)
	}
	assert.Equal(t, 2, testutil.CollectAndCount(activityEvents), "no attempt failed or was retried")
}

// sessionEnvInProject is a deliverable session env whose project is the
// project cluster named project. Every env's session lives in the same
// namespace, as every project's sessions live in its default namespace.
func sessionEnvInProject(t *testing.T, project string) *sessionTestEnv {
	t.Helper()
	env := newDeliverableSessionEnv(t, testRuntimeClass)
	env.reconciler.mgr = newFakeMCManager(project, newFakeCluster(env.project))
	hubNS := testHubNamespace()
	hubNS.Labels[downstreamclient.UpstreamOwnerClusterNameLabel] = EncodeClusterName(project)
	require.NoError(t, env.hub.Update(context.Background(), hubNS))
	return env
}

func reconcileInProject(t *testing.T, env *sessionTestEnv, project string) {
	t.Helper()
	_, err := env.reconciler.Reconcile(context.Background(), mcreconcile.Request{
		ClusterName: multicluster.ClusterName(project),
		Request: ctrl.Request{NamespacedName: types.NamespacedName{
			Namespace: testProjNS,
			Name:      testSessionName,
		}},
	})
	require.NoError(t, err)
}

// TestInstanceConsoleSessionMetricsLabelTheProject asserts the project label
// names the project, not the namespace in it: two projects whose sessions live
// in namespaces of the same name are two series of each metric.
func TestInstanceConsoleSessionMetricsLabelTheProject(t *testing.T) {
	resetSessionMetrics()
	projects := []string{"project-a", "project-b"}
	envs := map[string]*sessionTestEnv{}
	for _, project := range projects {
		envs[project] = sessionEnvInProject(t, project)
		reconcileInProject(t, envs[project], project)
		reconcileInProject(t, envs[project], project)
	}

	assert.Equal(t, 2, testutil.CollectAndCount(sessionsCreated), "one created series for each project")
	assert.Equal(t, 2, testutil.CollectAndCount(sessionsOpen), "one open series for each project")
	for _, project := range projects {
		assert.Equal(t, 1.0, testutil.ToFloat64(sessionsCreated.WithLabelValues(project, testFederatorLocation)),
			"%s created one session", project)
		assert.Equal(t, 1.0, testutil.ToFloat64(sessionsOpen.WithLabelValues(project, testFederatorLocation, sessionPhasePending)),
			"%s holds one open session, under the same project value as its counter", project)
	}

	// One project's session goes; the other project's stays open.
	envs["project-a"].deleteProjectSession(t)
	reconcileInProject(t, envs["project-a"], "project-a")
	reconcileInProject(t, envs["project-a"], "project-a")
	assert.Equal(t, 1, testutil.CollectAndCount(sessionsOpen), "only project-b holds an open session")
	assert.Equal(t, 1.0, testutil.ToFloat64(sessionsOpen.WithLabelValues("project-b", testFederatorLocation, sessionPhasePending)))
	assert.Equal(t, 2, testutil.CollectAndCount(sessionsCreated), "a session that ended was still created")
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
