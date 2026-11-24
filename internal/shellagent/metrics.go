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

// Purposes of the commands the agent runs inside instances, the values of the
// purpose label on agentExecs.
const (
	// execPurposeProbe checks a container can run a session's command.
	execPurposeProbe = "probe"
	// execPurposeExitWatch waits in the container for a session command to
	// exit, for as long as the session may last.
	execPurposeExitWatch = "exit_watch"
	// execPurposeExitCheck polls for a session command's exit status.
	execPurposeExitCheck = "exit_check"
	// execPurposeStop stops a session's processes.
	execPurposeStop = "stop"
	// execPurposeMarkerCheck looks for leftover session markers in a
	// container during the sweep.
	execPurposeMarkerCheck = "marker_check"
)

// Results of a command the agent ran inside an instance, the values of the
// result label on agentExecs.
const (
	execResultOK      = "ok"
	execResultNonZero = "nonzero"
	execResultError   = "error"
)

// How the sweep found an ended session's processes, the values of the source
// label on agentOrphansReaped.
const (
	// orphanSourceSlot is a session whose slot outlived it.
	orphanSourceSlot = "slot"
	// orphanSourceMarker is a session whose marker file was found in a
	// container after its slot was gone.
	orphanSourceMarker = "marker"
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

	// agentExecs counts the commands the agent runs inside instances. Each is
	// an exec request to the cell's API server, so this is the agent's share
	// of that load.
	agentExecs = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "execs_total",
			Help: "Commands the agent ran inside instances, by cell, purpose " +
				"(probe, exit_watch, exit_check, stop, marker_check) and result (ok, nonzero, error).",
		},
		[]string{labelCell, "purpose", "result"},
	)

	// agentSweepSeconds is how long each sweep for leftover session
	// processes took. Its count is the number of sweeps.
	agentSweepSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "sweep_duration_seconds",
			Help:      "Seconds each sweep for leftover shell session processes took, by cell.",
			Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300},
		},
		[]string{labelCell},
	)

	// agentSweepErrors counts sweeps that could not list the cell's slots
	// and so did no work.
	agentSweepErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "sweep_errors_total",
			Help:      "Sweeps for leftover shell session processes that failed before doing any work, by cell.",
		},
		[]string{labelCell},
	)

	// agentOrphansReaped counts ended sessions the sweep cleaned up after,
	// stopping their processes or finding them already gone with their pod.
	agentOrphansReaped = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "orphans_reaped_total",
			Help:      "Ended shell sessions whose leftover processes the sweep cleaned up, by cell and source (slot, marker).",
		},
		[]string{labelCell, "source"},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		agentSessionsOpen,
		agentEndpointReachable,
		agentClaims,
		agentCleanupUnconfirmed,
		agentExecs,
		agentSweepSeconds,
		agentSweepErrors,
		agentOrphansReaped,
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

func (a *Agent) recordExec(purpose string, code int, err error) {
	result := execResultOK
	switch {
	case err != nil:
		result = execResultError
	case code != 0:
		result = execResultNonZero
	}
	agentExecs.WithLabelValues(a.cfg.Cell, purpose, result).Inc()
}

func (a *Agent) recordOrphanReaped(source string) {
	agentOrphansReaped.WithLabelValues(a.cfg.Cell, source).Inc()
}

func (a *Agent) recordCleanupUnconfirmed() {
	agentCleanupUnconfirmed.WithLabelValues(a.cfg.Cell).Inc()
}

// recordOpen exports the open-session count. The caller holds a.mu.
func (a *Agent) recordOpen() {
	agentSessionsOpen.WithLabelValues(a.cfg.Cell, a.cfg.Agent).Set(float64(len(a.open)))
}
