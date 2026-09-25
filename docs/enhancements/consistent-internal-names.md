# Consistent Internal Names for Workloads

**Issue:** [datum-cloud/compute#367](https://github.com/datum-cloud/compute/issues/367)
**Status:** Draft
**Related:** [Federated Deployment Scheduling](./federated-deployment-scheduling.md) (how
workloads reach locations) · [Workload Status Reasons](https://github.com/datum-cloud/compute/pull/136)
(where the new failure reason is defined)

---

## Summary

A workload with a long name never starts, and the customer is never told why. Compute builds
the name of every internal object from the workload name, the placement name and the
location name joined together. Once that string passes 63 characters, Kubernetes rejects
the objects that run the workload. Today that happens with workload names as short as 40
characters, and the exact limit depends on which location the workload lands in.

Compute will name its internal objects the same way every time: a short, readable piece of
the workload name plus a fixed-length hash. Name length stops depending on the location or
placement, so any valid workload name of up to 63 characters runs everywhere. When a
workload cannot start for any other reason, its status says so.

## Motivation

A customer picks a descriptive name, the API accepts it, and nothing runs. There is no
error in the workload's status, no event, and no instance. Production hit this on
2026-09-25: a 46-character workload failed in every location, while a workload 6 characters
shorter in the same project ran.

The limit is invisible and moving:

| Runtime | Longest workload name that runs today |
|---|---|
| General purpose | 42 characters, or 45 in a location with a shorter name |
| Unikernel | 40 characters, fewer at 10 or more replicas |

Adding a location with a longer name, or a placement with a longer name, silently lowers
the limit for every customer deploying there.

The same naming scheme also lets two different workloads collide. Workload `app-a` with
placement `b` and workload `app` with placement `a-b` produce the same internal name, and
one overwrites the other.

### Goals

- Any workload name the API accepts runs in every location, with any placement name and
  any replica count.
- The API tells a customer up front when a name is invalid, instead of accepting it and
  failing later.
- A workload that cannot start says why in its status.
- Two workloads can never share an internal name.
- Operators can still find every internal object from the workload, placement and location
  names.

### Non-Goals

- Workload names longer than 63 characters. 63 is the standard Kubernetes limit for names
  used as labels and DNS names, and compute tools select workloads by name.
- Changing how customers name or address a workload. Workload names are unchanged.

## Proposal

### What customers see

- **Creating a workload:** names must be a valid DNS label of up to 63 characters. Anything
  else is rejected at create time with a clear message. The check applies to new workloads
  only, so existing workloads are untouched.
- **Instances:** instance names follow the new internal format, for example
  `checkout-api-production-east-7c1e04b9a2-0`. The name starts with the workload name, so
  it stays recognizable. The location is shown as a field on the instance, not packed into
  the name.
- **Failures:** if an instance cannot be created, the workload reports it as unavailable,
  with the reason and the underlying error.

### Naming format

Every internal object derived from a workload in one location is named:

```
<first 30 characters of the workload name>-<10-character hash>
```

- The hash covers the workload's unique ID, the placement name and the location. Two
  workloads can never share a name, and a workload that is deleted and recreated with the
  same name gets fresh internal objects instead of reusing stale ones.
- The name is at most 41 characters, so instance names stay within 45 characters even at
  999 replicas. That leaves room under every 63-character limit in the chain: instance
  labels, pod host names and unikernel service names.
- The format is the same for every workload. There is no threshold at which a name
  switches to a different shape.

The full workload, placement and location names stay on each object as labels. Operators
and tools find objects by those labels or by ownership, never by parsing a name.

### Rollout

1. **Report failures and validate names.** Ship the failure reason in workload status and
   the 63-character name check together. Customers get a clear answer immediately, even
   before the new names land.
2. **New naming for new deployments.** Controllers match deployments by workload, placement
   and location instead of by name, and create new ones in the new format. Controllers that
   report instance status back to the customer look deployments up by ownership, not by a
   name label.
3. **Migrate running workloads.** For each existing deployment, start the replacement under
   its new name, wait until it is healthy, then remove the old one. Production has 18
   deployments across 9 projects as of 2026-09-25, so this is a small, controlled move
   with no downtime.
4. **Test the edges.** End-to-end tests with a 63-character workload name, a 63-character
   placement name, the longest location name and 10 or more replicas, on both runtimes.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Migration restarts customer instances | Make-before-break: the new instance is healthy before the old one is removed |
| Instance names change for the 9 affected projects | Instances are replaced during migration anyway; the workload name is unchanged |
| Something parses instance or deployment names, for example to recover the location | Audit the portals, CLI and metrics before phase 3; the location moves to a field |
| Stricter create-time validation rejects names accepted today | Names over 63 characters never ran; the rare shorter name with dots is rejected only on create. Call out the change in release notes |

## Alternatives

- **Shorten names only when they are too long.** Fewer names change, but it keeps two
  formats, a threshold edge case and the collision between workloads.
- **Reject long names and change nothing else.** Quick, but the limit still depends on the
  location and placement, so customers cannot know it in advance.
- **Fully opaque internal names.** Removes all length concerns, but instance names become
  unrecognizable to customers and operators with no gain over a readable prefix.
- **Allow workload names up to 253 characters.** Breaks the tools that select workloads by
  name, and every internal label would need hashing too.

## Open Questions

- Should the migration run automatically on upgrade, or as a one-time operator step?
- Should workload names also start with a letter, so providers can use them directly as
  service names?
