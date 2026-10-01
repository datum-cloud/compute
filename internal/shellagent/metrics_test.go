// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	testCell  = "cell-dfw-1"
	testAgent = "exec-agent-0"
)

func labeled(c *Config) {
	c.Cell = testCell
	c.Agent = testAgent
}

func TestClaimMetricsFollowTheReconciler(t *testing.T) {
	agentClaims.Reset()
	agentSessionsOpen.Reset()
	h := newHarness(t)
	a := h.agent(labeled)
	h.session(testSession, testUID)

	h.reconcile(a, testSession)
	if got := testutil.ToFloat64(agentClaims.WithLabelValues(testCell, claimOutcomeClaimed)); got != 1 {
		t.Fatalf("claimed = %v, want 1", got)
	}
	if got := testutil.ToFloat64(agentSessionsOpen.WithLabelValues(testCell, testAgent)); got != 1 {
		t.Fatalf("open = %v, want 1", got)
	}

	h.clock.advance(2 * a.cfg.ConnectTimeout)
	h.reconcile(a, testSession)
	if got := testutil.ToFloat64(agentClaims.WithLabelValues(testCell, claimOutcomeNotConnected)); got != 1 {
		t.Fatalf("not_connected = %v, want 1", got)
	}
	if got := testutil.ToFloat64(agentSessionsOpen.WithLabelValues(testCell, testAgent)); got != 0 {
		t.Fatalf("open = %v, want 0 once the session ended", got)
	}

	// A session that cannot run is refused before any claim. Its command is
	// one the agent has not probed in the container yet.
	h.session("no-bash", "uid-2", func(s *computev1alpha.InstanceConsoleSession) { s.Spec.Command = []string{"bash"} })
	h.exec.mu.Lock()
	h.exec.probeExit = probeExitCommandMissing
	h.exec.mu.Unlock()
	h.reconcile(a, "no-bash")
	if got := testutil.ToFloat64(agentClaims.WithLabelValues(testCell, claimOutcomeRefused)); got != 1 {
		t.Fatalf("refused = %v, want 1", got)
	}
}

func TestEndpointReachableMetricFollowsTheEndpointPod(t *testing.T) {
	h := newHarness(t)
	a := h.agent(labeled, func(c *Config) { c.EndpointPodName = testEndpointPod })

	if err := a.renewLease(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(agentEndpointReachable.WithLabelValues(testCell, testAgent)); got != 0 {
		t.Fatalf("reachable = %v, want 0 without an endpoint pod", got)
	}

	endpoint := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: testAgentNamespace, Name: testEndpointPod}}
	if err := h.cell.Create(h.ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	endpoint.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := h.cell.Status().Update(h.ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	if err := a.renewLease(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(agentEndpointReachable.WithLabelValues(testCell, testAgent)); got != 1 {
		t.Fatalf("reachable = %v, want 1 with a ready endpoint pod", got)
	}

	// Draining stops claims but keeps reporting the endpoint.
	a.draining.Store(true)
	if err := a.renewLease(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(agentEndpointReachable.WithLabelValues(testCell, testAgent)); got != 1 {
		t.Fatalf("reachable = %v, want 1 while draining", got)
	}
}

func TestCleanupUnconfirmedMetricCountsFailedStops(t *testing.T) {
	agentCleanupUnconfirmed.Reset()
	h := newHarness(t)
	a := h.agent(labeled)
	var pod corev1.Pod
	if err := h.cell.Get(h.ctx, types.NamespacedName{Namespace: testNamespace, Name: testInstance}, &pod); err != nil {
		t.Fatal(err)
	}
	held := &slot{
		sessionUID:  testUID,
		pod:         types.NamespacedName{Namespace: testNamespace, Name: testInstance},
		podUID:      pod.UID,
		container:   testContainer,
		containerID: pod.Status.ContainerStatuses[0].ContainerID,
		markerDir:   h.exec.probeDir,
	}

	h.exec.mu.Lock()
	h.exec.killExit = 1
	h.exec.mu.Unlock()
	stopped, err := a.stopProcesses(h.ctx, held)
	if err != nil || stopped {
		t.Fatalf("stopProcesses = %v, %v; want unconfirmed", stopped, err)
	}
	if got := testutil.ToFloat64(agentCleanupUnconfirmed.WithLabelValues(testCell)); got != 1 {
		t.Fatalf("unconfirmed = %v, want 1", got)
	}

	h.exec.mu.Lock()
	h.exec.killExit = 0
	h.exec.mu.Unlock()
	stopped, err = a.stopProcesses(h.ctx, held)
	if err != nil || !stopped {
		t.Fatalf("stopProcesses = %v, %v; want stopped", stopped, err)
	}
	if got := testutil.ToFloat64(agentCleanupUnconfirmed.WithLabelValues(testCell)); got != 1 {
		t.Fatalf("unconfirmed = %v, want still 1 after a confirmed stop", got)
	}
}
