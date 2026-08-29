// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

// Reason constants for placement outcomes that depend on the runtime class a
// workload selected. They live alongside the other WorkloadDeployment.Available
// reasons in the same stable, machine-readable vocabulary clients match on.
const (
	// WorkloadDeploymentReasonRuntimeClassNotServed is set on
	// WorkloadDeployment.Available when no cell in the deployment's city
	// advertises the runtime class it selected. Without it the federated
	// scheduler simply never places the deployment, and the customer sees a
	// workload that sits there with nothing to act on; the class and the
	// location are both theirs to change.
	WorkloadDeploymentReasonRuntimeClassNotServed = "RuntimeClassNotServed"
)
