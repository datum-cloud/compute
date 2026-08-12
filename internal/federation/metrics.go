// SPDX-License-Identifier: AGPL-3.0-only

// Package federation holds observability for the federation plane's ownership
// invariants: hub write-back Instances are owned by their hub WorkloadDeployment
// and are never created without it. Nothing here drives a cleanup process — the
// single gauge below reports objects that contradict those invariants, so a
// contradiction is a diagnosable ticket rather than a permanent reconcile-error
// ratio.
package federation

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Quarantine reason label values. Each is a state no retry can change.
const (
	// QuarantineReasonUnidentifiable marks a hub Instance whose upstream-owner
	// cluster label is missing or undecodable.
	QuarantineReasonUnidentifiable = "unidentifiable"

	// QuarantineReasonMissingNamespaceLabel marks a hub Instance whose project
	// namespace can never be resolved.
	QuarantineReasonMissingNamespaceLabel = "missing_namespace_label"

	// QuarantineReasonMissingDeploymentName marks a hub Instance whose owning
	// WorkloadDeployment can never be named.
	QuarantineReasonMissingDeploymentName = "missing_deployment_name"

	// QuarantineReasonDeploymentAbsent marks a hub Instance that outlived the
	// project WorkloadDeployment it belongs to. Hub ownership and owner-gated
	// write-back are supposed to make that impossible, so a non-zero series here
	// is an invariant violation to investigate, not a queue of work.
	QuarantineReasonDeploymentAbsent = "deployment_absent"
)

// QuarantinedObjects latches at the number of hub objects the projector has
// given up on, by terminal reason. It stays non-zero for as long as the objects
// exist, which is what makes a broken invariant alertable as a ticket rather
// than as a reconcile-error outage.
var QuarantinedObjects = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "compute_federation_quarantined_objects",
	Help: "Hub objects the projector has quarantined, by terminal reason.",
}, []string{"reason"})

func init() {
	ctrlmetrics.Registry.MustRegister(QuarantinedObjects)
}
