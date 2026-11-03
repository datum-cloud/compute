# Runbook: Shell sessions ending with Unavailable or AgentLost

**Alert:** `ComputeShellSessionsUnavailable`
**Severity:** critical
**Component:** Instance shell sessions (`compute-system/compute-manager` session controller and the shell agents in `compute-shell-system` on each cell)

## What this alert means

Shell sessions bound to one location are ending before a requester can use
them, and the reason is on the platform side:

- **Unavailable** — no cell in the location claimed the session within its
  30-second claim window. The session controller bound it to the cell that
  runs the Instance, but no agent on that cell picked it up.
- **AgentLost** — a cell claimed the session and then stopped reporting on it:
  the agent went away, the cell was deregistered, or the Instance moved.

The alert fires **per location and reason** once the rate of such ends has
stayed above zero for 10 minutes. A single stray end does not fire; a steady
trickle does, because every one of them is a requester who opened a shell and
got nothing.

## Impact

- Requesters in the location cannot open shells into their Instances, or lose
  the ones they had.
- Each failed session still consumed a per-Instance reservation and quota until
  it ended.
- Nothing else about the Instance is affected: the workload keeps running.

## How to confirm

Queries run against the platform metrics store. The session-side metrics are
scraped from `compute-system/compute-metrics`; the agent-side metrics come from
each cell. Use the *Compute / Instance Shell Sessions* dashboard
(`compute-shell-sessions`) for the same signals with the location and cell
pre-selected.

1. **Which reason, how many** — confirm the rate and reason for the location:

   ```promql
   sum by (location, reason) (
     rate(compute_shell_sessions_ended_total{reason=~"Unavailable|AgentLost"}[10m])
   )
   ```

2. **Are sessions still being created there** — the failure is only real if
   there is demand:

   ```promql
   sum by (location) (rate(compute_shell_sessions_created_total[10m]))
   ```

3. **Can the cell's agents reach their endpoint** — Unavailable almost always
   means the agents on the cell cannot serve. A cell with no reachable agent
   fails every session bound to it:

   ```promql
   max by (cell, agent) (compute_shell_agent_endpoint_reachable)
   ```

   `ComputeShellCellEndpointUnreachable` or
   `ComputeShellAgentEndpointUnreachable` firing for a cell in the location is
   the usual companion.

4. **Are the agents claiming at all** — a cell whose agents are up but claim
   nothing is not seeing the sessions; propagation from the hub to the cell is
   the next suspect:

   ```promql
   sum by (cell, outcome) (rate(compute_shell_agent_claims_total[10m]))
   ```

5. **Find one session and follow it** — pick a failed session from the
   project's activity (the `SessionEnded` event carries the UID and the ending
   reason), find the Instance's cell from the Instance's status, and read the
   agent logs on that cell:

   ```sh
   kubectl logs -n compute-shell-system deploy/exec-agent | grep <session UID>
   ```

   On the management plane, the session controller logs the same UID:

   ```sh
   kubectl logs -n compute-system deployment/compute-manager | grep <session UID>
   ```

## Known causes

- **Agents down or unreachable on the cell.** The agent pair on the cell is not
  running, cannot reach the tunnel endpoint, or lost its credentials. Sessions
  end with Unavailable.
- **Sessions not reaching the cell.** The session propagates hub to cell
  through Karmada; a cell whose Karmada membership is unhealthy (unreachable
  taint, stale kubeconfig) never sees the session. Sessions end with
  Unavailable while agents look healthy and idle.
- **Cell stopped reporting.** The cell was deregistered, or its agents were
  restarted while sessions were open. Open sessions end with AgentLost.
- **Instance moved.** The Instance was rescheduled to another cell; the
  session was bound to the old one and ends with AgentLost.

## Remediation

- **Agents:** check `kubectl get pods -n compute-shell-system` on the cell and
  the agent logs. Restart the agent Deployment if it is wedged; sessions that
  were open will end with AgentLost, and new ones will be served.
- **Propagation:** check the cell's Karmada `Cluster` status and whether the
  session objects exist on the cell (`kubectl get instanceconsolesessions -A`).
  Fix the cell's membership; sessions created after that will be served.
- **Endpoint:** follow the endpoint-unreachable runbook when its alert is also
  firing.

Once the cause is fixed, new sessions succeed at once; the alert clears when
the failed-end rate has been zero for the evaluation window.

## Escalation

If the cell's agents and Karmada membership both look healthy and sessions
still end with Unavailable, escalate to the compute team with the location, a
failed session UID, and the agent and controller log lines for that UID.

## Expected steady state (alert cleared)

Ends with reason Unavailable or AgentLost are zero; ends are dominated by
Completed, Expired, Disconnected and Revoked.
