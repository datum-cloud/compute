// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"fmt"
	"testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func manySlots(c *Config) { c.SlotsPerInstance = 100 }

// firstInOrder returns the agent the cell's claim order puts first for a
// session.
func firstInOrder(uid string, agents ...*Agent) *Agent {
	first := agents[0]
	for _, a := range agents[1:] {
		if claimRank(uid, agentLeaseName(a.EndpointID())) < claimRank(uid, agentLeaseName(first.EndpointID())) {
			first = a
		}
	}
	return first
}

func TestClaimsSpreadAcrossAgents(t *testing.T) {
	h := newHarness(t)
	agents := []*Agent{h.agent(manySlots), h.agent(manySlots)}
	const sessions = 24

	claimed := map[string]int{}
	for i := range sessions {
		name, uid := fmt.Sprintf("s%d", i), fmt.Sprintf("uid-%d", i)
		h.session(name, uid, h.justCreated)
		for _, a := range agents {
			h.reconcile(a, name)
		}
		s := h.cellSession(name)
		requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonSessionReady)
		if want := firstInOrder(uid, agents...).EndpointID(); s.Status.Connection.EndpointID != want {
			t.Fatalf("session %s claimed by %s, want the first agent in its claim order, %s",
				name, s.Status.Connection.EndpointID, want)
		}
		claimed[s.Status.Connection.EndpointID]++
	}
	for _, a := range agents {
		if claimed[a.EndpointID()] == 0 {
			t.Fatalf("claims = %v: one agent took every session although both were reconciled for each", claimed)
		}
	}
}

func TestLaterAgentClaimsOnceItsTurnComes(t *testing.T) {
	h := newHarness(t)
	agents := []*Agent{h.agent(), h.agent()}
	h.session(testSession, testUID, h.justCreated)
	second := agents[0]
	if firstInOrder(testUID, agents...) == second {
		second = agents[1]
	}

	res := h.reconcile(second, testSession)
	requireReason(t, h.cellSession(testSession), computev1alpha.InstanceConsoleSessionReasonPending)
	if want := DefaultConfig().ClaimStagger; res.RequeueAfter != want {
		t.Fatalf("requeue after %v, want the agent's turn in %v", res.RequeueAfter, want)
	}

	h.clock.advance(res.RequeueAfter)
	h.reconcile(second, testSession)
	s := h.cellSession(testSession)
	requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonSessionReady)
	if s.Status.Connection.EndpointID != second.EndpointID() {
		t.Fatal("the later agent did not take the session its predecessor left")
	}
}

func TestAgentThatCannotClaimIsLeftOutOfTheOrder(t *testing.T) {
	h := newHarness(t)
	h.agent(manySlots, func(c *Config) { c.EndpointPodName = testEndpointPod })
	ready := h.agent(manySlots)

	for i := range 16 {
		name := fmt.Sprintf("s%d", i)
		h.session(name, fmt.Sprintf("uid-%d", i), h.justCreated)
		if res := h.reconcile(ready, name); res.RequeueAfter != DefaultConfig().ConnectTimeout {
			t.Fatalf("session %s: requeue after %v; an agent whose endpoint is not ready must not hold back claims",
				name, res.RequeueAfter)
		}
		requireReason(t, h.cellSession(name), computev1alpha.InstanceConsoleSessionReasonSessionReady)
	}
}
