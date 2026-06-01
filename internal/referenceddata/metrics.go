// SPDX-License-Identifier: AGPL-3.0-only

package referenceddata

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Metrics for the cell-side referenced-data gate clearing path.
//
// All metrics use the prefix "compute_referenced_data_" to group them.
const (
	metricsNamespace   = "compute"
	metricsSubsystem   = "referenced_data"
	metricsNsLabelName = "namespace"
)

var (
	// CompanionsPresent tracks the number of companions present on the cell,
	// aggregated per namespace. Aggregating per namespace (rather than per
	// instance) avoids an unbounded cardinality growth as instances are created
	// and deleted.
	CompanionsPresent = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "companions_present",
			Help: "Number of expected companion ConfigMaps/Secrets that are present " +
				"on the cell, aggregated per namespace. Set at each reconcile while " +
				"instances are waiting for companions.",
		},
		[]string{metricsNsLabelName},
	)

	// CompanionsExpected tracks how many companions the cell expects, aggregated
	// per namespace (from the expected-set annotation). Useful as the denominator
	// when evaluating CompanionsPresent.
	CompanionsExpected = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "companions_expected",
			Help: "Total number of companion ConfigMaps/Secrets expected on the cell, " +
				"aggregated per namespace, as recorded in the expected-referenced-data annotation.",
		},
		[]string{metricsNsLabelName},
	)

	// GateWaitDuration observes how long (in seconds) an Instance spent blocked
	// by the ReferencedData scheduling gate. Observed when the gate is removed.
	GateWaitDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "gate_wait_seconds",
			Help: "Duration in seconds that an Instance waited with the ReferencedData " +
				"scheduling gate before all companions became available.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{metricsNsLabelName},
	)

	// ConditionTransitions counts transitions between ReferencedDataReady reason
	// values on Instances. Labels carry the from/to reason so callers can build
	// state-machine dashboards.
	ConditionTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "condition_transitions_total",
			Help: "Total number of ReferencedDataReady condition reason transitions " +
				"observed on Instances by the cell gate-clearing reconciler.",
		},
		[]string{metricsNsLabelName, "from_reason", "to_reason"},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		CompanionsPresent,
		CompanionsExpected,
		GateWaitDuration,
		ConditionTransitions,
	)
}
