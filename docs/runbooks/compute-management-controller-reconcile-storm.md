# Runbook: Compute management controller reconcile storm

**Alerts:** `ComputeManagementControllerReconcileStorm`, `ComputeManagementControllerWorkqueueAddStorm`
**Severity:** warning
**Component:** compute management controllers (`compute-system/compute-manager`)
**Related:** [datum-cloud/compute#191](https://github.com/datum-cloud/compute/issues/191) (this issue), companion fix in #192

## What this alert means

Compute's management controllers — `workload`, `referenced-data`, and
`workload-deployment-federator` — are re-reconciling idle Workloads in a
self-perpetuating loop. Nothing about the Workloads is actually changing; the
controllers keep waking each other (and themselves) up and doing no useful work.

Each reconcile **succeeds**, so error-based alerting stays silent. The tell is a
high reconcile / enqueue rate while the workqueue stays drained (depth ~0) and
the number of Workloads and WorkloadDeployments is small and stable.

Left unchecked, this wastes control-plane CPU and apiserver capacity and adds
background noise that makes real reconcile activity harder to see. It is a
capacity and observability problem, not (yet) a correctness one.

## Impact

- Wasted control-plane capacity on the compute management plane (CPU on
  `compute-manager`, write load on the project/management apiserver).
- Genuine reconciles are harder to spot amid the churn.
- No direct customer-facing Workload/Instance impact expected while it is only a
  hot loop, but it erodes headroom and should be resolved.

## How to confirm

All queries run against the platform metrics store (`vmcluster-metrics`, e.g. via
the staging Grafana / vmselect). The management-controller metrics are scraped
from `compute-system/compute-metrics` by the platform vmagent.

1. **Reconcile rate by controller** — should be small for a handful of idle
   Workloads; during a storm it sits in the tens-to-hundreds per minute:

   ```promql
   sum by (controller) (
     rate(controller_runtime_reconcile_total{
       controller=~"workload|referenced-data|workload-deployment-federator"
     }[5m])
   ) * 60
   ```

2. **Enqueue rate vs. queue depth** — the smoking gun. A high add rate with a
   depth pinned near zero means items are re-added as fast as they drain. Note
   the workqueue metrics label the controller as `name`, not `controller`:

   ```promql
   # adds per minute (should track the reconcile rate during a storm)
   sum by (name) (
     rate(workqueue_adds_total{
       name=~"workload|referenced-data|workload-deployment-federator"
     }[5m])
   ) * 60

   # depth stays ~0 the whole time
   max by (name) (
     workqueue_depth{name=~"workload|referenced-data|workload-deployment-federator"}
   )
   ```

3. **Object count is tiny and stable** — confirms the rate is not just real work
   from many Workloads:

   ```promql
   count(datum_cloud_compute_workload_info)
   ```

4. **Reconcile errors are flat** — confirms reconciles are succeeding, so do not
   rely on this to detect the loop:

   ```promql
   sum by (controller) (
     rate(controller_runtime_reconcile_errors_total{
       controller=~"workload|referenced-data|workload-deployment-federator"
     }[5m])
   )
   ```

5. **apiserver write cross-check** — if the management/project apiserver metrics
   are available in the same store, a self-perpetuating loop shows up as a steady
   update/patch rate on WorkloadDeployments far above what the object count
   warrants (a single WorkloadDeployment has been observed patched several times
   per second):

   ```promql
   sum(rate(apiserver_request_total{resource="workloaddeployments",verb=~"update|patch"}[5m]))
   ```

   Note: the compute Workload/WorkloadDeployment resources are served by the
   project/management control plane, whose apiserver metrics may not be scraped
   into this store. If the query returns nothing, fall back to the controller and
   workqueue signals above.

6. **Eyeball the logs** — a storm produces a continuous stream of reconcile log
   lines for the same handful of objects:

   ```sh
   kubectl logs -n compute-system deployment/compute-manager -f | \
     grep -E 'workload|referenced-data|workload-deployment-federator'
   ```

   Watch for the same Workload / WorkloadDeployment names recurring many times
   per second with no meaningful spec/status change between reconciles.

## Known root cause

The controllers ping-pong on an annotation: the `workload` controller and the
`referenced-data` controller each write an annotation that the other treats as a
change, so each write re-triggers the other's reconcile, which writes again. The
`workload-deployment-federator` rides the same churn. The result is a steady-state
loop over idle objects.

The fix lives in the controller code and is tracked in companion PR **#192**
(this alerting/runbook PR does not change controller behavior). See
[#191](https://github.com/datum-cloud/compute/issues/191) for the full analysis.

## Remediation

- **Preferred:** roll out the controller fix (PR #192). Once merged and deployed,
  the reconcile and enqueue rates drop back to near zero for idle Workloads and
  both alerts clear on their own.
- **Immediate mitigation** if the loop is degrading the control plane before the
  fix is available: restart `compute-manager`
  (`kubectl rollout restart deployment/compute-manager -n compute-system`). This
  does **not** fix the loop — it will resume once reconciliation restarts — but
  can buy time. Prefer shipping the fix.

## Escalation

If the fix is not yet available and the loop is causing measurable control-plane
saturation (apiserver latency, `compute-manager` CPU throttling), escalate to the
compute team and reference [#191](https://github.com/datum-cloud/compute/issues/191)
and #192.

## Expected steady state (alert cleared)

For a small number of idle Workloads, reconcile and workqueue add rates should be
near zero (occasional single reconciles on real changes only), workqueue depth
~0, and errors flat.
