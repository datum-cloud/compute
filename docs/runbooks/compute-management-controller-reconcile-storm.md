# Runbook: Compute controller reconcile storm

**Alerts:** `ComputeControllerReconcileStorm`, `ComputeControllerWorkqueueAddStorm`
**Severity:** warning
**Component:** compute controllers (`compute-system/compute-manager`)
**Related:** [datum-cloud/compute#191](https://github.com/datum-cloud/compute/issues/191) (one known instance), companion fix in #192

## What this alert means

A reconcile storm is a generic controller-runtime pathology: a compute
controller re-reconciles the same objects in a self-perpetuating loop. Nothing
about the objects is actually changing; the controller keeps waking itself (or a
peer controller) up and doing no useful work.

The alert is **per controller** and evaluates across every controller
`compute-manager` reports — management-plane controllers (`workload`,
`referenced-data`, `workload-deployment-federator`, `instance-projector`, ...)
and cell-side controllers (`instance`, `workloaddeployment`, quota, ...) alike,
including any controller added later. **The firing `controller` / `name` label
tells you which controller is storming and therefore where to look.**

Each reconcile **succeeds**, so error-based alerting stays silent. The tell is a
high reconcile / enqueue rate while the workqueue stays drained (depth ~0) and
the number of objects for that controller's resource is small and stable.

Left unchecked, this wastes control-plane CPU and apiserver capacity and adds
background noise that makes real reconcile activity harder to see. It is a
capacity and observability problem, not (yet) a correctness one.

## Impact

- Wasted control-plane capacity on the compute control plane (CPU on
  `compute-manager`, write load on the project/management or cell apiserver).
- Genuine reconciles are harder to spot amid the churn.
- No direct customer-facing Workload/Instance impact expected while it is only a
  hot loop, but it erodes headroom and should be resolved.

## How to confirm

All queries run against the platform metrics store (`vmcluster-metrics`, e.g. via
the staging Grafana / vmselect). The compute controller metrics are scraped from
`compute-system/compute-metrics` by the platform vmagent, so `job="compute-metrics"`
scopes every query to `compute-manager`. Filter on the firing `controller` label
to focus on the storming controller.

1. **Reconcile rate by controller** — should be small for a handful of idle
   objects; during a storm the offending controller sits in the
   tens-to-hundreds per minute:

   ```promql
   sort_desc(
     sum by (controller) (
       rate(controller_runtime_reconcile_total{job="compute-metrics"}[5m])
     ) * 60
   )
   ```

2. **Enqueue rate vs. queue depth** — the smoking gun. A high add rate with a
   depth pinned near zero means items are re-added as fast as they drain. Note
   the workqueue metrics label the controller as `name`, not `controller`:

   ```promql
   # adds per minute (tracks the reconcile rate during a storm)
   sort_desc(
     sum by (name) (
       rate(workqueue_adds_total{job="compute-metrics"}[5m])
     ) * 60
   )

   # depth stays ~0 the whole time
   max by (name) (workqueue_depth{job="compute-metrics"})
   ```

3. **Object count is tiny and stable** — confirms the rate is not just real work
   from many objects. Pick the metric for the firing controller's resource, e.g.
   for `workload`:

   ```promql
   count(datum_cloud_compute_workload_info)
   ```

4. **Reconcile errors are flat** — confirms reconciles are succeeding, so do not
   rely on this to detect the loop:

   ```promql
   sum by (controller) (
     rate(controller_runtime_reconcile_errors_total{job="compute-metrics"}[5m])
   )
   ```

5. **apiserver write cross-check** — if the relevant apiserver metrics are
   available in the same store, a self-perpetuating loop shows up as a steady
   update/patch rate on the storming controller's resource far above what the
   object count warrants (during the #191 incident a single WorkloadDeployment
   was patched several times per second):

   ```promql
   sum(rate(apiserver_request_total{resource="workloaddeployments",verb=~"update|patch"}[5m]))
   ```

   Note: compute resources are served by the project/management or cell control
   plane, whose apiserver metrics may not be scraped into this store. If the
   query returns nothing, fall back to the controller and workqueue signals
   above.

6. **Eyeball the logs** — a storm produces a continuous stream of reconcile log
   lines for the same handful of objects:

   ```sh
   kubectl logs -n compute-system deployment/compute-manager -f | grep -i "<controller>"
   ```

   Watch for the same object names recurring many times per second with no
   meaningful spec/status change between reconciles.

## Known causes

The root cause is controller-specific — the firing label is your starting point.
A confirmed example is an **annotation ping-pong** between the `workload` and
`referenced-data` controllers: each writes an annotation the other treats as a
change, so each write re-triggers the other's reconcile, which writes again; the
`workload-deployment-federator` rides the same churn. That specific loop is
tracked in [#191](https://github.com/datum-cloud/compute/issues/191) with the fix
in companion PR **#192**.

More generally, look for anything that makes a controller re-enqueue its own (or
a peer's) objects without a real change: writes on every reconcile (status/
annotation/label churn), watches that fire on self-authored updates, or requeues
with a near-zero backoff. This alerting/runbook change does not alter controller
behavior; it only surfaces the pattern.

## Remediation

- **Identify the controller** from the firing `controller` / `name` label and use
  the queries above to confirm the drained-queue fingerprint.
- **If it is the #191 loop:** roll out the controller fix (PR #192). Once merged
  and deployed, the reconcile and enqueue rates for those controllers drop back
  to near zero and both alerts clear on their own.
- **For a newly surfaced controller:** inspect what that controller writes on
  each reconcile and which watch/requeue re-triggers it; the fix is to stop
  re-enqueuing on self-authored, no-op changes.
- **Immediate mitigation** if the loop is degrading the control plane before a
  fix is available: restart `compute-manager`
  (`kubectl rollout restart deployment/compute-manager -n compute-system`). This
  does **not** fix the loop — it will resume once reconciliation restarts — but
  can buy time. Prefer shipping the fix.

## Escalation

If no fix is available and the loop is causing measurable control-plane
saturation (apiserver latency, `compute-manager` CPU throttling), escalate to the
compute team. Reference the firing controller and, if it is the known instance,
[#191](https://github.com/datum-cloud/compute/issues/191) and #192.

## Expected steady state (alert cleared)

For a small number of idle objects, reconcile and workqueue add rates should be
near zero (occasional single reconciles on real changes only), workqueue depth
~0, and errors flat.
