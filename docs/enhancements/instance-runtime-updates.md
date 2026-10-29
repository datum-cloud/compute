# Keep instances and addresses during upgrades

Tracking: [compute#562](https://github.com/datum-cloud/compute/issues/562), following [infra#7932](https://github.com/datum-cloud/infra/issues/7932).

An image update currently deletes the Instance so its provider creates a new Pod. Deleting the Instance also releases network reservations. A normal upgrade should replace the running process while keeping the Instance, network interfaces, and addresses.

This change adds the compute side of that contract. It is **disabled by default** with `InstanceRuntimeUpdates=false`. It is not a complete fix and must not be enabled in production yet.

## Compute behavior

With the gate enabled, an image change or restart updates the existing Instance and advances `spec.controller.runtimeRevision`. The revision is stored on the Instance, so retries and controller restarts do not advance it again. The resource version prevents concurrent writes from overwriting a newer update. Existing network requests, status, ownership, and Instance UID are retained. Permanent removal still deletes the Instance and follows its existing cleanup policy.

Compute waits for `status.controller.observedRuntimeRevision` and the observed template hash to match before moving to the next instance. A stale Ready condition or a provider echoing the desired hash cannot complete the rollout. A newer desired template may supersede a failed or pending update.

Revision zero represents existing runtimes. The v0.14.1 hash migration does not change this revision or restart them. Once an Instance has a nonzero runtime revision, disabling the gate does not return it to deletion on update. Rolling back to a binary that predates this contract is unsafe after enabling the feature.

The first version accepts sandbox runtime and volume updates with unchanged interface requests, location, execution tier, and instance sizing. Unsupported changes return an error rather than silently deleting the Instance. Template metadata is merged; removing old template metadata is not yet supported. These restrictions must be resolved or clearly exposed before general availability.

## Provider work required before enablement

Both Kata and Unikraft currently leave an existing Pod's spec unchanged and copy the Instance's desired hash into status. Both must adopt this contract:

1. Stamp new Pods with their runtime revision using `pkg/instanceruntime`. Never update that annotation on an existing Pod. A Pod without it is revision zero. Verify owner UID before interpreting the revision.
2. When the desired revision changes, mark the Instance unavailable for the replacement and end sessions attached to the old runtime. Stop the old Pod using a UID precondition; wait for confirmed runtime shutdown and network detachment before starting another Pod with the same interfaces. Pod deletion alone is insufficient when a lost node may still run the old process. Do not clear interface reservations or their addresses.
3. Build the replacement from the latest desired Instance. Handle another update or controller restart during shutdown without resurrecting an older runtime. A deleting Pod or a Pod with the wrong revision cannot satisfy readiness.
4. Report the revision actually running only after the replacement is ready. A hash-format migration may update the observed hash when the runtime revision is unchanged. A real update must never be acknowledged from the old Pod.
5. Keep permanent Instance removal on the existing finalizer path, including runtime termination and network reclamation.

The shared helper validates ownership and revision; it does not implement shutdown, detach, or provider reconciliation.

## Shell sessions and networking still required

Shell session slots record Pod UID, and process cleanup checks it. Session connection and exec still address the Pod by name. With a stable Instance, a same-name replacement could receive an old session; a pre-exec UID check alone leaves a race. Resolve this with runtime-specific Pod names and lookup by Instance ownership/revision, or another mechanism that binds execution to a particular Pod UID. Do not enable in-place updates until connection, reconnect, polling, and cleanup all remain bound to the original runtime.

Confirm network claims belong to the live Instance, are not deleting, and have completed detachment before a replacement can use them. Do not trust a previous owner's Bound/Allocated/Prepared conditions. Implement interface-list changes by name so unchanged interfaces survive; the initial guarded path rejects those changes.

## Required integration validation

Before enabling the gate, run both providers through image updates and explicit restarts and verify that Instance UID, interface UIDs, private/public addresses, and reservations stay unchanged. Cover controller restart during teardown, rapid successive updates, a replacement that never becomes ready, a lost old node, an existing shell session, and permanent Instance deletion. Also verify upgrading existing revision-zero Instances without a requested runtime change causes no restart.

The compute unit tests cover ordered waits, stale provider acknowledgment, concurrent writes, rapid changes, revision-zero hash migration, sticky opt-in, and permanent removal. They do not prove provider shutdown, network handoff, or shell-session isolation.
