# Skill: placement triage

Use for `NoMatchingLocation`, `AmbiguousServingLocation`, or `CityCodeMismatch`
on a WorkloadDeployment.

## The one thing to know

**All three are Datum's to fix.** The customer's placement request is not the
problem — Datum's setup for the location is. Nothing they change in their
workload will clear any of them, and suggesting a change sends them down a dead
end.

## Procedure

1. **Identify which:**

   - `NoMatchingLocation` — Datum has not finished setting up the location this
     part of the workload was sent to, so it has nowhere to run.
   - `AmbiguousServingLocation` — the location's setup contradicts itself; it
     has been given more than one identity. Datum holds the workload rather
     than starting it somewhere it may not belong.
   - `CityCodeMismatch` — the workload asked for one city and was sent to
     another. It was routed to the wrong place.

2. **Confirm the scope.** `workloads_list` shows whether other workloads in the
   same placement are also failing. Several failing in one place is a
   location-wide problem and is worth reporting as such; a single one may be a
   leftover deployment.

3. **Check whether other placements are serving.** A workload with several
   placements may be fully available elsewhere. Say so — the customer's service
   may be up even though this part is broken.

4. **Escalate with specifics.** Datum needs: the WorkloadDeployment name, its
   `cityCode`, its (empty or wrong) `location`, and the status message. Pull
   these from `workloads_get`.

## Reporting

Lead with the fact that this one is Datum's to fix. Then say whether the
workload is still serving from another placement, and hand over the escalation
details. Do not offer a workaround in the workload.
