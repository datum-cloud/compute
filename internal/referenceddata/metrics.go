// SPDX-License-Identifier: AGPL-3.0-only

package referenceddata

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Metrics for the cell-side referenced-data gate clearing path.
//
// All metrics use the prefix "compute_referenced_data_" to group them.
var (
	// CompanionsPresent tracks the ratio of companions present on the cell
	// relative to the total number expected, per instance. It is set to
	// present/expected at each reconcile; set to 1 when the gate clears.
	CompanionsPresent = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "compute",
			Subsystem: "referenced_data",
			Name:      "companions_present",
			Help: "Number of expected companion ConfigMaps/Secrets that are present " +
				"on the cell for an instance. Set to the expected total once all " +
				"companions arrive and the gate is cleared.",
		},
		[]string{"namespace", "instance"},
	)

	// CompanionsExpected tracks how many companions the cell expects for each
	// instance (from the expected-set annotation). Useful as the denominator
	// when evaluating CompanionsPresent.
	CompanionsExpected = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "compute",
			Subsystem: "referenced_data",
			Name:      "companions_expected",
			Help: "Total number of companion ConfigMaps/Secrets expected on the cell " +
				"for an instance, as recorded in the expected-referenced-data annotation.",
		},
		[]string{"namespace", "instance"},
	)

	// GateWaitDuration observes how long (in seconds) an Instance spent blocked
	// by the ReferencedData scheduling gate. Observed when the gate is removed.
	GateWaitDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "compute",
			Subsystem: "referenced_data",
			Name:      "gate_wait_seconds",
			Help: "Duration in seconds that an Instance waited with the ReferencedData " +
				"scheduling gate before all companions became available.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"namespace"},
	)

	// ConditionTransitions counts transitions between ReferencedDataReady reason
	// values on Instances. Labels carry the from/to reason so callers can build
	// state-machine dashboards.
	ConditionTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "compute",
			Subsystem: "referenced_data",
			Name:      "condition_transitions_total",
			Help: "Total number of ReferencedDataReady condition reason transitions " +
				"observed on Instances by the cell gate-clearing reconciler.",
		},
		[]string{"namespace", "from_reason", "to_reason"},
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
