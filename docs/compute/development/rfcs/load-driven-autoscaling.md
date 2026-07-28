---
status: proposed
---

# Load-driven horizontal autoscaling for compute workloads

> Tracks [datum-cloud/enhancements#799](https://github.com/datum-cloud/enhancements/issues/799).

## Table of Contents

- [Summary](#summary)
- [Goal](#goal)
- [Design](#design)
  - [Where this runs](#where-this-runs)
  - [Desired replica ownership](#desired-replica-ownership)
  - [The metrics interface](#the-metrics-interface)
  - [How scaling works](#how-scaling-works)
  - [Status and events](#status-and-events)
- [Alternatives](#alternatives)
- [Failure modes](#failure-modes)
- [What gets built](#what-gets-built)
- [Decisions](#decisions)
- [Open questions](#open-questions)

---

## Summary

Workloads can already declare min/max replicas and scaling metrics, but today nothing uses them — instance count stays pinned to the minimum. This RFC adds a controller that reads CPU/memory usage, compares it to the configured target, and writes a new desired replica target through the `WorkloadDeployment` scale subresource. It follows HPA's basic formula, but reacts faster because our instances start in milliseconds rather than tens of seconds.

## Goal

A pool of identical instances behind a load balancer should grow and shrink with CPU (and memory) usage on its own, without an operator adjusting anything by hand. An autoscaled workload should sit at its minimum under no load, scale up as usage crosses the target, scale back down when it subsides, and show its current state clearly on its status.

**Out of scope for this RFC**:

- Scale-to-zero and waking a workload back up from zero (owned by the snapshot / suspend-resume work).
- Scaling on anything other than CPU/memory, like request latency (a future metric type).
- Workloads that hand out one dedicated instance per user or session — think an AI agent sandbox assigned to a single person — rather than routing many users across one shared, load-balanced set of instances. Growing capacity for that shape means creating more workloads, not adding replicas to one. That's a scheduling/provisioning problem, not horizontal scaling, and isn't addressed here.
- Building the metrics pipeline this design reads from.

## Design

### Where this runs

A new controller inside the existing `bin/manager` process, not a separate service — consistent with how compute already splits concerns into several narrow controllers on the same manager, and with how HPA itself is a controller separate from the one that manages replicas.

Scaling is evaluated per `WorkloadDeployment`, so each placement/location adjusts independently.

### Desired replica ownership

Autoscaling should not use status as input. `WorkloadDeployment` gets a Kubernetes scale subresource from the start, backed by a desired replica field on spec. The autoscaler writes that scale target, `WorkloadDeploymentReconciler` creates/deletes instances from it, and `status.desiredReplicas` remains reporting state. If the desired replica field is unset, the desired count is `scaleSettings.minReplicas`.

Manual scale commands use the same subresource. If autoscaling is enabled, a manual scale is temporary and may be overwritten on the autoscaler's next check; durable bounds live in `scaleSettings.minReplicas` and `scaleSettings.maxReplicas`.

### The metrics interface

Scaling needs current CPU/memory usage for the instances in a `WorkloadDeployment`. The unikraft provider creates local Pods with CPU/memory requests and limits, but it does not currently expose live per-instance usage to Compute. This RFC assumes a fresh per-instance usage source exists — either from an existing local metrics system or new observability work — and is reachable through an interface shaped roughly like this. It does not design that source itself:

```go
type MetricsSource interface {
    Usage(ctx context.Context, instanceIDs []string) ([]InstanceMetrics, error)
}

type InstanceMetrics struct {
    InstanceID string
    Timestamp  time.Time
    Window     time.Duration
    Usage      ResourceUsage
}

type ResourceUsage struct {
    CPU    *resource.Quantity
    Memory *resource.Quantity
}
```

The scaler turns this into whatever shape the configured target needs, the same way HPA turns `PodMetrics` into a comparable value. That's not an extra lookup: the scaler already has each instance's spec in hand, since it needs the instance list to build the ID list passed into `Usage()` in the first place.

CPU/memory resource metrics support `averageUtilization` and `averageValue` targets. `value` is left for future metric types where a single target value makes sense.

The assumptions baked into this shape: readings are fresh on the order of seconds, not minutes; a missing instance is simply absent from the result rather than reported as a silent zero; usage can be requested for exactly the instances in one `WorkloadDeployment` rather than only a platform-wide aggregate; and it's callable from wherever this controller runs, not only from a central endpoint. If the real pipeline can't meet one of these, the design here — the schedule, the dead zone, the scale-down wait, all below — will need to loosen to match it. The initial numbers are tuning defaults, not API guarantees.

### How scaling works

Each `WorkloadDeployment` with metrics configured is checked on a fixed schedule — proposed at every 15 seconds — instead of only when its settings change. A deployment with no metrics configured is untouched by autoscaling: its desired target stays at the minimum unless changed manually through `/scale`.

Each check, in order:

1. **List the deployment's current instances** and query `MetricsSource.Usage()` for them.
2. **Split instances into "known" (the scaler has seen a metric for them before) and "new" (never reported).** New instances are excluded from everything below rather than counted as missing — otherwise a big scale-up would immediately swamp the very next check with brand-new instances and look like a metrics outage, freezing scaling right after the spike it was meant to handle.
3. **Check whether enough known instances reported.** If fewer than half of them have a fresh reading right now, the sample is too thin to trust: hold the current desired target, mark the status as unable to read metrics, emit an event, and stop here. If there is no desired target yet, fall back to the minimum instead, since there's nothing to hold.
4. **Compute a current value per metric**, matching the configured target's shape: average per-instance utilization (usage ÷ the instance's requested CPU/memory) for an `averageUtilization` target, or average raw usage for an `averageValue` target.
5. **Check the dead zone.** If the ratio of current value to target value is within ~10% of 1.0 (HPA's own default), a small, meaningless wobble isn't worth acting on — stop here.
6. **Compute a desired replica count**: `clamp(ceil(current desired target × ratio), min, max)`, per metric, then take the higher of the two if both CPU and memory are configured — same as HPA.
7. **Apply it, depending on direction**:
   - **Scaling up** moves directly to the computed target, capped by `maxReplicas` — if usage says move from 2 desired instances to 12, the target moves to 12. Quota, available capacity, and the deployment's instance management policy still govern how quickly instances are actually created.
   - **Scaling down** waits briefly: use the highest desired count seen over the last 30-60 seconds, so one quiet sample does not immediately shrink the pool.
8. **Write the result** through the `WorkloadDeployment` scale subresource, update the autoscaling status fields (last observed usage, last scale time, current state), and emit an event if it changed. The deployment controller then observes that desired count and reports it back through `status.desiredReplicas`.

The "known" instance set and scale-down window can live in memory. On manager restart or leader handoff, the scaler starts with no known instances and seeds the scale-down window with the current desired target. That may pause scaling for a tick or two while fresh metrics arrive, but it avoids guessing and does not require persisting scaler history.

### Status and events

A `WorkloadDeployment`'s status should show a small autoscaling summary: the usage it last read, when it last changed the replica target, and a condition/reason showing whether scaling is active, capped at min/max, or waiting on metrics. Events are emitted when the desired target changes or metrics are unavailable.

## Alternatives

**Extend `WorkloadDeploymentReconciler` in place instead of a new controller.** Rejected — simpler short-term, but it mixes timer-driven scaling with the event-driven deployment reconciler and couples scaling bugs to instance creation.

**Use HPA's slower defaults.** Rejected — those defaults are meant for slower starts. Keeping them would make fast-start workloads feel sluggish for no real benefit.

**Run the actual Kubernetes HPA controller against workloads.** Rejected — HPA scales Kubernetes workloads through Kubernetes metrics APIs. Our target lives on `WorkloadDeployment`, and usage comes from the instance runtime.

**Wait for the metrics pipeline before starting this work.** Rejected — the real pipeline is required before this can work end-to-end, but the controller, formula, status/events, and tests can be built against the assumed interface now.

## Failure modes

**Metrics are missing or stale.** The scaler never guesses, and never treats missing data as 0% usage (which would shrink to the minimum) or 100% (which would grow to the maximum).

**Thrashing.** Scale-up has no delay, but doesn't run away: current value is recomputed each check against the current desired target, so a genuinely recurring peak converges to a stable size rather than growing indefinitely. What's left is ordinary noise — a stray reading crossing the dead zone adds one extra instance, which the scale-down wait sheds if it doesn't recur. The dead zone and scale-down wait are still the two knobs to adjust; scale-up doesn't need its own.

**Handing off to scale-to-zero.** This design only handles instance counts from 1 upward. Scale-to-zero and wake-on-traffic need more design with the snapshot / suspend-resume work, and may end up as either a separate controller or an extension of this one. For now, this autoscaler does not decide when a workload should go to or wake from zero.

**Instances that must come up in a fixed order.** Ordered or fixed-identity deployments can still receive autoscaled targets, but their instance management policy controls how fast instances are created or deleted. This RFC does not add special ordered-scaling behavior.

## What gets built

- A new controller in `bin/manager` that computes and writes the desired replica target for deployments with metrics configured, on a fixed schedule.
- A `MetricsSource` interface, with a test-only stand-in until the real pipeline exists.
- A ~10% dead zone around the target and a 30-60 second wait before scaling down, both purely to smooth out noisy readings.
- `WorkloadDeployment` gets a scale subresource backed by a spec desired-replica field. `WorkloadDeploymentReconciler` reads the desired count from that field, falling back to `MinReplicas` when it is unset.
- RBAC for the autoscaler to update the `WorkloadDeployment` scale subresource. User access to `/scale` is not required for this RFC.
- Validation that autoscaling has an explicit `maxReplicas`, that `maxReplicas` is greater than `minReplicas`, and that metrics are only accepted with `maxReplicas`.
- New `WorkloadDeployment` status fields showing current usage, when it last scaled, and its current state.
- Events when the desired target changes or metrics are unavailable.
- Memory as a supported resource metric in validation and autoscaler logic, alongside CPU.

Out of scope: the metrics pipeline itself, scale-to-zero, scaling on anything other than CPU/memory, and per-user dedicated instances.

## Decisions

- **A new controller, same process**: not folded into `WorkloadDeploymentReconciler`, not a separate service.
- **Desired replica handoff**: through the `WorkloadDeployment` scale subresource, backed by spec. Status remains reporting state and is not used as input.
- **Autoscaler scope**: the autoscaler only sets the desired replica target. Existing instance management behavior is unchanged: ordered policies may create/delete gradually, while future parallel policies can move faster.
- **Scaling scope**: decisions are per deployment/location. Global rebalancing across locations is follow-on work.
- **Metrics used**: CPU and memory only for now; other metric types can be added later without redesigning this.
- **Schedule**: every 15 seconds, only for deployments with metrics configured.
- **Formula**: HPA's basic ratio formula: compare current usage to target usage, multiply by the current desired target, then clamp to min/max.
- **Reaction speed**: faster than HPA's defaults — a ~10% dead zone and a 30-60 second wait before scaling down; scale-up moves directly to the computed target, capped by `maxReplicas`.
- **Missing data**: hold the current desired target and show it clearly on status; never guess. Before a first successful check, fall back to the minimum instead, same as an unconfigured deployment.
- **Usage vs. percentage**: the metrics interface returns a raw usage rate, mirroring `PodMetrics`; the scaler computes utilization itself from that plus the instance's known request, mirroring HPA. Settled rather than left open, since the scaler already has the instance data on hand.

## Open questions

1. **Metric timing and tuning.** This design assumes seconds-level freshness with clear missing-data signaling. If real metric timing is slower or noisier, the 15s check, dead zone, and scale-down wait need to change.
2. **Scale-to-zero integration.** Once snapshot / suspend-resume is ready, should zero-idle behavior live in this controller or a separate one, and what signal keeps it from fighting normal load-based scaling?
