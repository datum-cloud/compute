# Service-Provider Visibility into Workload Health and Consumer Troubleshooting

**Issue:** [datum-cloud/compute#183](https://github.com/datum-cloud/compute/issues/183)
**Status:** Draft
**Related:** [Platform Activity](https://github.com/datum-cloud/enhancements/blob/main/enhancements/platform/activity/README.md)
(the service-provider support persona this builds on, not a parallel audit
surface) · [Service-contributed UI for the cloud and service-provider
portals](https://github.com/datum-cloud/enhancements/pull/820) (the plugin
architecture this view is delivered through) · [Federated Deployment
Scheduling](../federated-deployment-scheduling.md) (how a Workload's status
and placements are actually produced) · [Quota Enforcement Across Deployment
Modes](../quota-enforcement/README.md) (why an Instance can be gated, one of
the states this view surfaces)

---

- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Target Users](#target-users)
  - [Key Workflows](#key-workflows)
  - [Delivery Mechanism](#delivery-mechanism)
  - [User Stories](#user-stories)
- [Design Details](#design-details)
  - [What's already shipped](#whats-already-shipped)
  - [Gap 1: no fleet-wide, health-scoped view](#gap-1-no-fleet-wide-health-scoped-view)
  - [Gap 2: no proactive unhealthy signal](#gap-2-no-proactive-unhealthy-signal)
  - [Gap 3: Events, Logs, and Metrics are placeholders](#gap-3-events-logs-and-metrics-are-placeholders)
  - [Scoping and Access Model](#scoping-and-access-model)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Follow-up Issues](#follow-up-issues)

## Summary

Datum operates compute on behalf of consumers, but the platform team has no
dedicated way to see what's running across the fleet, which workloads are
unhealthy, or what a specific consumer is experiencing when they open a
support request — today that requires ad-hoc cluster access, which doesn't
scale and isn't something support or on-call can be handed.

Part of this already shipped: a staff-portal plugin
([#219](https://github.com/datum-cloud/compute/pull/219),
[#232](https://github.com/datum-cloud/compute/pull/232)) gives a per-project
Workload list and a per-Workload support-detail view (conditions, placements,
per-instance scheduling gates and status), plus a cross-project search
extension that lists Workloads as a resource type on staff-portal's
`/customers/resources`. That covers per-consumer troubleshooting once a
project is known. It does not cover the other two workflows the issue asks
for: a **fleet-wide overview** and a **proactive unhealthy-workload signal** —
both need a health-scoped, cross-project view that nothing existing produces.
This document proposes closing that gap: a **Fleet Health** view, added to the
same provider plugin, that lists Workloads across every project filtered and
sorted by health, linking straight into the existing per-Workload detail page.

## Motivation

Provider-side visibility into compute is limited to `kubectl`/cluster access
today. That works for the engineer who built the feature, not for support or
on-call fielding "why is this workload down" without knowing which project,
location, or component to look at first. The two-view split above — a fleet
list scoped by health, and a per-Workload deep dive — mirrors how the same
problem was already solved for the platform's other big triage surface: a
staff member either starts from "what's broken right now" (fleet list) or
from "here's the specific thing a consumer reported" (drill-down, which
[#232](https://github.com/datum-cloud/compute/pull/232) already provides).

### Goals

- Give service-provider staff a single place to see every Workload across
  every project, filterable by health.
- Surface unhealthy or degraded Workloads proactively — sorted or filtered so
  the fleet's worst-off Workloads are the first thing visible, not something
  a staff member has to know to go look for.
- Let support go from "a consumer's Workload is broken" to the provider-side
  state needed to diagnose it (conditions, placements, scheduling gates,
  quota) without cluster access — already delivered by
  [#232](https://github.com/datum-cloud/compute/pull/232); this document
  keeps it as a goal because the fleet view must link directly into it, not
  duplicate it.
- Reuse the existing provider plugin and its data path (staff-portal's
  same-origin API proxy, the viewing staff member's own session) rather than
  adding a second one.

### Non-Goals

- **Replacing metrics/logs/tracing.** Events, Logs, and Metrics stay
  "Coming Soon" placeholders in the per-Workload view (see [Gap
  3](#gap-3-events-logs-and-metrics-are-placeholders)) — this document
  doesn't change that; it's a separate integration, not a prerequisite for
  fleet health.
- **A new audit/activity surface.** Who-changed-what is the [Platform
  Activity](https://github.com/datum-cloud/enhancements/blob/main/enhancements/platform/activity/README.md)
  service's job. This view shows current state, not history.
- **Alerting or paging.** "Proactive" here means the fleet list surfaces
  unhealthy Workloads when a staff member opens it — it does not mean
  pushing a notification to anyone. On-call is still paged by existing
  infra alerting; this view is where they land afterward to see what's
  actually wrong within compute's resources.
- **Telemetry-derived health.** The issue's reference to the [Telemetry
  system's `MetricDefinitions`](https://github.com/datum-cloud/enhancements/blob/main/enhancements/telemetry/README.md)
  as a possible health signal is noted but out of scope for v1: health here
  stays derived from the `Workload`/`Instance` status conditions compute
  already computes and already ships in
  [#232](https://github.com/datum-cloud/compute/pull/232)'s detail view.
  Folding in telemetry-based health is a natural v2, not a blocker for a
  fleet list that just needs the health enum that already exists.
- **A new backend or API.** This is a UI-only change to the existing provider
  plugin, reading Workloads the same way `WorkloadList` and the resource
  search extension already do. No new compute API, no new credential.
- **Consumer-facing scope.** This is the staff-portal provider surface, not
  cloud-portal — consumers already see their own Workloads there.

## Proposal

### Target Users

- **Support/on-call**, responding to a specific consumer report — served
  today by the per-Workload detail view; unchanged by this proposal beyond
  gaining a second way to arrive there (from the fleet list instead of only
  from `/customers/resources` search or a project's own nav).
- **Platform ops**, doing fleet-wide health sweeps with no specific consumer
  report in hand — the audience this document adds a view for.

### Key Workflows

| Workflow | Status | Where |
|---|---|---|
| Per-consumer troubleshooting (given a project/Workload) | **Shipped** | `WorkloadDetail` — [#232](https://github.com/datum-cloud/compute/pull/232) |
| Fleet overview (what's deployed, across projects) | **Partial** — cross-project search exists but isn't health-scoped | resource-platform search extension — [#219](https://github.com/datum-cloud/compute/pull/219) |
| Unhealthy-workload triage (proactive, fleet-wide) | **Missing** | this document |

### Delivery Mechanism

No new delivery mechanism is needed. The fleet view is a third extension in
the same `ui/provider` plugin
([#219](https://github.com/datum-cloud/compute/pull/219),
[#232](https://github.com/datum-cloud/compute/pull/232)) — staff-portal's
Module Federation plugin host, itself an instance of the architecture
proposed platform-wide in
[enhancements#820](https://github.com/datum-cloud/enhancements/pull/820) and
prototyped in
[cloud-portal#1353](https://github.com/datum-cloud/cloud-portal/pull/1353).
Where `WorkloadList`/`WorkloadDetail` mount as `portal.page/project`
(project-scoped) and the search extension is `portal.resource/platform`
(a data-only GVK declaration staff-portal's own search runs against), the
fleet view is a new top-level `portal.page/platform` extension — a page
mounted outside any single project's route tree, since its entire purpose is
to span all of them. It reads data the same way the existing pages do:
client-side, through staff-portal's same-origin proxy, under the viewing
staff member's own session — no new credential, no plugin-owned backend.

### User Stories

**Fleet sweep, no prior report.** A platform-ops staff member opens the Fleet
Health page with no consumer complaint in hand. Degraded and Unavailable
Workloads sort to the top, each showing its project and a one-line reason
(the condition message most responsible for its current health). They click
through to confirm severity and decide whether it needs action.

**Consumer report, but the project is wrong or forgotten.** A support
engineer knows a customer's workload name from a ticket but not which project
it lives in. They search it on `/customers/resources` (already supported by
[#219](https://github.com/datum-cloud/compute/pull/219)) and land on the same
`WorkloadDetail` page the fleet view links to — this document doesn't change
that path, only adds the other way in.

**Triage during an incident.** A location-wide issue is suspected. A staff
member filters the fleet view by health and scans for a cluster of Workloads
degraded around the same time, in the same location, rather than checking
projects one at a time.

## Design Details

### What's already shipped

`ui/provider/src/schema.ts`'s `Workload` type already carries everything a
health-scoped list needs: `health` (`Available | Degraded | Unavailable |
Unknown`), `readyReplicas`/`desiredReplicas`, `regions`, `conditions`, and
`placements` (each with its own `health` and `conditions`). `WorkloadList`
(`ui/provider/src/pages/workload-list.tsx`) already renders exactly this shape
for one project. The fleet view is that same table, fetched across every
project instead of one, plus a health filter/sort — no new field is needed on
the `Workload` schema itself.

### Gap 1: no fleet-wide, health-scoped view

`WorkloadList` takes a `projectName` from the route and only ever queries one
project (`useWorkloads(projectName)` in `ui/provider/src/lib/api.ts`) — there
is no page that lists Workloads across projects. The resource-platform
extension from [#219](https://github.com/datum-cloud/compute/pull/219) comes
closest: it lets staff-portal's own search list Workloads as a Type filter on
`/customers/resources`. But that's staff-portal's generic resource search,
running as the *viewing staff member's own* credentials — it lists every
Workload matching a search term, with no concept of health, and no
compute-specific filter or sort. It answers "where is this named thing,"
not "what's broken." Closing this gap means a new `portal.page/platform`
extension that fetches Workloads across every project the viewing staff
member can reach and renders them as one table, sortable by health.

### Gap 2: no proactive unhealthy signal

There is currently no view where a degraded Workload is visible without
already knowing to look for it. `WorkloadDetail` shows a Workload's health
once you're already on its page; nothing surfaces it to someone who wasn't
already looking. The fleet view closes this by defaulting its sort to
worst-health-first, so opening the page *is* the proactive signal — no
separate alerting path, consistent with the [Non-Goals](#non-goals) above.

### Gap 3: Events, Logs, and Metrics are placeholders

`WorkloadDetail`'s Events/Logs/Metrics tabs
(`ui/provider/src/pages/workload-detail.tsx`) are intentionally empty —
no data source is wired up for any of the three. This document doesn't close
that gap: the fleet view's "reason" column and the detail page's existing
Conditions tab are drawn from the same `status.conditions` compute already
publishes, which is enough to identify *that* something is wrong and roughly
*why*, without needing logs or metrics wired in first. Wiring those tabs is
tracked as a separate follow-up (see [Follow-up
Issues](#follow-up-issues)), not blocked on or by this document.

### Scoping and Access Model

Unchanged from the shipped provider plugin: staff-portal loads the plugin
under the *viewing staff member's own session*, and every read — the fleet
list included — goes through staff-portal's same-origin API proxy using that
session's own RBAC. A staff member sees exactly the projects and Workloads
their own account is permitted to see; the fleet view has no elevated
credential and cannot show a project a staff member couldn't already reach
by other means. This is the same trust boundary
`ui/provider/README.md`'s `portal.resource/platform` extension already
documents for cross-project search, extended to a second cross-project read.
Consumer-owned data (workload configuration, images, environment) is exposed
read-only, exactly as `WorkloadDetail` already exposes it — no write path
exists in the provider plugin today, and this document doesn't add one.

### Risks and Mitigations

- **Listing every project's Workloads client-side doesn't scale as the fleet
  grows.** Start with pagination and a bound on how many projects are queried
  per page load; revisit server-side aggregation if or when that bound is
  reached. Not a blocker to shipping a first version against today's fleet
  size.
- **A staff member without access to a project shouldn't be able to infer
  its existence from the fleet list.** The scoping model above already
  guarantees this — the list can only ever contain what the viewing staff
  member's own session is permitted to see — but it's worth stating as an
  explicit non-negotiable for this design, not an incidental property.
- **"Reason" derived from raw condition messages can be noisy or
  inconsistent across condition types.** Reuse `ConditionsTable`'s existing
  rendering conventions (`ui/provider/src/components/conditions-table.tsx`)
  rather than inventing a new summarization scheme; refine wording only if
  it proves confusing in practice.

## Drawbacks

- Adds a second cross-project read path (alongside the existing resource
  search extension) that has to keep working as the number of projects
  grows — see [Risks and Mitigations](#risks-and-mitigations).
- Health here is exactly as good as the status conditions Workload/Instance
  controllers already publish. If a controller under-reports a real problem
  (marks something `Available` when it isn't), the fleet view inherits that
  blind spot — it has no independent signal.

## Alternatives

- **Server-side aggregation service that pre-computes fleet health.**
  Rejected for v1 — the existing per-project Workload API already carries
  everything needed for a client-side rollup at today's scale; adding a new
  aggregation backend is more infrastructure than the problem currently
  needs. Worth revisiting if the scalability risk above materializes.
- **Fold fleet health into `/customers/resources`' existing search/filter
  UI instead of a dedicated page.** Rejected — that surface is staff-portal's
  generic cross-service resource browser, not health-aware for any service,
  and teaching it a compute-specific health sort would mean either a
  staff-portal change well outside compute's own repo, or bending a generic
  search into a specialized triage tool it isn't designed to be. A
  compute-owned page keeps the health model where compute already owns it.
- **Wire Events/Logs/Metrics before shipping a fleet view.** Rejected — the
  status conditions already published are sufficient to identify and
  triage most of what a "workload is broken" report needs; gating fleet
  visibility on log/metric integration would delay the more commonly needed
  capability for a less commonly blocking one.

## Follow-up Issues

Per the parent issue's acceptance criteria, implementation issues are opened
once this design is settled. Expected follow-ups:

- Add the `portal.page/platform` fleet extension to `ui/provider`, with the
  health-sorted, cross-project Workload list described above.
- Wire Events/Logs/Metrics data sources into `WorkloadDetail`'s existing
  placeholder tabs (tracked separately, not a dependency of the fleet view).
- Revisit server-side aggregation if the client-side fleet list's scalability
  risk (see [Risks and Mitigations](#risks-and-mitigations)) materializes in
  practice.
