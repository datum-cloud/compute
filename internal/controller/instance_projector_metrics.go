// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/types"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// projectionsStalled is the number of Instances whose projection has waited
// longer than projectClusterStallBound for its project cluster. The projector
// log names each one.
var projectionsStalled = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: metricsNamespace,
	Subsystem: "instance_projector",
	Name:      "stalled_projections",
	Help:      "Instances whose projection has waited too long for its project cluster to become available.",
})

func init() {
	ctrlmetrics.Registry.MustRegister(projectionsStalled)
}

// projectionStallTracker remembers when this process first saw each Instance
// wait for its project cluster. Times are local to the process, so a restart,
// which is when waits are expected, starts every wait over.
type projectionStallTracker struct {
	mu      sync.Mutex
	since   map[types.NamespacedName]time.Time
	stalled map[types.NamespacedName]bool
	gauge   prometheus.Gauge
}

func newProjectionStallTracker(gauge prometheus.Gauge) *projectionStallTracker {
	return &projectionStallTracker{since: map[types.NamespacedName]time.Time{}, stalled: map[types.NamespacedName]bool{}, gauge: gauge}
}

// waiting records another wait for key and returns how long it has waited.
func (t *projectionStallTracker) waiting(key types.NamespacedName, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	since, ok := t.since[key]
	if !ok {
		t.since[key] = now
		return 0
	}
	waited := now.Sub(since)
	if waited >= projectClusterStallBound && !t.stalled[key] {
		t.stalled[key] = true
		t.gauge.Inc()
	}
	return waited
}

// done forgets key once a reconcile no longer waits for its project cluster.
func (t *projectionStallTracker) done(key types.NamespacedName) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stalled[key] {
		t.gauge.Dec()
	}
	delete(t.since, key)
	delete(t.stalled, key)
}
