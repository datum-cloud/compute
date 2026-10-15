// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func TestClaimPublishesConnection(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)

	res := h.reconcile(a, testSession)

	s := h.cellSession(testSession)
	requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonSessionReady)
	if s.Status.Connection == nil || s.Status.Connection.EndpointID != a.EndpointID() ||
		s.Status.Connection.Target != a.cfg.Target || len(s.Status.Connection.RelayURLs) != 1 {
		t.Fatalf("connection = %+v", s.Status.Connection)
	}
	if want := h.clock.now().Add(time.Minute); !s.Status.ConnectBefore.Time.Equal(want) {
		t.Fatalf("connectBefore = %v, want %v", s.Status.ConnectBefore, want)
	}
	if res.RequeueAfter != time.Minute {
		t.Fatalf("requeue after %v, want the connect timeout", res.RequeueAfter)
	}
	if !controllerutil.ContainsFinalizer(s, computev1alpha.InstanceConsoleSessionAgentFinalizer) {
		t.Fatal("cell copy has no finalizer")
	}
	if n := len(h.slots()); n != 1 {
		t.Fatalf("slots = %d, want 1", n)
	}
}

func TestClaimRaceHasOneWinner(t *testing.T) {
	h := newHarness(t)
	agents := []*Agent{h.agent(), h.agent()}
	h.session(testSession, testUID)

	var wg sync.WaitGroup
	for _, a := range agents {
		wg.Go(func() {
			for range 3 {
				_, _ = a.Reconcile(h.ctx, reqFor(testSession))
			}
		})
	}
	wg.Wait()

	s := h.cellSession(testSession)
	requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonSessionReady)
	owners := 0
	for _, a := range agents {
		if s.Status.Connection.EndpointID == a.EndpointID() {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("session claimed by %d agents", owners)
	}
	if n := len(h.slots()); n != 1 {
		t.Fatalf("slots = %d, want 1", n)
	}
}

func TestClaimWithStaleCopyLosesToWinner(t *testing.T) {
	h := newHarness(t)
	winner, loser := h.agent(), h.agent()
	h.session(testSession, testUID)
	stale := h.cellSession(testSession)

	h.reconcile(winner, testSession)
	if _, err := loser.claim(h.ctx, stale); err != nil {
		t.Logf("losing claim returned %v", err)
	}

	s := h.cellSession(testSession)
	if s.Status.Connection.EndpointID != winner.EndpointID() {
		t.Fatal("a stale claim overwrote the winner's connection")
	}
	slots := h.slots()
	if len(slots) != 1 || slots[0].Annotations[annotationEndpoint] != winner.EndpointID() {
		t.Fatalf("slots = %+v, want only the winner's", slots)
	}
}

func TestStaleRetryKeepsTheAgentsOwnClaim(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	stale := h.cellSession(testSession)
	h.reconcile(a, testSession)

	if _, err := a.claim(h.ctx, stale); err != nil {
		t.Fatalf("retried claim returned %v", err)
	}

	s := h.cellSession(testSession)
	if s.Status.Connection == nil || s.Status.Connection.EndpointID != a.EndpointID() {
		t.Fatalf("connection = %+v", s.Status.Connection)
	}
	if n := len(h.slots()); n != 1 {
		t.Fatalf("slots = %d, want the claim's slot kept", n)
	}
}

