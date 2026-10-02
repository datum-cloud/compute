# Runbook: Shell session cleanup unconfirmed

**Alert:** `ComputeShellSessionCleanupUnconfirmed`
**Severity:** warning
**Component:** Instance shell sessions (session controller on the management plane, shell agents on the cell)

## What this alert means

When a shell session ends, the session controller waits for the cell to
confirm that the session's command has stopped before it forgets the session.
If the cell does not confirm within the cleanup timeout, the controller gives
up waiting, records a `CleanupUnconfirmed` warning event in the project's
activity, and increments `compute_shell_agent_cleanup_unconfirmed_total` for
the cell.

The alert fires when any cell has an unconfirmed cleanup in the last hour. It
is deliberately sensitive: a process the platform started may still be running
inside a customer's Instance.

## Impact

- A shell command may still be running in the Instance after the requester's
  session is gone. For an interactive shell that usually means an orphaned
  shell process; for a long-running command it means work continuing without
  anyone attached.
- The session's per-Instance reservation and quota were released when the
  session ended, so the Instance can accept new sessions.

## How to confirm

1. **Which cell, how many:**

   ```promql
   sum by (cell) (increase(compute_shell_agent_cleanup_unconfirmed_total[1h]))
   ```

2. **Which sessions.** Each affected session has a `CleanupUnconfirmed`
   event in its project's activity, alongside its `SessionStarted` and
   `SessionEnded` events, with the session UID, Instance and container.

3. **What the cell saw.** On the cell, search the agent logs for the UID:

   ```sh
   kubectl logs -n compute-shell-system deploy/exec-agent | grep <session UID>
   ```

   and check whether the cell's copy of the session still exists:

   ```sh
   kubectl get instanceconsolesessions -A | grep <session name>
   ```

4. **Is the process still there.** If the Instance is reachable, open a fresh
   session and look for the orphaned command, or check the container's
   process list from the cell.

## Known causes

- **Agent restarted between the end and the confirmation.** The surviving
  agent should pick up the cleanup, but if both restarted the confirmation
  is lost. The process usually did stop.
- **Cell lost or Instance moved** while the session was ending: there is no
  cell left to confirm. Sessions that end with AgentLost are not counted
  here; this alert is for a cell that was reachable but silent.
- **Karmada status aggregation stalled**, so the cell confirmed but the hub
  never saw it. The process did stop; only the bookkeeping is late.
- **Command ignored the stop signal** and the agent could not end it within
  the timeout.

## Remediation

- Check the cell's copy of the session and the agent logs. If the command
  stopped and only the confirmation was lost, nothing more is needed.
- If the command is still running inside the Instance, end it from a fresh
  session or by restarting the Instance's container, coordinating with the
  project owner.
- If Karmada aggregation is stalled for the cell, fix the cell's membership;
  the same stall will affect session status generally.

## Escalation

Escalate to the compute team when a cell accumulates unconfirmed cleanups
across several sessions, or when a command cannot be stopped from the cell.

## Expected steady state (alert cleared)

`compute_shell_agent_cleanup_unconfirmed_total` does not increase.
