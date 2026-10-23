# Network interfaces for Pods

**Status:** Proposed

Builds on [Native Pods on a VPC](native-pod-vpc-attachment.md).

## Summary

A Pod lists the networks it needs, and compute creates, names, and wires the
network interface claims for it. Nobody writes claims by hand, copies a bound
address into settings, or keeps claims in GitOps.

Internal DNS is the first user. Its resolvers need a second interface, `net1`,
on the producer network, and they need to keep the same address across
restarts and rollouts. Today infra declares each claim in Git and copies each
bound address and attachment name into deployment settings.

## What a Pod declares

```yaml
metadata:
  labels:
    compute.datumapis.com/network-interfaces-enabled: "true"
  annotations:
    compute.datumapis.com/network-interfaces: |
      [{"network": "internal-dns-producer", "interface": "net1",
        "claimName": "internal-dns-node", "reclaimPolicy": "Retain"}]
```

Each entry has:

| Field | Meaning | Default |
| --- | --- | --- |
| `network` | Network in the Pod's namespace | Required |
| `interface` | Device name in the Pod. `eth0` replaces the primary network; any other name is added beside it | `eth0` |
| `claimName` | Name of the claim. Set it to keep the address when the Pod is replaced | Derived |
| `reclaimPolicy` | `Delete` or `Retain`, copied to the claim | `Delete` |

The existing `primary-network` opt-in keeps working and means one `eth0` entry.
A Pod uses one opt-in or the other, not both.

## How compute names claims

A claim's name decides whether a new Pod gets the old address back. Network
services binds a retained interface to the next claim with the same name.

1. `claimName`, when set. Use it for single-replica Deployments.
2. `<pod name>-<interface>`, when a StatefulSet owns the Pod. StatefulSet Pod
   names are stable.
3. `pod-<pod UID>-<interface>` otherwise, so every Pod gets a fresh address.

Admission rejects `Retain` without a stable name. A UID-named claim is never
asked for again, so its address would stay reserved forever.

## What compute does

1. Admission validates the list and adds a scheduling gate.
2. Compute creates one claim per entry, owned by the Pod. Pod labels under
   `compute.datumapis.com/` are copied to the claim, and network services
   carries them on to the interface.
3. If a claim of that name already exists with no owner and the same network,
   interface, and policy, compute adopts it. This lets existing GitOps claims
   move over without changing address.
4. If the claim belongs to another Pod, or is being deleted, compute waits.
   The claim name works as a lock, so two Pods never hold one interface.
5. Once every claim is bound, allocated, and prepared, compute sets the Multus
   default network for `eth0`, adds the other interfaces to the Pod's Multus
   networks list, and writes each interface's address to
   `compute.datumapis.com/address-<interface>`.
6. In a separate write, compute removes the gate.

A container reads its address through the downward API:

```yaml
env:
  - name: LISTENER_ADDRESS
    valueFrom:
      fieldRef:
        fieldPath: metadata.annotations['compute.datumapis.com/address-net1']
```

The address is the claim's first address without its prefix length. For a
delegated IPv6 block it is the first address of the block.

## Lifecycle

| Event | Result |
| --- | --- |
| Container restarts | Nothing changes |
| Pod replaced, `Delete` | The old claim and interface go away; the new Pod gets a new address |
| Pod replaced, `Retain` | The old claim goes away, the interface stays, and the new Pod's claim binds it again with the same address |
| Workload deleted, `Retain` | The interface stays reserved until someone deletes it |

Rolling updates that start the new Pod before stopping the old one do not work
with a fixed claim name: the new Pod waits for a claim the old Pod still holds.
Use `Recreate` or a StatefulSet.

## Moving Internal DNS off GitOps

1. Ship this change to the staging cells.
2. Move one resolver at a time, node-b first because it takes no traffic. Set
   `claimName` to the claim name already in Git, read the address through the
   downward API, and drop the copied Multus networks value.
3. Check that the address did not change and the resolver goes Ready.
4. When all five have moved, remove the claims and copied settings from Git.
   The Flux Kustomization that applies them orphans on delete, so the claims
   stay for compute.

## Not covered

- Network services does not yet copy labels to the VPC attachment, where
  Internal DNS picks which resolver serves each VPC. Those tier labels stay in
  Git until it does.
- Resolver configs still list each other's addresses. Retain keeps those
  addresses fixed, but they are still copied.
- Compute does not release retained interfaces after their workload is gone.
  A follow-up can find and alert on them.
- The late Multus annotation and CNI detach ordering still need an end-to-end
  check on the resolver runtime class, as noted in the native Pod design.