func TestSlotCapIsExactlyThree(t *testing.T) {
	h := newHarness(t)
	agents := []*Agent{h.agent(), h.agent()}
	for i := range 6 {
		h.session(fmt.Sprintf("s%d", i), fmt.Sprintf("uid-%d", i))
	}

	var wg sync.WaitGroup
	for i := range 6 {
		a := agents[i%2]
		wg.Go(func() {
			for range 3 {
				_, _ = a.Reconcile(h.ctx, reqFor(fmt.Sprintf("s%d", i)))
			}
		})
	}
	wg.Wait()

	counts := map[string]int{}
	for i := range 6 {
		counts[readyReason(h.cellSession(fmt.Sprintf("s%d", i)))]++
	}
	if counts[computev1alpha.InstanceConsoleSessionReasonSessionReady] != 3 ||
		counts[computev1alpha.InstanceConsoleSessionReasonTooManySessions] != 3 {
		t.Fatalf("outcomes = %v, want 3 ready and 3 refused", counts)
	}
	if n := len(h.slots()); n != 3 {
		t.Fatalf("slots = %d, want 3", n)
	}
}

func TestSlotFreedByEndedSessionIsReused(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	for i := range 4 {
		h.session(fmt.Sprintf("s%d", i), fmt.Sprintf("uid-%d", i))
	}
	for i := range 3 {
		h.reconcile(a, fmt.Sprintf("s%d", i))
	}
	s0 := h.cellSession("s0")
	setReady(s0, metav1.ConditionFalse, computev1alpha.InstanceConsoleSessionReasonCompleted, "done")
	if err := h.cell.Status().Update(h.ctx, s0); err != nil {
		t.Fatal(err)
	}

	h.reconcile(a, "s3")

	requireReason(t, h.cellSession("s3"), computev1alpha.InstanceConsoleSessionReasonSessionReady)
}

func TestChecksBeforeClaim(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(h *harness)
		want   string
	}{
		{"no shell", func(h *harness) { h.exec.probeExit = 127 }, computev1alpha.InstanceConsoleSessionReasonNoShell},
		{"no writable directory", func(h *harness) { h.exec.probeExit = probeExitNoWritableDir }, computev1alpha.InstanceConsoleSessionReasonNoShell},
		{"missing executable", func(h *harness) { h.exec.probeExit = probeExitCommandMissing }, computev1alpha.InstanceConsoleSessionReasonCommandUnavailable},
		{"replaced instance", func(h *harness) {
			patchInstance(h, func(i *computev1alpha.Instance) {
				i.Labels[computev1alpha.WorkloadDeploymentUIDLabel] = "newer"
			})
			if err := h.cell.Create(h.ctx, &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{
				Name:      "web-1",
				Namespace: testNamespace,
				Labels:    map[string]string{computev1alpha.WorkloadDeploymentUIDLabel: testDeploymentUID},
			}}); err != nil {
				h.t.Fatal(err)
			}
		}, computev1alpha.InstanceConsoleSessionReasonInstanceNotFound},
		{"pod not running", func(h *harness) {
			patchPod(h, func(p *corev1.Pod) { p.Status.Phase = "Pending" })
		}, computev1alpha.InstanceConsoleSessionReasonInstanceNotRunning},
		{"pod from another instance", func(h *harness) {
			patchPod(h, func(p *corev1.Pod) { p.OwnerReferences[0].UID = "other" })
		}, computev1alpha.InstanceConsoleSessionReasonInstanceNotRunning},
		{"other runtime", func(h *harness) {
			patchPod(h, func(p *corev1.Pod) { p.Labels[managedByLabel] = "unikraft-provider" })
		}, computev1alpha.InstanceConsoleSessionReasonInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			a := h.agent()
			tc.mutate(h)
			h.session(testSession, testUID)

			h.reconcile(a, testSession)

			s := h.cellSession(testSession)
			requireReason(t, s, tc.want)
			if s.Status.Connection != nil || len(h.slots()) != 0 {
				t.Fatal("a refused session was claimed")
			}
		})
	}
}

func TestSessionForAnotherCellIsIgnored(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID, func(s *computev1alpha.InstanceConsoleSession) {
		s.Labels[computev1alpha.WorkloadDeploymentUIDLabel] = "elsewhere"
	})

	for range 3 {
		if res := h.reconcile(a, testSession); res.RequeueAfter <= 0 {
			t.Fatal("expected a retry while the session is unclaimed")
		}
		h.clock.advance(10 * time.Minute)
	}

	requireReason(t, h.cellSession(testSession), computev1alpha.InstanceConsoleSessionReasonPending)
	if len(h.slots()) != 0 {
		t.Fatal("a cell without the instance took a slot")
	}
}

