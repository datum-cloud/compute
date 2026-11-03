# Runbook: Shell sessions slow to connect

**Alert:** `ComputeShellSessionConnectLatencyHigh`
**Severity:** warning
**Component:** Instance shell sessions (session controller, Karmada propagation, shell agents)

## What this alert means

The time from creating a shell session to a connected client
(`compute_shell_session_connect_seconds`) is the requester's whole experience
of "opening a shell". The 95th percentile in one location has been above 5
seconds for 15 minutes. Steady state is about a second; the design measured
sub-second on real cells.

This is a latency signal, not a failure signal: sessions still connect. It
fires per location.

## Impact

- Requesters in the location wait several seconds for a prompt. Some give up
  and retry, which adds load.
- Sessions that take longer than the 60-second connection window end with
  NotConnected; if p95 climbs toward that, expect
  `ComputeShellSessionsUnavailable`-like symptoms from requesters even though
  the ending reason differs.

## How to confirm

1. **Percentiles by location:**

   ```promql
   histogram_quantile(0.95, sum by (location, le) (rate(compute_shell_session_connect_seconds_bucket[15m])))
   histogram_quantile(0.50, sum by (location, le) (rate(compute_shell_session_connect_seconds_bucket[15m])))
   ```

   A high p95 with a normal p50 points at a subset of cells or Instances; both
   high points at the whole path.

2. **Where the time goes.** The session path is: project API, session
   controller on the management plane, Karmada propagation to the cell, agent
   claim, status back through Karmada aggregation, client connect. Check each
   hop:

   - Session controller reconcile latency and queue depth
     (`controller_runtime_reconcile_time_seconds`, `workqueue_depth` with
     `job="compute-metrics"`).
   - Agent claim rate and outcomes on the location's cells:

     ```promql
     sum by (cell, outcome) (rate(compute_shell_agent_claims_total[5m]))
     ```

   - Karmada health for the location's cells (`Cluster` conditions, work
     apply latency if scraped).

3. **Load.** A creation burst can stretch the path; check
   `ComputeShellSessionVolumeUnusual` and the created rate for the location.

## Known causes

- **Session controller backlog** on the management plane (reconcile storm,
  apiserver throttling, leader churn). Every location is affected at once.
- **Slow Karmada propagation** to one cell (member cluster unreachable
  intermittently, work controller backed up). Only that cell's location is
  affected.
- **Agents slow to claim** because they are restarting, resource-throttled, or
  contending on the per-Instance reservation.
- **Client side** slow to open the tunnel; visible as a gap between the
  session showing SessionReady and the Connected transition.

## Remediation

- Fix the hop that is slow; there is no knob on the session itself.
- If the management-plane controller is the bottleneck, treat it as a
  controller health problem (see the reconcile-storm runbook if that alert is
  also firing).
- If one cell is slow, check its Karmada membership and its agents; restart
  the agent Deployment if the claim rate is low while sessions are pending.

## Escalation

Escalate to the compute team with the location, the p50/p95 values, and which
hop looks slow, when the cause is not visible from the hops above.

## Expected steady state (alert cleared)

p95 time to first prompt at or below about 2 seconds in every location.
