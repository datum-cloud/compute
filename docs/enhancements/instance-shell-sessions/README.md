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

Let users open a live shell inside a running instance from the CLI, so they can
see and fix problems where they happen. Access follows the project's existing
permissions, and every session is short-lived and recorded.

## Motivation

When an instance misbehaves, users today can see its status and logs but cannot
look inside it. Checking a file, testing a connection or running a quick
command means changing and redeploying the workload, which is slow and can
hide the problem being investigated.

Getting into a running instance is something every major hosting platform
offers. Without it, Datum Compute feels hard to debug.

### Goals

- A user with the right permission can open an interactive shell in a running
  instance of the general-purpose (Kata) runtime class with one command.
- Opening a shell takes about as long as starting a command on the runtime
  itself; the platform adds no noticeable delay.
- Access is granted per project through existing permissions, and every
  session is recorded with who opened it, where, and how it ended.
- A session ends cleanly and explains why: the user exited, it expired, it was
  revoked, or the platform was restarting.
- Shells leave nothing behind: when a session ends, its processes end with it.

### Non-Goals

- Shells from the Cloud Portal. The same client works in a browser, so this can
  follow without changing anything on the cell side.
- Other runtime classes (unikernel, virtual machines).
- Attaching to an instance's main process, port forwarding or file transfer.
- Recording the contents of a session (keystrokes and output).

## Proposal

### What it looks like

```
$ datumctl compute exec web-0 -- sh
/ # cat /etc/app/config.yaml
/ # exit
```

*Illustrative only; exact commands are to be designed.*

### User Stories

#### Story 1: inspect a misbehaving instance

A developer's API returns errors in one location only. They open a shell in an
instance there, find a stale config file and a failing DNS lookup, and fix the
workload without redeploying to investigate.

#### Story 2: a one-off operational task

An operator runs a database migration from inside an instance, using the
instance's own network access and configuration, then exits.

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

| Container | Responsibility |
|---|---|
| **datumctl compute** | Creates the session with a key generated for it, then connects and runs the terminal. The key is never stored. |
| **Project API** | Decides who may open a shell (a new permission on Instance, granted to admins only), stores sessions and records them in the audit log. |
| **Compute controllers** | Send each session to the cell that runs the instance and report its status back to the project. |
| **Relay** | Forwards encrypted traffic between the user and the cell. Both sides connect outward, so cells need no open ports. |
| **Tunnel endpoint** | The cell's internet-facing piece. It holds no credentials and can reach only its shell agent and the relays. |
| **Shell agent** | Claims sessions, checks the instance can take one, verifies the client holds the session's key, runs the shell, and ends its processes when the session ends. |
| **Instance** | The user's Kata VM. The shell reaches it through the runtime, never through the instance's network. |

A session moves through five steps:

1. The user runs `datumctl compute exec`. The CLI creates a session naming the
   instance, the command and the public half of a key it just generated.
2. The Project API checks the user may open shells in this project.
3. Compute delivers the session to the cell running the instance, where a
   shell agent claims it and publishes where to connect.
4. The CLI connects through the relay and proves it holds the session's key.
5. The agent starts the shell. When the user exits, the session expires or it
   is revoked, the agent ends the shell's processes and records how it ended.

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
| Abandoned sessions hold capacity | Connect deadline, per-instance limit and automatic cleanup when an agent stops |
| Security review | Review the key, isolation and cleanup model with the platform security owners before preview |

## Design Details

- **Session resource.** A new `InstanceConsoleSession` in the project names the
  instance, the command, whether it needs a terminal, the client's public key
  and a time-to-live. Its spec cannot change after creation. Its status says
  which agent took it, where to connect, when it must connect by, when it
  expires, and a single `Ready` condition whose reason records how it ended.
- **Runtime capability.** Runtime classes advertise shell support. Sessions for
  instances of other classes are refused.
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
| Kata runtime | Shells fail for instances on the affected node |

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

- 2026-09-29: Local prototype on a Kata cell; this document drafted.

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

- A shell agent and tunnel endpoint deployed to each Kata cell, with network
  policy limiting both.
- Relay capacity and placement reviewed for interactive traffic.
- Audit logging of session creation in the Project API's audit policy.