// revokeSession annotates the cell copy the way Karmada does once the control
// plane revokes the session's hub copy.
func revokeSession(t *testing.T, h *harness) {
	t.Helper()
	s := h.cellSession(testSession)
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[computev1alpha.InstanceConsoleSessionRevokeAnnotation] = h.clock.now().Format(time.RFC3339)
	if err := h.cell.Update(h.ctx, s); err != nil {
		t.Fatal(err)
	}
}

func requireRevokedUnclaimed(t *testing.T, h *harness) {
	t.Helper()
	s := h.cellSession(testSession)
	requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonRevoked)
	if s.Status.Connection != nil || len(h.slots()) != 0 ||
		controllerutil.ContainsFinalizer(s, computev1alpha.InstanceConsoleSessionAgentFinalizer) {
		t.Fatalf("a revoked session was claimed: %+v", s)
	}
}

func TestRevokedSessionIsNotClaimed(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	revokeSession(t, h)

	h.reconcile(a, testSession)

	requireRevokedUnclaimed(t, h)
}

func TestClaimLosesToRevoke(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	stale := h.cellSession(testSession)
	revokeSession(t, h)

	if _, err := a.claim(h.ctx, stale); err != nil {
		t.Logf("losing claim returned %v", err)
	}
	s := h.cellSession(testSession)
	if s.Status.Connection != nil || len(h.slots()) != 0 {
		t.Fatalf("a claim read before the revoke landed: %+v", s)
	}

	h.reconcile(a, testSession)
	requireRevokedUnclaimed(t, h)
}

func TestProbeIsCachedPerPodAndContainer(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	h.session("s2", "uid-2")

	h.reconcile(a, testSession)
	h.reconcile(a, "s2")

	if n := h.exec.ran(probeScript); n != 1 {
		t.Fatalf("probe ran %d times, want 1", n)
	}
}

func TestNotConnectedDeadline(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	h.reconcile(a, testSession)

	h.clock.advance(time.Minute)
	h.reconcile(a, testSession)

	s := h.cellSession(testSession)
	requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonNotConnected)
	if s.Status.EndedAt == nil || s.Status.ExitCode != nil {
		t.Fatalf("status = %+v", s.Status)
	}
	if n := len(h.slots()); n != 0 {
		t.Fatalf("slots = %d, want 0", n)
	}
	if a.openCount() != 0 {
		t.Fatal("ended session still counts as open")
	}
}

func TestExpiryStopsConnectedSession(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	h.reconcile(a, testSession)
	markConnected(h, testSession, h.clock.now().Add(time.Minute))
	stopped := fakeLive(a, testUID, false)

	if res := h.reconcile(a, testSession); res.RequeueAfter != time.Minute {
		t.Fatalf("requeue after %v, want the time left", res.RequeueAfter)
	}
	h.clock.advance(time.Minute)
	h.reconcile(a, testSession)

	if got := <-stopped; got != computev1alpha.InstanceConsoleSessionReasonExpired {
		t.Fatalf("stop reason = %q, want Expired", got)
	}
}

func TestLivenessTakeover(t *testing.T) {
	h := newHarness(t)
	lost, survivor := h.agent(), h.agent()
	h.session(testSession, testUID)
	h.reconcile(lost, testSession)
	markConnected(h, testSession, h.clock.now().Add(time.Hour))

	if res := h.reconcile(survivor, testSession); res.RequeueAfter != LeaseDuration {
		t.Fatalf("requeue after %v while the owner is alive, want %v", res.RequeueAfter, LeaseDuration)
	}
	requireReason(t, h.cellSession(testSession), computev1alpha.InstanceConsoleSessionReasonConnected)

	h.clock.advance(LeaseDuration + time.Second)
	if err := survivor.renewLease(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.reconcile(survivor, testSession)

	s := h.cellSession(testSession)
	requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonAgentLost)
	if h.exec.ran(killScript) != 1 {
		t.Fatal("the lost session's processes were not stopped")
	}
	if n := len(h.slots()); n != 0 {
		t.Fatalf("slots = %d, want 0", n)
	}
}

