---
status: provisional
stage: alpha
latest-milestone: "v0.x"
---

# Instance Shell Sessions

**Status:** Provisional
**Related:** [CLI-first Compute experience](https://github.com/datum-cloud/enhancements/issues/823)
(which set this capability aside for separate tracking) ·
[Runtime Classes](../runtime-classes/README.md) (how a class advertises what it
supports) · [Federated Deployment Scheduling](../federated-deployment-scheduling.md)
(how work reaches the cell that runs an instance)

---

- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [What it looks like](#what-it-looks-like)
  - [User Stories](#user-stories)
  - [Architecture](#architecture)
  - [Guardrails](#guardrails)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
- [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure Needed](#infrastructure-needed)

## Summary

Let users open a live shell inside a running instance from the CLI or the Cloud
Portal, so they can see and fix problems where they happen. It works the same
for general-purpose and unikernel instances. Access follows the project's existing
permissions, and every session is short-lived and recorded.

## Motivation

When an instance misbehaves, users today can see its status and logs but cannot
look inside it. Checking a file, testing a connection or running a quick
command means changing and redeploying the workload, which is slow and can
hide the problem being investigated.

Getting into a running instance is something every major hosting platform
offers. Without it, Datum Compute feels hard to debug, especially for users who
have found a problem in the portal and have nowhere to go from there.

### Goals

- A user with the right permission can open an interactive shell in a running
  instance of the general-purpose (Kata) or unikernel (Unikraft) runtime class
  with one command, or with one click in the Cloud Portal, and the experience
  is the same on both runtimes.
- Opening a shell takes about as long as starting a command on the runtime
  itself; the platform adds no noticeable delay.
- Access is granted per project through existing permissions, and every
  session is recorded with who opened it, where, and how it ended.
- A session ends cleanly and explains why: the user exited, it expired, it was
  revoked, or the platform was restarting.
- Shells leave nothing behind: when a session ends, its processes end with it.

### Non-Goals

- The virtual machine runtime class.
- Attaching to an instance's main process, port forwarding or file transfer.
- Recording the contents of a session (keystrokes and output).

## Proposal

### What it looks like

```
$ datumctl compute exec web-0 -- sh
/ # cat /etc/app/config.yaml
/ # exit
```

In the Cloud Portal, an instance's page gets an **Open shell** action that opens
a terminal in the browser.

*Illustrative only; exact commands and UI are to be designed.*

### User Stories

#### Story 1: inspect a misbehaving instance

A developer's API returns errors in one location only. They open a shell in an
instance there, find a stale config file and a failing DNS lookup, and fix the
workload without redeploying to investigate.

#### Story 2: a one-off operational task

An operator runs a database migration from inside an instance, using the
instance's own network access and configuration, then exits.

#### Story 3: from the portal to a fix

A user notices an unhealthy instance on its portal page, opens a shell from the
same page, confirms a full disk and clears it, without switching tools.

#### Story 3: controlled access

A project admin can open shells; a viewer cannot. Afterwards, the team can see
who opened a shell into which instance and when.

### Architecture

Access is decided once, centrally, when a session is created. The shell itself
travels straight from the user to the cell that runs the instance, encrypted
end to end, so the control plane is never in the data path and cells accept no
inbound connections.

![C4 container diagram](./c4-container-diagram.png)

Source: [c4-container-diagram.puml](./c4-container-diagram.puml)

### Guardrails

| Guardrail | Effect |
|---|---|
| Permission on session creation | Only users granted it (admins by default) can open shells |
| One-time key | Nothing to leak or rotate; deleting the session revokes it |
| One connection, one command | A session can't be reused or turned into a different command |
| Short-lived | Must connect within 60 seconds; lasts at most one hour |
| Per-instance limit | At most three shells per instance at a time |
| Clear endings | Users see why a session ended; the audit log records it |
| Cleanup | A shell's processes end with its session, even if the connection drops |
| Isolation | The internet-facing tunnel endpoint holds no credentials and can reach only its agent |

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| A shell is a powerful form of access | Admin-only permission, short sessions, audit of every session, one command per session |
| The Go iroh library has one maintainer | Pin the version; test it against the Rust tunnel endpoint in CI; keep a small Rust helper as a fallback |
| Relay distance adds latency | Use Datum's regional relays; measure session start from real networks before preview |
| The browser client is a large download | Load it only when a terminal opens (about 3 MB compressed); cache it between visits |
| Abandoned sessions hold capacity | Connect deadline, per-instance limit and automatic cleanup when an agent stops |
| Runtimes behave differently inside | A shell check when each session is claimed; an instance without a usable shell is refused with a clear reason. Verify process cleanup and scale-to-zero behaviour on unikernels in staging |
| Security review | Review the key, isolation and cleanup model with the platform security owners before preview |

## Design Details

- **Session resource.** A new `InstanceConsoleSession` in the project names the
  instance, the command, whether it needs a terminal, the client's public key
  and a time-to-live. Its spec cannot change after creation. Its status says
  which agent took it, where to connect, when it must connect by, when it
  expires, and a single `Ready` condition whose reason records how it ended.
- **Runtime capability.** Runtime classes advertise shell support, and sessions
  for classes without it are refused. The shell agent works through each
  provider's instances the same way, so a runtime needs no shell-specific code
  in compute:
  - **General-purpose (Kata):** supported by the runtime natively.
  - **Unikernel (Unikraft):** supported by the Unikraft exec plugin, which the
    unikraft provider already enables per cell through its exec policy.
- **Instances that are not running.** A unikernel instance scaled to zero, or
  any stopped instance, cannot take a session; the user is told why.
- **Routing.** Instances record which cell runs them, and sessions follow the
  same federation path as the workloads they target.
- **Ending reasons.** Completed, Expired, Revoked, NotConnected, AgentShutdown,
  AgentLost, TooManySessions, NoShell, InstanceNotFound and Invalid, each with
  a user-facing message.
- **Resilience.** Agents run in pairs per cell. An agent restarting for
  maintenance warns open shells and closes them at a deadline; an agent that
  crashes has its sessions ended and cleaned up by the other.

A local prototype on a Kata cell proved this design end to end, including
crash recovery, the per-instance limit, clean endings and network isolation.

<<[UNRESOLVED open decisions]>>
- Which team owns the shell agent and tunnel endpoint.
- Whether tunnel keys live in Secret Manager or are generated and kept in each
  cell.
- Whether the tunnel endpoint reuses the Datum Connect code or is a small
  dedicated binary.
<<[/UNRESOLVED]>>

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

- **Enable / disable:** a compute feature gate, plus the shell capability on
  each runtime class. Disabling either stops new sessions; open sessions end
  with a clear reason and their processes are cleaned up.
- **Default behavior:** unchanged. Nothing reaches an instance until a user
  with the new permission creates a session.
- **Rollback:** safe at any time. Instances and workloads are unaffected.

### Rollout

Staging cells first, then a preview for selected projects, then production
cells through the normal release tag.

### Monitoring Requirements

- **For users:** each session's `Ready` condition and reason.
- **For operators:** sessions opened, active and ended by reason; time from
  creation to first output; agent health per cell.

### Dependencies

| Dependency | Impact of an outage |
|---|---|
| Relays | New and open shells in the affected region fail; instances are unaffected |
| Federation to cells | New sessions are not delivered; open shells continue |
| Kata runtime | Shells fail for Kata instances on the affected node |
| Unikraft exec plugin | Shells fail for unikernel instances on the affected node |

### Scalability

One new API type, one object per session, created only on user action and
bounded by the per-instance limit and session lifetime.

### Troubleshooting

| Failure | What the user sees |
|---|---|
| Instance not running or missing | `InstanceNotFound` before connecting |
| Image has no shell | `NoShell` before connecting |
| Agent restarting | A maintenance warning, then "start a new session" |
| Agent crashed | `AgentLost`; the shell's processes are cleaned up |

## Implementation History

- 2026-09-29: Local prototype on a Kata cell, from the CLI and from a browser
  (Chromium and WebKit); this document drafted. The
  unikernel path relies on the Unikraft exec plugin the unikraft provider
  already supports.

## Drawbacks

- A new always-on component in every cell, and a new data path through the
  relays to operate.
- A shell is broad access to an instance; misconfigured permissions would
  matter more than for read-only features.

## Alternatives

| Alternative | Why not |
|---|---|
| Proxy shells through the control plane | Puts every keystroke through the core and needs an inbound path into cells |
| Reach cells through the federation hub's cluster proxy | Cells connect outward only, and it would give the core cluster-admin reach into every cell |
| A tunnel into the instance's private network (SSH) | Needs software in the image and breaks when the network is the thing being debugged |
| Store a long-lived key per user | Adds registration, rotation and revocation for no gain over a key per session |
| Reuse the Datum Connect desktop client | No local API, not distributed as a CLI, and licensed incompatibly with datumctl |

## Infrastructure Needed

- A shell agent and tunnel endpoint deployed to each compute cell, with
  network policy limiting both.
- The Unikraft exec policy enabled on cells that run unikernel instances.
- Relays served over TLS, with browser access allowed on their latency probe, so
  portal terminals can connect.
- Relay capacity and placement reviewed for interactive traffic.
- Audit logging of session creation in the Project API's audit policy.
