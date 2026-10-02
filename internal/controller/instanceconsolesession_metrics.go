// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Control-plane metrics for Instance shell sessions, the project and location
// view of the design's monitoring requirements. Labels are bounded: a project
// namespace, a location, a phase or an ending reason. Sessions, users and
// Instances never appear in a label.
//
// The location is the one the session controller stamps on the session at
// delivery, taken from the hub WorkloadDeployment's location label. A session
// that ends before it is delivered has no location, and its label is empty.
const (
	metricsNamespace        = "compute"
	sessionMetricsSubsystem = "shell"

	labelProject  = "project"
	labelLocation = "location"

	// Phases of an open session, derived from its Ready condition.
	sessionPhasePending   = "pending"
	sessionPhaseReady     = "ready"
	sessionPhaseConnected = "connected"
)

var (
	// sessionsCreated counts sessions delivered to a cell, once per session.
	sessionsCreated = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: sessionMetricsSubsystem,
			Name:      "sessions_created_total",
			Help:      "Shell sessions delivered to a cell, by project namespace and location.",
		},
		[]string{labelProject, labelLocation},
	)

	// sessionsOpen is the number of sessions that have not ended, by phase. It
	// is recomputed from the sessions the controller has observed rather than
	// incremented, so a reconcile repeated for the same session cannot skew it.
	sessionsOpen = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: sessionMetricsSubsystem,
			Name:      "sessions_open",
			Help: "Shell sessions that have not ended, by project namespace, location " +
				"and phase (pending, ready, connected).",
		},
		[]string{labelProject, labelLocation, "phase"},
	)

	// sessionsEnded counts session ends by their Ready condition reason, once
	// per session, when the end is recorded in the project's activity.
	sessionsEnded = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: sessionMetricsSubsystem,
			Name:      "sessions_ended_total",
			Help:      "Shell sessions that ended, by location and ending reason.",
		},
		[]string{labelLocation, "reason"},
	)

	// sessionConnectSeconds is the time from a session's creation to its
	// client's connection, the design's time to first prompt. The buckets span
	// the claim timeout and the connect timeout.
	sessionConnectSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: sessionMetricsSubsystem,
			Name:      "session_connect_seconds",
			Help:      "Seconds from a shell session's creation to its client's connection, by location.",
			Buckets:   []float64{0.5, 1, 2, 3, 5, 10, 20, 30, 60, 90},
		},
		[]string{labelLocation},
	)

	// sessionDurationSeconds is the time a session's client was connected. The
	// buckets span the default TTL and the longest one a session may request.
	sessionDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: sessionMetricsSubsystem,
			Name:      "session_duration_seconds",
			Help:      "Seconds from a shell session's client connection to its end, by location and ending reason.",
			Buckets:   []float64{1, 5, 15, 30, 60, 300, 900, 1800, 3600, 7200, 14400},
		},
		[]string{labelLocation, "reason"},
	)

	// openSessionsByKey backs sessionsOpen.
	openSessionsByKey = newOpenSessionTracker()
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		sessionsCreated,
		sessionsOpen,
		sessionsEnded,
		sessionConnectSeconds,
		sessionDurationSeconds,
	)
}

// sessionPlacement is where a session was delivered: the location and the
// cell, as recorded on the session at delivery.
type sessionPlacement struct {
	location string
	cell     string
}

func placementOf(session *computev1alpha.InstanceConsoleSession) sessionPlacement {
	return sessionPlacement{
		location: session.Annotations[computev1alpha.InstanceConsoleSessionLocationAnnotation],
		cell:     session.Annotations[computev1alpha.InstanceConsoleSessionCellAnnotation],
	}
}

// sessionPhase maps an open session's Ready condition to a phase label.
func sessionPhase(session *computev1alpha.InstanceConsoleSession) string {
	switch sessionReadyReason(session) {
	case computev1alpha.InstanceConsoleSessionReasonConnected:
		return sessionPhaseConnected
	case computev1alpha.InstanceConsoleSessionReasonSessionReady:
		return sessionPhaseReady
	}
	return sessionPhasePending
}

// openSessionSeries is one series of sessionsOpen.
type openSessionSeries struct {
	project, location, phase string
}

// openSessionTracker keeps the last observed state of every open session and
// exports their counts. A session is keyed by the project cluster, namespace
// and name the controller reconciles it under, so a deleted session, whose
// reconcile sees only its key, can be dropped.
type openSessionTracker struct {
	mu       sync.Mutex
	sessions map[string]openSessionSeries
	exported map[openSessionSeries]struct{}
}

func newOpenSessionTracker() *openSessionTracker {
	return &openSessionTracker{
		sessions: map[string]openSessionSeries{},
		exported: map[openSessionSeries]struct{}{},
	}
}

// observe records an open session's current state.
func (t *openSessionTracker) observe(key string, series openSessionSeries) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessions[key] = series
	t.export()
}

// forget drops a session that ended or was deleted.
func (t *openSessionTracker) forget(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.sessions[key]; !ok {
		return
	}
	delete(t.sessions, key)
	t.export()
}

// export recomputes sessionsOpen from the tracked sessions. Series with no
// sessions left are removed rather than set to zero, so the gauge does not
// keep a series for every project that ever opened a session.
func (t *openSessionTracker) export() {
	counts := make(map[openSessionSeries]int, len(t.exported))
	for _, series := range t.sessions {
		counts[series]++
	}
	for series := range t.exported {
		if _, ok := counts[series]; !ok {
			sessionsOpen.DeleteLabelValues(series.project, series.location, series.phase)
			delete(t.exported, series)
		}
	}
	for series, n := range counts {
		sessionsOpen.WithLabelValues(series.project, series.location, series.phase).Set(float64(n))
		t.exported[series] = struct{}{}
	}
}

// reset clears every tracked session, for tests.
func (t *openSessionTracker) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessions = map[string]openSessionSeries{}
	t.export()
}