func TestRestartedAgentEndsItsPreviousSessions(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	h.reconcile(a, testSession)
	markConnected(h, testSession, h.clock.now().Add(time.Hour))

	h.reconcile(a, testSession)

	requireReason(t, h.cellSession(testSession), computev1alpha.InstanceConsoleSessionReasonAgentLost)
	if h.exec.ran(killScript) != 1 {
		t.Fatal("the previous run's processes were not stopped")
	}
}

func TestSessionOwnedByAnotherAgentWithoutSlotIsLeftAlone(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	s := h.cellSession(testSession)
	s.Status.Connection = &computev1alpha.InstanceConsoleSessionConnection{EndpointID: "another-agents-endpoint"}
	setReady(s, metav1.ConditionTrue, computev1alpha.InstanceConsoleSessionReasonSessionReady, "ready")
	if err := h.cell.Status().Update(h.ctx, s); err != nil {
		t.Fatal(err)
	}

	h.reconcile(a, testSession)

	requireReason(t, h.cellSession(testSession), computev1alpha.InstanceConsoleSessionReasonSessionReady)
}

func TestRevokeStopsConnectedSession(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	h.reconcile(a, testSession)
	markConnected(h, testSession, h.clock.now().Add(time.Hour))
	stopped := fakeLive(a, testUID, false)
	revokeSession(t, h)

	h.reconcile(a, testSession)

	if got := <-stopped; got != computev1alpha.InstanceConsoleSessionReasonRevoked {
		t.Fatalf("stop reason = %q, want Revoked", got)
	}
	if !controllerutil.ContainsFinalizer(h.cellSession(testSession), computev1alpha.InstanceConsoleSessionAgentFinalizer) {
		t.Fatal("the finalizer holds the copy until the control plane deletes it")
	}
}

func TestRevokeEndsUnconnectedSessionAndFreesItsSlot(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	h.reconcile(a, testSession)
	revokeSession(t, h)

	h.reconcile(a, testSession)

	s := h.cellSession(testSession)
	requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonRevoked)
	if s.Status.EndedAt == nil {
		t.Fatal("the end has no time")
	}
	if len(h.slots()) != 0 {
		t.Fatal("slot kept after the revoke")
	}
}

func TestRevokeOfLostAgentsSessionStopsProcessesFirst(t *testing.T) {
	h := newHarness(t)
	lost, survivor := h.agent(), h.agent()
	h.session(testSession, testUID)
	h.reconcile(lost, testSession)
	markConnected(h, testSession, h.clock.now().Add(time.Hour))
	lost.releaseLease(h.ctx, lost.EndpointID())
	revokeSession(t, h)

	h.reconcile(survivor, testSession)

	requireReason(t, h.cellSession(testSession), computev1alpha.InstanceConsoleSessionReasonRevoked)
	if h.exec.ran(killScript) == 0 || len(h.slots()) != 0 {
		t.Fatal("the end was reported before the lost agent's processes were stopped")
	}
}

