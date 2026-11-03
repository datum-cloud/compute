# Skill: placement triage

Use for `NoMatchingLocation`, `AmbiguousServingLocation`, `LocationMismatch`, or
`RuntimeClassNotServed` on a WorkloadDeployment or a Workload.

## The one thing to know

**Three of these are Datum's to fix, and one is not.** `NoMatchingLocation`,
`AmbiguousServingLocation` and `LocationMismatch` are Datum's setup for the
location, and nothing the customer changes in their workload clears them.
`RuntimeClassNotServed` is different: the workload asked for a runtime class in
a location that does not offer it, and the customer can change that. Identify
which one it is before saying anything about who acts.

## Datum's location setup

1. **Identify which:**

   - `NoMatchingLocation` — Datum has not finished setting up the location this
     part of the workload was sent to, so it has nowhere to run.
   - `AmbiguousServingLocation` — the location's setup contradicts itself; it
     has been given more than one identity. Datum holds the workload rather
     than starting it somewhere it may not belong.
   - `LocationMismatch` — the workload asked for one location and was sent to
     another. It was routed to the wrong place.

2. **Confirm the scope.** `compute_workloads_list` shows whether other
   workloads in the same placement are also failing. Several failing in one
   place is a location-wide problem and is worth reporting as such; a single one
   may be a leftover deployment.

3. **Check whether other placements are serving.** A workload with several
   placements may be fully available elsewhere. Say so — the customer's service
   may be up even though this part is broken.

4. **Escalate with specifics.** Datum needs: the WorkloadDeployment name, its
   `location`, and the status message. Pull these from `compute_workloads_get`.

Do not offer a workaround in the workload for any of these three.

## `RuntimeClassNotServed`: the class is not offered there

Every workload runs in one runtime class, the one it named or the default Datum
filled in when it was created. Before anything is placed, Datum checks whether
the location the placement targets serves that class. If nothing there does,
the placement is refused and no instance is created. The status message names
the class and the location. A deployment that is already serving is not
flagged.

This is a separate question from whether the class itself is usable. The
RuntimeClass's own `Available` status can be `True` while a given location
still does not offer it.

1. **Read the class and the location** from the status message, or from
   `compute_workloads_get`. The class is final on the workload.

2. **Check the other placements.** If the same class is served in another
   placement, the workload may already be up there. Say so.

3. **Lay out the two real options, and be plain about their cost:**

   - **Change where it runs.** Placements can be edited on the existing
     workload: point this placement at a location that offers the class, or
     remove it. Nothing in these tools lists which locations offer which class,
     so say that a different location is a guess to try, not a known answer.
   - **Change the class.** The runtime class of an existing workload cannot be
     changed, so this means creating a new workload in a class this location
     offers, and deleting the old one once the new one is serving. Load
     `workload-create` for that, and pick the class from what `resources_list`
     returns for RuntimeClass, not by trying names.

4. **If the customer expected this location to offer the class** — it did
   before, or Datum said it would — that is worth raising with Datum alongside
   the workload name, the location and the class. Do not present it as Datum's
   fault by default: the refusal itself is accurate.

## Reporting

Lead with who acts. For the three location-setup reasons, that is Datum: say
whether the workload is still serving from another placement, and hand over the
escalation details. For `RuntimeClassNotServed`, say which class was asked for
and where, whether another placement is serving, and the two options above,
including that a different class means a new workload.
