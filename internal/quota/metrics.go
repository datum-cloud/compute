// SPDX-License-Identifier: AGPL-3.0-only

package quota

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Metric reason label values for quota_eval_failures_total.
const (
	ReasonBackendUnavailable    = "backend_unavailable"
	ReasonProjectNotFound       = "project_not_found"
	ReasonNamespaceNotFound     = "namespace_not_found"
	ReasonMisconfigured         = "misconfigured"
	ReasonProjectIDUnresolvable = "project_id_unresolvable"
	ReasonNoBudget              = "no_budget"
)

var (
	// EnforcementEnabled is a gauge set to 1 when quota enforcement is active
	// (a credential path is configured) and 0 when disabled (no path configured).
	// This gives dashboards and alerting a stable signal rather than log scraping.
	EnforcementEnabled = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "compute_quota_enforcement_enabled",
		Help: "1 if quota enforcement is active, 0 if disabled (no credential configured).",
	})

	// EvalFailuresTotal counts quota evaluation failures by reason code.
	// Incremented each time quota evaluation fails for a reason other than the
	// normal quota-exceeded or quota-pending flow.
	EvalFailuresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "compute_quota_eval_failures_total",
		Help: "Total quota evaluation failures by reason code.",
	}, []string{"reason"})

	// ClaimOrphanedTotal counts ResourceClaims orphaned during instance deletion
	// because the project ID could not be resolved at deletion time.
	ClaimOrphanedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "compute_quota_claim_orphaned_total",
		Help: "Total ResourceClaims orphaned because project ID could not be resolved at deletion.",
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		EnforcementEnabled,
		EvalFailuresTotal,
		ClaimOrphanedTotal,
	)
}