func TestDeletionReleasesFinalizerAfterProcessesStop(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	h.reconcile(a, testSession)
	markConnected(h, testSession, h.clock.now().Add(time.Hour))
	stopped := fakeLive(a, testUID, false)

	if err := h.cell.Delete(h.ctx, h.cellSession(testSession)); err != nil {
		t.Fatal(err)
	}
	h.reconcile(a, testSession)
	if got := <-stopped; got != computev1alpha.InstanceConsoleSessionReasonRevoked {
		t.Fatalf("stop reason = %q, want Revoked", got)
	}

	var cell computev1alpha.InstanceConsoleSession
	if err := h.cell.Get(h.ctx, reqFor(testSession).NamespacedName, &cell); !apierrors.IsNotFound(err) {
		t.Fatalf("cell copy still exists with finalizers %v: %v", cell.Finalizers, err)
	}
	if h.exec.ran(killScript) == 0 || len(h.slots()) != 0 {
		t.Fatal("processes were not stopped before the finalizer was released")
	}
}

func TestFinalizerHeldWhileProcessesCannotBeStopped(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	h.reconcile(a, testSession)
	markConnected(h, testSession, h.clock.now().Add(time.Hour))
	endAs(h, computev1alpha.InstanceConsoleSessionReasonAgentLost)
	h.exec.killExit = 1

	if err := h.cell.Delete(h.ctx, h.cellSession(testSession)); err != nil {
		t.Fatal(err)
	}
	if res := h.reconcile(a, testSession); res.RequeueAfter == 0 {
		t.Fatal("expected a retry while processes may be running")
	}
	if !controllerutil.ContainsFinalizer(h.cellSession(testSession), computev1alpha.InstanceConsoleSessionAgentFinalizer) {
		t.Fatal("finalizer released before processes were stopped")
	}

	h.exec.killExit = 0
	h.reconcile(a, testSession)
	var cell computev1alpha.InstanceConsoleSession
	if err := h.cell.Get(h.ctx, reqFor(testSession).NamespacedName, &cell); !apierrors.IsNotFound(err) {
		t.Fatalf("cell copy kept after processes stopped: %v", err)
	}
}

func TestUnclaimedDeletionIsRevoked(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID, func(s *computev1alpha.InstanceConsoleSession) {
		s.Finalizers = []string{"test/hold"}
	})
	if err := h.cell.Delete(h.ctx, h.cellSession(testSession)); err != nil {
		t.Fatal(err)
	}

	h.reconcile(a, testSession)

	requireReason(t, h.cellSession(testSession), computev1alpha.InstanceConsoleSessionReasonRevoked)
}

func TestDrain(t *testing.T) {
	h := newHarness(t)
	a := h.agent(func(c *Config) { c.DrainTimeout = 100 * time.Millisecond })
	h.session("waiting", "uid-w")
	h.session("tty", "uid-t")
	h.session("pipe", "uid-p", func(s *computev1alpha.InstanceConsoleSession) { s.Spec.Terminal = false })
	for _, name := range []string{"waiting", "tty", "pipe"} {
		h.reconcile(a, name)
	}
	markConnected(h, "tty", h.clock.now().Add(time.Hour))
	markConnected(h, "pipe", h.clock.now().Add(time.Hour))
	ttyClient, ttyStopped := liveWithStream(a, "uid-t", true)
	pipeClient, pipeStopped := liveWithStream(a, "uid-p", false)

	a.Drain(h.ctx)

	requireReason(t, h.cellSession("waiting"), computev1alpha.InstanceConsoleSessionReasonAgentShutdown)
	for _, stopped := range []chan string{ttyStopped, pipeStopped} {
		if got := <-stopped; got != computev1alpha.InstanceConsoleSessionReasonAgentShutdown {
			t.Fatalf("stop reason = %q, want AgentShutdown", got)
		}
	}
	if got := <-ttyClient; got == "" {
		t.Fatal("terminal session got no maintenance notice")
	}
	if got := <-pipeClient; got != "" {
		t.Fatalf("session without a terminal got injected bytes %q", got)
	}
	if !a.draining.Load() || a.claiming() {
		t.Fatal("a drained agent still claims")
	}
	h.session("late", "uid-l")
	h.reconcile(a, "late")
	requireReason(t, h.cellSession("late"), computev1alpha.InstanceConsoleSessionReasonPending)
}

