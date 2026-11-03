# Runbook: Unusual shell session volume

**Alert:** `ComputeShellSessionVolumeUnusual`
**Severity:** warning
**Component:** Instance shell sessions (project API, session controller)

## What this alert means

Sessions created in one location over the last hour exceed five times that
location's average hourly count over the past week, and the hour had at least
20 sessions. The floor keeps a quiet location's first genuinely busy hour from
firing on a near-zero baseline.

Shell sessions are a privileged path into customer Instances. A burst is
usually benign (a new customer onboarding, a demo, a migration script), but
it is also what a compromised credential, a runaway automation, or a client
stuck in a retry loop looks like. The alert exists so someone looks.

## Impact

- None directly; sessions are served normally. Per-Instance limits
  (`TooManySessions`) and project quota still apply.
- A burst can stretch time to first prompt; see
  `ComputeShellSessionConnectLatencyHigh`.

## How to confirm

1. **The hour against the baseline:**

   ```promql
   compute:shell_sessions_created:increase1h
   avg_over_time(compute:shell_sessions_created:increase1h[7d])
   ```

2. **Which projects:**

   ```promql
   topk(10, sum by (project) (increase(compute_shell_sessions_created_total{location="<location>"}[1h])))
   ```

3. **How the sessions end.** A retry loop shows as many short sessions
   ending NotConnected, Invalid, InstanceNotFound or TooManySessions; real
   use shows Completed, Expired and Disconnected:

   ```promql
   sum by (reason) (increase(compute_shell_sessions_ended_total{location="<location>"}[1h]))
   ```

4. **Who is creating them.** The project's audit log records each session
   creation with the requesting user or service identity. One identity
   creating hundreds of sessions in minutes is the thing to look at.

## Known causes

- **Automation or CI** that opens a session per step or per Instance.
- **Client retry loop** after a failure it does not understand (for example
  a session ending NotConnected because the client never opened the tunnel).
- **Onboarding or a demo** in a location that was previously quiet.
- **Abuse** with a leaked credential.

## Remediation

- Benign burst: nothing to do; the baseline catches up within the week.
- Retry loop: contact the project owner with the identity and the ending
  reason pattern; fix the client.
- Suspected abuse: follow the platform's credential revocation process for the
  identity and revoke the open sessions by deleting them; escalate to
  security.

## Escalation

Escalate to security and the compute team together when a single identity
accounts for the burst and the project owner does not recognise it.

## Expected steady state (alert cleared)

Hourly creation counts within a few multiples of the weekly average per
location.
