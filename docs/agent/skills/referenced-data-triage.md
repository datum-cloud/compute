# Skill: referenced data triage

Use when `ReferencedDataReady=False`, or the workload reports
`ReferencedDataNotReady`.

## Background

The ConfigMaps and Secrets a workload references are read out of the customer's
project by Datum and delivered to the machines that run the instances. The
`ReferencedDataReady` condition therefore appears in two places: on the
WorkloadDeployment (could Datum read it?) and on the Instance (did it arrive?).
Read both; they fail for different reasons.

Call them ConfigMaps and Secrets. The customer wrote `configMapRef:` or
`secretRef:` in their own workload, so those are already their words;
"referenced data" is not. Quote the object name from the status message
verbatim — that is the part they act on.

## Procedure

1. **`ReferencedDataNotReady` is a pointer.** Read the `ReferencedDataReady`
   condition itself for the real reason.

2. **Map the reason:**

   - `SourceNotFound` — the ConfigMap or Secret does not exist in the project.
     **Customer fixes.** The message names the object and the container that
     references it — quote it. Usual causes: a typo in the name, or it was
     never created in this project.
   - `SourceUnauthorized` — the object is there, but Datum does not have
     permission to read it. **Datum fixes.** Escalate; do not ask the customer
     to recreate anything, and say plainly that their object is fine.
   - `SourceTooLarge` — the object is bigger than Datum allows. **Customer
     fixes**, by shrinking or splitting it.
   - `Resolving` / `AwaitingPropagation` — normal. Being read, or in transit.
     Wait.

3. **Distinguish "not found" from "not there yet."** `SourceNotFound` on the
   deployment means it genuinely is not in the project. `AwaitingPropagation`
   on the instance means it was read fine and is still on its way. Only the
   first is something the customer can act on.

4. **Check every reference.** A workload may use several ConfigMaps and
   Secrets; the condition reports the first one blocking. After it is fixed,
   re-check — another may be waiting behind it.

## Reporting

Quote the object name from the status message. For `SourceNotFound`, the next
step is concrete: create that ConfigMap or Secret in this project, or fix the
name the workload references it by.