func TestConcurrencyCapDefersClaims(t *testing.T) {
	h := newHarness(t)
	a := h.agent(func(c *Config) { c.MaxOpenSessions = 1 })
	h.session(testSession, testUID)
	h.session("s2", "uid-2")

	h.reconcile(a, testSession)
	res := h.reconcile(a, "s2")

	requireReason(t, h.cellSession("s2"), computev1alpha.InstanceConsoleSessionReasonPending)
	if res.RequeueAfter == 0 {
		t.Fatal("a deferred claim must be retried")
	}
}

func TestClaimWaitsForReadyEndpoint(t *testing.T) {
	h := newHarness(t)
	a := h.agent(func(c *Config) { c.EndpointPodName = testEndpointPod })
	h.session(testSession, testUID)

	res := h.reconcile(a, testSession)
	requireReason(t, h.cellSession(testSession), computev1alpha.InstanceConsoleSessionReasonPending)
	if res.RequeueAfter == 0 {
		t.Fatal("a claim deferred for a missing endpoint must be retried")
	}

	endpoint := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: testAgentNamespace, Name: testEndpointPod}}
	if err := h.cell.Create(h.ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	h.reconcile(a, testSession)
	requireReason(t, h.cellSession(testSession), computev1alpha.InstanceConsoleSessionReasonPending)

	endpoint.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := h.cell.Status().Update(h.ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	h.reconcile(a, testSession)
	requireReason(t, h.cellSession(testSession), computev1alpha.InstanceConsoleSessionReasonSessionReady)
}

func TestSweepStopsOrphanedProcesses(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, "uid-live")
	h.reconcile(a, testSession)
	orphan := &slot{
		sessionUID: "uid-gone",
		session:    types.NamespacedName{Namespace: testNamespace, Name: "gone"},
		pod:        types.NamespacedName{Namespace: testNamespace, Name: testInstance},
		podUID:     "pod-uid",
		container:  testContainer,
		markerDir:  "/tmp",
	}
	if err := h.cell.Create(h.ctx, orphan.leaseFor(testAgentNamespace, "slot-orphan-0")); err != nil {
		t.Fatal(err)
	}
	h.exec.markers = "uid-live\nuid-gone\nuid-older\n"

	if err := a.sweep(h.ctx); err != nil {
		t.Fatal(err)
	}

	killed := map[string]bool{}
	for _, c := range h.exec.commands {
		if len(c) > 4 && c[2] == killScript {
			killed[c[4]] = true
		}
	}
	for _, uid := range []string{"uid-gone", "uid-older"} {
		if !killed[markerPath("/tmp", uid)] {
			t.Fatalf("orphan %s was not stopped (killed %v)", uid, killed)
		}
	}
	if killed[markerPath("/tmp", "uid-live")] {
		t.Fatal("an open session's process was stopped")
	}
	slots := h.slots()
	if len(slots) != 1 || ptr.Deref(slots[0].Spec.HolderIdentity, "") != "uid-live" {
		t.Fatalf("slots = %v, want only the open session's", slots)
	}
}

func TestEnsureIdentityCreatesKey(t *testing.T) {
	h := newHarness(t)
	a := h.agent(func(c *Config) { c.Ordinal = 1 })
	a.identity.Store(nil)

	if err := a.EnsureIdentity(h.ctx); err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	if err := h.cell.Get(h.ctx, client.ObjectKey{Namespace: testAgentNamespace, Name: "endpoint-key-1"}, &secret); err != nil {
		t.Fatal(err)
	}
	if len(secret.Data[KeySecretKey]) != 32 || a.EndpointID() != EndpointID(secret.Data[KeySecretKey]) {
		t.Fatal("endpoint ID does not match the stored key")
	}
	first := a.EndpointID()
	if err := a.EnsureIdentity(h.ctx); err != nil || a.EndpointID() != first {
		t.Fatal("an existing key was replaced")
	}
}

