// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Drain stops the agent claiming and connecting sessions, ends the ones it
// claimed that no client connected to, warns connected sessions that have a
// terminal, and waits up to the drain timeout for connected sessions to end
// before ending the rest with AgentShutdown.
func (a *Agent) Drain(ctx context.Context) {
	a.draining.Store(true)
	logger := log.FromContext(ctx)
	deadline := time.Now().Add(a.cfg.DrainTimeout)

	for uid, key := range a.openSessions() {
		if a.isLive(uid) {
			continue
		}
		var session computev1alpha.InstanceConsoleSession
		if err := a.cell.Get(ctx, key, &session); err != nil {
			continue
		}
		if err := a.endUnconnected(ctx, &session, computev1alpha.InstanceConsoleSessionReasonAgentShutdown); err != nil {
			logger.Error(err, "end unconnected session", "session", uid)
		}
	}

	notice := fmt.Sprintf("\r\n*** Maintenance: this session will close by %s. Start a new session to continue. ***\r\n",
		deadline.UTC().Format("15:04:05 MST"))
	for _, l := range a.liveSessions() {
		a.mu.Lock()
		stream, tty := l.stream, l.tty
		a.mu.Unlock()
		if tty && stream != nil {
			_ = stream.Notify(notice)
		}
	}

	for time.Now().Before(deadline) && len(a.liveSessions()) > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}

	var wg sync.WaitGroup
	for uid := range a.liveSessions() {
		wg.Go(func() {
			a.stop(uid, computev1alpha.InstanceConsoleSessionReasonAgentShutdown)
		})
	}
	wg.Wait()
	a.releaseLease(ctx, a.EndpointID())
}
