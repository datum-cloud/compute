# Native Pods on a VPC

**Status:** Draft

## Summary

Platform teams need to run a service on every eligible node while giving each
Pod an address in a chosen VPC. Internal DNS is the first use case: one DNS Pod
on each compute and Connect gateway node, with Galactic as its primary network.
The same capability can place telemetry endpoints in consumer VPCs and forward
their traffic to a shared collector.

Compute adds an opt-in attachment path for native Kubernetes Pods, including
DaemonSet Pods. The workload keeps Kubernetes scheduling and rollout behavior.
Each Pod receives its own VPC interface before it starts, and that interface is
its primary `eth0` rather than an additional network.

## User experience

An operator selects a VPC network on a Pod template and opts in to primary VPC
networking. The network must be available in the Pod's namespace. A DaemonSet
can then target the desired node classes using ordinary node selectors,
affinity, and tolerations. New Pods wait for an address and attachment before
they are scheduled; a failed attachment leaves the Pod pending rather than
starting it on the cluster network.

For the DNS rollout, operators can deploy one DaemonSet across compute and
Connect gateway nodes and observe each Pod's claim and conditions when it is
waiting. The same opt-in works for other node services without adding a
DNS-specific workload API.

## Proposed behavior

1. Admission adds a scheduling gate to each opted-in Pod.
2. Compute requests a VPC interface through a Pod-owned
   `NetworkInterfaceClaim`.
3. NSO allocates the address; Cloud prepares a VPC attachment and a unique
   Multus network definition for that Pod.
4. Once the claim is bound, allocated, and prepared, Compute sets the prepared
   definition as the Pod's primary network and removes the scheduling gate.
5. Multus invokes Galactic CNI when Kubernetes starts the Pod. Kubernetes
   garbage collection removes the claim when the Pod is deleted.

The interface is scoped to the Pod UID, so replacement Pods receive distinct
attachments. Compute does not wait for the interface to be programmed before
releasing the gate: programming happens when the Pod sandbox starts.

## Acceptance criteria

- A DaemonSet Pod on each selected compute and Connect gateway node starts with
  a Galactic VPC address on `eth0`.
- An attachment that cannot be prepared leaves its Pod pending, with a visible
  claim condition; it never falls back to the cluster's primary network.
- Replacing a Pod creates a distinct attachment and releases the old claim.
- Ordinary Pods without the opt-in keep their existing admission and network
  behavior.

## Rollout dependencies

This first implementation is feature gated and intended for a cell pilot. Before
enabling the DNS DaemonSet, we need a stable provider-published reference to the
prepared Multus definition, plus an end-to-end check that Multus sees the
annotation added before gate removal. We also need to verify CNI detach and
address release ordering during Pod replacement. Host-originated network probes
need a reachable management path; an in-container exec probe can be used for
the pilot. These checks determine when the capability is ready for general use.