func TestRotationWaitsForOpenSessions(t *testing.T) {
	rotationPollInterval = 10 * time.Millisecond
	h := newHarness(t)
	a := h.agent()
	a.identity.Store(nil)
	if err := a.EnsureIdentity(h.ctx); err != nil {
		t.Fatal(err)
	}
	before := a.EndpointID()
	a.track("uid-open", types.NamespacedName{Name: "open"})

	done := make(chan error, 1)
	go func() { done <- a.rotate(h.ctx) }()
	time.Sleep(50 * time.Millisecond)
	if a.EndpointID() != before || !a.rotating.Load() || a.claiming() {
		t.Fatal("rotation must stop claiming and wait for open sessions")
	}
	a.forget("uid-open")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if a.EndpointID() == before || a.rotating.Load() {
		t.Fatal("key was not rotated")
	}
	var lease coordinationv1.Lease
	if err := h.cell.Get(h.ctx, client.ObjectKey{Namespace: testAgentNamespace, Name: agentLeaseName(before)}, &lease); err == nil {
		t.Fatal("the old endpoint's liveness lease was kept")
	}
}

func reqFor(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: name}}
}

func markConnected(h *harness, name string, expires time.Time) {
	h.t.Helper()
	s := h.cellSession(name)
	started := metav1.NewTime(h.clock.now())
	s.Status.StartedAt = &started
	s.Status.ExpiresAt = &metav1.Time{Time: expires}
	setReady(s, metav1.ConditionTrue, computev1alpha.InstanceConsoleSessionReasonConnected, "connected")
	if err := h.cell.Status().Update(h.ctx, s); err != nil {
		h.t.Fatal(err)
	}
}

func endAs(h *harness, reason string) {
	h.t.Helper()
	s := h.cellSession(testSession)
	setReady(s, metav1.ConditionFalse, reason, "ended")
	if err := h.cell.Status().Update(h.ctx, s); err != nil {
		h.t.Fatal(err)
	}
}

// fakeLive registers a connection that reports the reason it was stopped for.
func fakeLive(a *Agent, uid string, tty bool) chan string {
	stopped := make(chan string, 1)
	var l *liveSession
	l, _ = a.register(uid, func() {
		go func() {
			stopped <- a.stopReason(uid)
			a.unregister(uid)
			close(l.done)
		}()
	}, tty)
	return stopped
}

// liveWithStream registers a connection with a client stream, and reports
// what the client read before the stream was stopped.
func liveWithStream(a *Agent, uid string, tty bool) (chan string, chan string) {
	clientSide, agentSide := net.Pipe()
	read := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		_ = clientSide.SetReadDeadline(time.Now().Add(time.Second))
		n, _ := clientSide.Read(buf)
		read <- string(buf[:n])
	}()
	stopped := fakeLive(a, uid, tty)
	a.mu.Lock()
	a.live[uid].stream = &clientStream{conn: agentSide}
	a.mu.Unlock()
	return read, stopped
}

func patchInstance(h *harness, mutate func(*computev1alpha.Instance)) {
	h.t.Helper()
	var inst computev1alpha.Instance
	if err := h.cell.Get(h.ctx, client.ObjectKey{Namespace: testNamespace, Name: testInstance}, &inst); err != nil {
		h.t.Fatal(err)
	}
	mutate(&inst)
	if err := h.cell.Update(h.ctx, &inst); err != nil {
		h.t.Fatal(err)
	}
}

func patchPod(h *harness, mutate func(*corev1.Pod)) {
	h.t.Helper()
	var pod corev1.Pod
	if err := h.cell.Get(h.ctx, client.ObjectKey{Namespace: testNamespace, Name: testInstance}, &pod); err != nil {
		h.t.Fatal(err)
	}
	mutate(&pod)
	status := pod.Status
	if err := h.cell.Update(h.ctx, &pod); err != nil {
		h.t.Fatal(err)
	}
	pod.Status = status
	if err := h.cell.Status().Update(h.ctx, &pod); err != nil {
		h.t.Fatal(err)
	}
}

