// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Cell-side metrics for Instance shell sessions, the cell and agent view of
// the design's monitoring requirements. The cell and agent labels come from
// the agent's configuration; sessions, users and Instances never appear in a
// label.
const (
	metricsNamespace = "compute"
	metricsSubsystem = "shell_agent"

	labelCell  = "cell"
	labelAgent = "agent"
)

// Outcomes of the reconciler's decisions about a session, the values of the
// outcome label on agentClaims.
const (
	// claimOutcomeClaimed means the agent claimed the session.
	claimOutcomeClaimed = "claimed"
	// claimOutcomeRefused means the agent ended an unclaimed session because
	// it cannot run: no Instance, no shell, no such command, too many
	// sessions, or an invalid delivery.
	claimOutcomeRefused = "refused"
	// claimOutcomeTakeover means the agent ended a session whose agent was
	// lost, including one its own previous run served.
	claimOutcomeTakeover = "takeover"
	// claimOutcomeExpired means the agent ended a connected session that
	// reached its time limit.
	claimOutcomeExpired = "expired"
	// claimOutcomeNotConnected means the agent ended a session it claimed
	// that no client connected to in time.
	claimOutcomeNotConnected = "not_connected"
	// claimOutcomeRevoked means the agent ended a session the control plane
	// revoked or deleted.
	claimOutcomeRevoked = "revoked"
	// claimOutcomeShutdown means the agent ended a session it claimed because
	// it is draining.
	claimOutcomeShutdown = "shutdown"
)

var (
	// agentSessionsOpen is the number of sessions the agent has claimed and
	// not yet ended, set from the agent's own count whenever it changes.
	agentSessionsOpen = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "sessions_open",
			Help:      "Shell sessions the agent has claimed and not yet ended, by cell and agent.",
		},
		[]string{labelCell, labelAgent},
	)

	// agentEndpointReachable is 1 while the agent's paired tunnel endpoint is
	// ready, which its relay probe grants only while a client can reach the
	// endpoint through the relays the agent publishes. It is refreshed on
	// every liveness renewal.
	agentEndpointReachable = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "endpoint_reachable",
			Help:      "1 while the agent's tunnel endpoint is reachable through its relays, else 0, by cell and agent.",
		},
		[]string{labelCell, labelAgent},
	)

	// agentClaims counts the reconciler's decisions about sessions.
	agentClaims = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "claims_total",
			Help: "Shell session decisions by the cell's agents, by cell and outcome " +
				"(claimed, refused, takeover, expired, not_connected, revoked, shutdown).",
		},
		[]string{labelCell, "outcome"},
	)

	// agentCleanupUnconfirmed counts attempts to stop a session's processes
	// that could not confirm they stopped. The sweep retries such a session,
	// so one leaked process counts once per attempt until it is stopped.
	agentCleanupUnconfirmed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "cleanup_unconfirmed_total",
			Help:      "Attempts to stop a shell session's processes that could not confirm they stopped, by cell.",
		},
		[]string{labelCell},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		agentSessionsOpen,
		agentEndpointReachable,
		agentClaims,
		agentCleanupUnconfirmed,
	)
}

func (a *Agent) recordOutcome(outcome string) {
	agentClaims.WithLabelValues(a.cfg.Cell, outcome).Inc()
}

func (a *Agent) recordEndpointReachable(reachable bool) {
	value := 0.0
	if reachable {
		value = 1
	}
	agentEndpointReachable.WithLabelValues(a.cfg.Cell, a.cfg.Agent).Set(value)
}

func (a *Agent) recordCleanupUnconfirmed() {
	agentCleanupUnconfirmed.WithLabelValues(a.cfg.Cell).Inc()
}

// recordOpen exports the open-session count. The caller holds a.mu.
func (a *Agent) recordOpen() {
	agentSessionsOpen.WithLabelValues(a.cfg.Cell, a.cfg.Agent).Set(float64(len(a.open)))
}
