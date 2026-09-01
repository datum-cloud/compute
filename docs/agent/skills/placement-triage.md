# Skill: placement triage

Use for `NoMatchingLocation`, `AmbiguousServingLocation`, or `CityCodeMismatch`
on a WorkloadDeployment.

## The one thing to know

**All three are platform faults.** The customer's placement request is not the
problem — the cell's location configuration is. Nothing in the workload spec
will fix any of them, and suggesting spec changes here sends the customer down a
dead end.

## Procedure

1. **Identify which:**

   - `NoMatchingLocation` — the cell has not been told which location it serves,
     so the deployment cannot be assigned one.
   - `AmbiguousServingLocation` — more than one location was delivered to the
     cell. The cell refuses to guess and waits for the platform to resolve it.
   - `CityCodeMismatch` — the deployment asked for one city and the cell serves
     another. It was placed on the wrong cell.

2. **Confirm the scope.** `workloads_list` shows whether other workloads in the
   same placement are also failing. Several workloads failing in one location is
   a cell-level problem and is worth reporting as such; a single one may be a
   stale deployment.

3. **Check whether other placements are serving.** A workload with several
   placements may be fully available elsewhere. Say so — the customer's service
   may be up even though this deployment is broken.

4. **Escalate with specifics.** Datum needs: the WorkloadDeployment name, its
   `cityCode`, its (empty or wrong) `location`, and the condition message. Pull
   these from `workloads_get`.

## Reporting

Lead with the fact that this is Datum's to fix. Then say whether the workload is
still serving from another placement, and hand over the escalation details.
Do not offer a workload-spec workaround.
