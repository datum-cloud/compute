# Runbook: Shell agent cannot reach its cell endpoint

**Alerts:** `ComputeShellAgentEndpointUnreachable` (warning, one agent),
`ComputeShellCellEndpointUnreachable` (critical, every agent on a cell)
**Component:** shell agents (`compute-shell-system/exec-agent` on each cell)

## What this alert means

Each shell agent on a cell serves sessions through the cell's tunnel endpoint
and reports whether it can reach it (`compute_shell_agent_endpoint_reachable`,
1 or 0). An agent that cannot reach the endpoint is running but cannot serve
anything.

- The **warning** fires when one agent has reported unreachable for 5 minutes.
  Agents run in pairs per cell, so the other agent is carrying the cell alone.
- The **critical** fires when every agent on the cell has reported unreachable
  for 5 minutes. No session bound to that cell can be served; each one ends
  with Unavailable after its claim window, and
  `ComputeShellSessionsUnavailable` follows for the location.

## Impact

- Warning: no requester-visible impact yet, but the cell has no redundancy
  for shell sessions.
- Critical: no shell sessions into any Instance on the cell. Workloads are
  unaffected.

## How to confirm

1. **Which agents, which cell:**

   ```promql
   max by (cell, agent) (compute_shell_agent_endpoint_reachable) == 0
   ```

2. **Is the agent running** — on the cell:

   ```sh
   kubectl get pods -n compute-shell-system -l app.kubernetes.io/name=exec-agent
   kubectl logs -n compute-shell-system deploy/exec-agent
   ```

3. **Is the endpoint up** — the tunnel endpoint is a separate Deployment in
   the same namespace, and network policy lets only the agents reach it:

   ```sh
   kubectl get pods -n compute-shell-system
   kubectl get networkpolicies -n compute-shell-system
   ```

4. **Are sessions already failing** — if the critical variant is firing,
   check the location's failed-end rate:

   ```promql
   sum by (location, reason) (
     rate(compute_shell_sessions_ended_total{reason=~"Unavailable|AgentLost"}[10m])
   )
   ```

## Known causes

- **Endpoint Deployment down or rolling.** Both agents lose it at once; the
  critical fires until the endpoint is back.
- **Network policy change** on the cell that no longer allows agent to
  endpoint traffic. Both agents lose it at once.
- **Agent credentials or relay configuration stale** after a rotation. Often
  one agent at a time as they restart.
- **Cilium or node networking fault** isolating one node; only the agent on
  that node is affected.

## Remediation

- Restart the endpoint or the agent Deployment on the cell once the cause is
  clear; agents reconnect and the gauge returns to 1.
- If a network policy or relay configuration was changed, revert it or fix the
  selector; do not loosen the policy beyond agent to endpoint.
- If the endpoint cannot be recovered quickly on a cell that has customer
  Instances, the cell-side failure surfaces to requesters as Unavailable;
  follow the sessions-unavailable runbook for the communication side.

## Escalation

If both agents and the endpoint are Running and the gauge stays 0, escalate to
the compute team with the cell name and the agent logs from the last 10
minutes.

## Expected steady state (alert cleared)

`compute_shell_agent_endpoint_reachable` is 1 for every agent on every cell.