func TestTerminalSessionIsNotRequeued(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	endAs(h, computev1alpha.InstanceConsoleSessionReasonNoShell)

	if res := h.reconcile(a, testSession); res.RequeueAfter != 0 {
		t.Fatalf("requeue after %v for an ended session this agent never held", res.RequeueAfter)
	}
}

func TestHungExecIsBounded(t *testing.T) {
	h := newHarness(t)
	a := h.agent(func(c *Config) { c.ExecTimeout = 200 * time.Millisecond })
	h.session(testSession, testUID)
	h.reconcile(a, testSession)
	held, err := a.slotFor(h.ctx, testUID)
	if err != nil || held == nil {
		t.Fatalf("slot = %v, %v", held, err)
	}
	h.exec.mu.Lock()
	h.exec.killHangs = true
	h.exec.mu.Unlock()

	start := time.Now()
	stopped, err := a.stopProcesses(h.ctx, held)

	if err != nil || stopped {
		t.Fatalf("stopProcesses() = %v, %v; want not stopped, no error", stopped, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("a hung exec held the caller for %v", took)
	}
}

func TestAgentLeaseNamesAreDistinctAndValid(t *testing.T) {
	a := agentLeaseName(strings.Repeat("a", 16) + strings.Repeat("0", 48))
	b := agentLeaseName(strings.Repeat("a", 16) + strings.Repeat("1", 48))
	if a == b {
		t.Fatal("endpoint IDs sharing a prefix share a liveness Lease")
	}
	if errs := validation.IsDNS1123Subdomain(a); len(errs) != 0 {
		t.Fatalf("lease name %q is invalid: %v", a, errs)
	}
}

func TestEndIsConfirmedOnlyOnceProcessesStop(t *testing.T) {
	h := newHarness(t)
	lost, survivor := h.agent(), h.agent()
	h.session(testSession, testUID)
	h.reconcile(lost, testSession)
	markConnected(h, testSession, h.clock.now().Add(time.Hour))
	lost.releaseLease(h.ctx, lost.EndpointID())
	h.exec.killExit = 1

	h.reconcile(survivor, testSession)

	s := h.cellSession(testSession)
	requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonAgentLost)
	if s.Status.EndedAt != nil {
		t.Fatal("endedAt set while the session's processes may still run")
	}
	if len(h.slots()) != 1 {
		t.Fatal("slot released before the processes were stopped")
	}

	if res := h.reconcile(survivor, testSession); res.RequeueAfter == 0 {
		t.Fatal("expected a retry while processes may be running")
	}
	if h.cellSession(testSession).Status.EndedAt != nil {
		t.Fatal("endedAt set after another failed stop")
	}

	h.exec.killExit = 0
	h.reconcile(survivor, testSession)
	s = h.cellSession(testSession)
	if s.Status.EndedAt == nil {
		t.Fatal("endedAt not set once the processes stopped")
	}
	if len(h.slots()) != 0 {
		t.Fatal("slot kept after the processes stopped")
	}
}

func TestSweepConfirmsEndOnceProcessesStop(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	h.session(testSession, testUID)
	h.reconcile(a, testSession)
	markConnected(h, testSession, h.clock.now().Add(time.Hour))
	endAs(h, computev1alpha.InstanceConsoleSessionReasonDisconnected)

	if err := a.sweep(h.ctx); err != nil {
		t.Fatal(err)
	}

	if h.cellSession(testSession).Status.EndedAt == nil {
		t.Fatal("the sweep stopped the processes but did not record it")
	}
	if len(h.slots()) != 0 {
		t.Fatal("slot kept after the sweep stopped the processes")
	}
}
