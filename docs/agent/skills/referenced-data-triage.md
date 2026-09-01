# Skill: referenced data triage

Use when `ReferencedDataReady=False`, or the workload reports
`ReferencedDataNotReady`.

## Background

ConfigMaps and Secrets referenced by a workload template are read by the
management plane from the project namespace and delivered to the cell. The
`ReferencedDataReady` condition appears in two places: on the WorkloadDeployment
(the resolver's view — could it read the source?) and on the Instance (the
cell's view — did the data arrive?). Read both; they fail for different reasons.

## Procedure

1. **`ReferencedDataNotReady` is a pointer.** Read the `ReferencedDataReady`
   condition itself for the real reason.

2. **Map the reason:**

   - `SourceNotFound` — the ConfigMap or Secret does not exist in the project
     namespace. **Customer fixes.** The message names the object and the
     container that references it — quote it. Usual causes: a typo in the
     reference, or the object was never created in this project.
   - `SourceUnauthorized` — the management identity cannot read the object.
     **Platform fault.** The object exists; Datum's RBAC is insufficient.
     Escalate; do not ask the customer to recreate anything.
   - `SourceTooLarge` — the object exceeds the size limit. **Customer fixes**
     by shrinking or splitting it.
   - `Resolving` / `AwaitingPropagation` — transient. Reading, or in flight to
     the cell. Wait.

3. **Distinguish "not found" from "not yet propagated."** `SourceNotFound` on
   the deployment means the source genuinely is not there.
   `AwaitingPropagation` on the instance means it was read fine and is still
   travelling. Only the first is actionable.

4. **Check every reference.** A workload may mount several ConfigMaps and
   Secrets; the condition reports the first blocking one. After it is fixed,
   re-check — another may be waiting behind it.

## Reporting

Quote the object name and namespace from the condition message. For
`SourceNotFound`, the next step is concrete: create that object in that
namespace, or correct the reference.
