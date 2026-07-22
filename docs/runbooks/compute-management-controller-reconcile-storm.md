# Runbook: Compute controller reconcile storm

**Alerts:** `ComputeControllerReconcileStorm`, `ComputeControllerWorkqueueAddStorm`
**Severity:** warning
**Component:** compute controllers (`compute-system/compute-manager`)

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
   object count warrants (a single object being patched several times per second
   is a strong signal):

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
One common shape is a **metadata write-fight** between two controllers: each
writes a piece of metadata (an annotation, label, or status field) that the other
treats as a change, so each write re-triggers the other's reconcile, which writes
again. A related shape is a single controller that writes on every reconcile and
then wakes itself on its own update.

More generally, look for anything that makes a controller re-enqueue its own (or
a peer's) objects without a real change: writes on every reconcile (status /
annotation / label churn), watches that fire on self-authored updates, or
requeues with a near-zero backoff.

## Remediation

- **Identify the controller** from the firing `controller` / `name` label and use
  the queries above to confirm the drained-queue fingerprint.
- **Find what re-triggers it:** inspect what that controller writes on each
  reconcile and which watch or requeue wakes it again. Look for a metadata
  write-fight with a competing controller (two controllers editing the same
  object) or a controller reacting to its own updates. The fix is to stop
  re-enqueuing on self-authored, no-op changes; once deployed the reconcile and
  enqueue rates drop back to near zero and both alerts clear on their own.
- **Immediate mitigation** if the loop is degrading the control plane before a
  fix is available: restart `compute-manager`
  (`kubectl rollout restart deployment/compute-manager -n compute-system`). This
  does **not** fix the loop — it will resume once reconciliation restarts — but
  can buy time. Prefer shipping the fix.

## Escalation

If no fix is available and the loop is causing measurable control-plane
saturation (apiserver latency, `compute-manager` CPU throttling), escalate to the
compute team, naming the firing controller and the observed reconcile / enqueue
rates.

## Expected steady state (alert cleared)

For a small number of idle objects, reconcile and workqueue add rates should be
near zero (occasional single reconciles on real changes only), workqueue depth
~0, and errors flat.
