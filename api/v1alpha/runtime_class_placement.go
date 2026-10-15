// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

// Reason constants for placement outcomes that depend on the runtime class a
// workload selected. They join the other WorkloadDeployment.Available reasons
// in the same stable, machine-readable vocabulary that clients match on.
const (
	// WorkloadDeploymentReasonRuntimeClassNotServed is set on
	// WorkloadDeployment.Available when no cell in the deployment's city
	// advertises the runtime class the deployment selected. Without the reason,
	// the federated scheduler never places the deployment and the customer has
	// no signal to act on. The customer can change either the class or the
	// location.
	WorkloadDeploymentReasonRuntimeClassNotServed = "RuntimeClassNotServed"
)
