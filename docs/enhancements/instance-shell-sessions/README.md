---
status: provisional
stage: alpha
latest-milestone: "v0.x"
---

# Instance shell sessions

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
  - [Non-goals](#non-goals)
- [Proposal](#proposal)
  - [What it looks like](#what-it-looks-like)
  - [User stories](#user-stories)
  - [Architecture](#architecture)
  - [Guardrails](#guardrails)
  - [Risks and mitigations](#risks-and-mitigations)
- [Design details](#design-details)
- [Production readiness review questionnaire](#production-readiness-review-questionnaire)
- [Implementation history](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure needed](#infrastructure-needed)

## Summary

Let users and customer-controlled AI agents open a live shell or run a one-off
command inside a running instance to inspect its state and diagnose problems.

## Motivation

When an instance misbehaves, users today can see its status and logs but cannot
look inside it. Checking a file, testing a connection or running a quick
command means changing and redeploying the workload, which is slow and can
hide the problem being investigated.

Getting into a running instance is something every major hosting platform
offers. Without it, Datum Compute feels hard to debug, especially for users who
have found a problem in the portal and have nowhere to go from there.

### Goals

- A user or customer-controlled AI agent with the right permission can run an
  interactive shell or one-off command in a running general-purpose (Kata) or
  unikernel (Unikraft) instance. Both runtimes use the same API and client
  workflow when the runtime and image meet the session requirements.
- The p95 time from accepted session creation to an interactive prompt or
  noninteractive process start is less than five seconds on supported
  production networks.
- The project activity log provides a complete history of session access.
- Every session ends with an actionable explanation.
- When a session ends, the platform stops every process that it started. File
  writes and other changes inside the instance remain until the instance is
  replaced.

### Non-goals

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

### User stories

#### A developer inspects a misbehaving instance

A developer sees that an API fails in one location. They choose an affected
instance, open a shell and trace the problem to a stale configuration file and
a failing DNS lookup.

#### A customer's AI agent investigates a failure

A customer-controlled AI agent detects a failed health check, creates a session
through the API with its project service identity, runs a targeted diagnostic
command and reports its findings.

### Architecture

After session creation, command traffic travels straight from the client to the
cell that runs the instance. Traffic is encrypted end to end, the control plane
is not in the data path and cells accept no inbound connections.

![C4 container diagram](./c4-container-diagram.png)

Source: [c4-container-diagram.puml](./c4-container-diagram.puml)

#### End-to-end flow

The client can be `datumctl`, the Cloud Portal or a customer-controlled AI
agent. Each uses the same API and data path.

![Instance shell session sequence](./sequence-diagram.png)

Source: [sequence-diagram.puml](./sequence-diagram.puml)

### Guardrails

- **Permission on session creation:** Creating `InstanceConsoleSession`
  resources requires the new session-create permission. The default
  project-admin role gets it; viewer roles do not. AI agent identities require
  an explicit grant
- **Single-use session:** The first authenticated connection consumes the
  session and can run only the requested command. The agent rejects replays and
  command changes; deleting the session closes an active connection
- **Short-lived:** The cell gives clients a 60-second connection window and
  limits command execution to one hour
- **Per-instance limit:** At most three sessions can run in an instance at once
- **Session quota:** Each project can hold at most 100 sessions at once. The
  quota system enforces this when a session is created, and projects can
  request more
- **Isolation:** Cell network policy lets the tunnel endpoint reach only its
  agent

### Risks and mitigations

- **Misconfigured authorization grants broad access:** Test default role
  bindings and alert operators to unusual session volume
- **The client private key grants access:** Keep CLI keys in process memory
  only. The browser client must hold the raw key in its worker memory for the
  Go iroh library. Never write it to browser storage, and clear it when the
  terminal closes
- **Client library maintenance depends on one person:** Pin the Go iroh version;
  test it against the Rust tunnel endpoint in CI; keep a small Rust helper as a
  fallback
- **Relay distance can miss the five-second p95 goal:** Review relay placement
  and capacity; do not enable preview in a region until staging meets the goal
- **Automated clients can overload session control paths:** Enforce the
  project session quota and per-instance limit; alert on sustained quota
  denials
- **The browser client is a large download:** Load it only when a terminal opens
  (about 3 MB compressed); cache it between visits
- **A Datum Connect regression breaks shells:** Qualify each release in CI
  before rolling it out to cells
- **Security review:** Review the key, isolation and cleanup model with the
  platform security owners before preview

## Design details

### Proposed API

`InstanceConsoleSession` is a project-scoped
`compute.datumapis.com/v1alpha` resource. A client creates one resource for one
command in one container. Deleting it revokes the session.

```yaml
apiVersion: compute.datumapis.com/v1alpha
kind: InstanceConsoleSession
metadata:
  # Clients use generateName because each resource represents one invocation.
  generateName: web-0-
spec:
  # The whole spec is immutable. To change a request, delete it and create one.

  # Required name and UID of an Instance in the same project. The UID prevents
  # the session from reaching a replacement Instance that reused the name.
  instanceRef:
    name: web-0
    uid: 3d39d44d-93fb-4d78-a14a-d76a9876aa71

  # Required exact container name. Clients fill this automatically when the
  # Instance has one container and require a choice when it has several.
  containerName: app

  # Required argument vector, not a shell string. It accepts 1–64 elements and
  # at most 16 KiB in total. The image must contain this executable and a
  # supported shell, which the agent uses to manage the process group.
  command: ["sh"]

  # Optional; defaults to false. Keeps standard input open when true, including
  # without a terminal.
  stdin: true

  # Optional; defaults to false. Allocates a pseudoterminal and merges output
  # streams when true. false preserves separate standard output and error.
  terminal: true

  # Required 32-byte iroh public key as 64 lowercase hexadecimal characters.
  # The private key remains in client memory and never enters the API.
  clientPublicKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

  # Optional command lifetime, measured from startedAt. Defaults to 15m and
  # cannot exceed 1h.
  ttl: 15m
status:
  # All status fields are output only.
  connection:
    # Public endpoint identity and relay addresses; neither is a bearer secret.
    endpointID: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
    relayURLs:
      - https://iroh-relay.us-central-1.datumconnect.net
    # host:port the client names when it opens the tunnel stream.
    target: exec-agent-0.exec-agent.compute-shell-system.svc.cluster.local:7777

  # Absolute deadline set from the shell agent's clock when it publishes the
  # connectable status. Status propagation consumes part of the 60-second
  # window, so clients should connect immediately.
  connectBefore: "2026-09-29T18:01:00Z"

  # Set when the command starts and calculated as startedAt plus spec.ttl.
  # startedAt: "2026-09-29T18:00:10Z"
  # expiresAt: "2026-09-29T18:15:10Z"
  # endedAt: "2026-09-29T18:04:32Z"

  # Set only when the runtime reports a normal process exit. datumctl returns
  # this value; without one, it returns nonzero and shows the condition reason.
  # exitCode: 0

  # Ready is Unknown while pending, True while connectable or connected, and
  # False after rejection or termination. reason identifies the exact phase.
  conditions:
    - type: Ready
      status: "True"
      reason: SessionReady
      message: "Connect before 2026-09-29T18:01:00Z"
      lastTransitionTime: "2026-09-29T18:00:01Z"
      observedGeneration: 1
```

### Session lifecycle

A session moves through `Pending`, `SessionReady` and `Connected`, then reaches
one immutable terminal state. Terminal reasons are `Completed`, `Expired`,
`Revoked`, `NotConnected`, `AgentShutdown`, `AgentLost`, `TooManySessions`,
`NoShell`, `CommandUnavailable`, `InstanceNotRunning`, `InstanceNotFound`,
and `Invalid`. The client uses the condition reason to distinguish a session
waiting for a connection from one whose command is running.

Deleting an active session revokes it. A finalizer waits up to five minutes for
the cell to stop the process and release its reservation. If the cell does not
confirm cleanup, the controller records `CleanupUnconfirmed` and removes the
finalizer; it never reports cleanup as confirmed. This cleanup outcome is a
lifecycle event, not a replacement for the immutable terminal reason.

The controller deletes a session as soon as it reaches a terminal state and its
end event is recorded, which releases its quota. A connected client receives
the ending reason and exit code in the stream's closing message. A client whose
session ended before it connected reads them from the session's final state.
Audit records outlive the resource.

### Runtime and routing

- **Runtime capability.** Runtime classes advertise the `exec` feature, and
  sessions for classes without it are refused. The agent also checks that the
  selected image contains a supported shell and the requested executable
  before accepting a session:
  - **General-purpose (Kata):** The runtime supports exec natively.
  - **Unikernel (Unikraft):** The Unikraft exec plugin is on by default for
    every instance. The provider's exec policy is `always` in every cell that
    runs unikernel instances.
- **Routing.** Instances record which cell runs them, and sessions follow the
  same federation path as the workloads they target.
- **Audit record.** The Project API audit pipeline records session creation
  with the requesting user or service identity as the actor. The compute
  session controller emits start and end events, where the actor is the
  controller. All three events include the session UID, project, instance and
  container, so the activity API and Cloud Portal can present one timeline with
  the requester, timestamps and ending reason. The events follow the project's
  audit-log retention policy and do not depend on the session resource. This
  requires a new activity policy, not Milo code changes.
- **Concurrency.** Before claiming a session, an agent atomically acquires one
  of three per-instance reservations by using Kubernetes resource-version
  checks. It releases the reservation when the session ends. The surviving
  agent releases stale reservations after agent failure.
- **Resilience.** Agents run in pairs per cell. An agent restarting for
  maintenance warns open shells and closes them at a deadline; an agent that
  crashes has its sessions ended and cleaned up by the other.

A local prototype on a Kata cell proved this design end to end, including
crash recovery, the per-instance limit, clean endings and network isolation.

### Decisions

- **Ownership:** compute owns the shell agent and tunnel endpoint so one team
  operates the cross-runtime session path. Runtime providers supply exec behind
  the common interface.
- **Tunnel keys:** the shell agent creates an endpoint identity key and stores
  it in a secret local to its cell. The tunnel endpoint mounts the secret
  read-only and has no Kubernetes API credentials. The key identifies the
  endpoint to clients but does not authorize a session, so a leak is confined to
  one cell. The agent rotates the secret every 30 days and immediately after
  suspected compromise. Rotation ends open sessions on that endpoint with
  `AgentShutdown`; clients can create new sessions.
- **Scale-to-zero:** opening a shell does not wake an instance. A session for a
  stopped instance fails before connecting and tells the user to select a
  running instance or raise the workload's minimum replica count. This avoids
  changing workload scale or incurring cost as a side effect of a diagnostic
  action.
- **Tunnel code:** the tunnel endpoint runs the Datum Connect CLI, so shells
  and Datum Connect share one tunnel implementation. Its license prevents
  linking it into `datumctl`; it does not prevent cells from running it as a
  separate program.

## Production readiness review questionnaire

### Feature enablement and rollback

- **Enable / disable:** a compute feature gate and the `exec` feature on each
  runtime class control the feature. Disabling either switch
  stops new sessions; open sessions end with a clear reason and their processes
  are cleaned up.
- **Rollback:** Disable the feature without changing instances or workloads.

### Rollout

Staging cells first, then a preview for selected projects, then production
cells through the normal release tag. Before preview, staging must validate the
latency goal and verify Unikraft behavior for missing shells, process cleanup,
idle sessions and scale-to-zero.

### Monitoring requirements

- **For users:** each session's `Ready` condition and reason.
- **For operators:** sessions opened, active and ended by reason; time from
  creation to the first interactive prompt, or to process start and first byte
  for a noninteractive command; agent health per cell.

### Dependencies

- **Relays:** New and open sessions in the affected region fail; instances are
  unaffected
- **Federation to cells:** New sessions are not delivered; open sessions continue
- **Kata runtime:** Sessions fail for Kata instances on the affected node
- **Unikraft exec plugin:** Sessions fail for unikernel instances on the affected
  node
- **Project API:** New sessions cannot be created; open data paths continue.
  Controllers retry terminal status and event updates
- **Activity system:** Sessions continue and the Project API audit record
  remains authoritative. Activity views can lag until event delivery recovers

### Scalability

One new API type, one object per open session, created only on a client
request and bounded by the project session quota and per-instance limit.
Sessions are deleted as soon as they end. Tests must cover simultaneous session
creation, quota release, reservation release and agent failover.

### Troubleshooting

- **Instance not running:** Tell the user to select a running instance or raise
  the workload's minimum replica count.
- **Instance missing:** Prompt the user to refresh the instance list.
- **Shell or command unavailable:** Tell the user to deploy an image with a
  supported shell and the requested executable.
- **Too many sessions:** Show the limit and ask the user to close a session or
  wait for one to end.
- **Session quota reached:** Tell the user the project has too many open
  sessions and ask them to close one or retry shortly.
- **Relay unreachable:** Retry another advertised relay and report a regional
  connectivity issue if none work.
- **Agent restart or crash:** Show `AgentShutdown` or `AgentLost`, then let the
  user create a new session.

## Implementation history

- 2026-09-29: Local prototype on a Kata cell, from the CLI and from a browser
  (Chromium and WebKit); this document drafted.

## Drawbacks

- Every cell runs an agent and tunnel endpoint, which adds operating cost and
  another failure surface.
- Relays carry interactive traffic and need regional capacity.
- Commands can mutate a running workload, so authorization and auditing
  require more scrutiny than read-only diagnostics.
- The browser client adds download weight, and automated clients add API and
  audit volume.

## Alternatives

- **Proxy shells through the control plane:** Puts every keystroke through the
  core and needs an inbound path into cells
- **Reach cells through the federation hub's cluster proxy:** Cells connect
  outward only, and it would give the core cluster-admin reach into every cell
- **A tunnel into the instance's private network (SSH):** Needs software in the
  image and breaks when the network is the thing being debugged
- **Store a long-lived key per user:** Adds registration, rotation and
  revocation for no gain over a key per session
- **Build a dedicated tunnel binary:** Duplicates the Datum Connect transport,
  release and security work without improving the user experience
- **Reuse the Datum Connect desktop client:** No local API, not distributed as a
  CLI, and cannot be linked into `datumctl` under its current license

## Infrastructure needed

- A shell agent and tunnel endpoint deployed to each compute cell, with
  network policy limiting both.
- The Unikraft provider exec policy set to `always` in every cell that runs
  unikernel instances.
- The session-create permission added to the default project-admin role.
- A quota claim policy for sessions, with a default of 100 per project.
- An activity policy that maps session creation and lifecycle events into the
  project activity log.
- A released container image of the Datum Connect CLI, including the fix that
  lets it shut down cleanly.
- Relays served over TLS, with browser access allowed on their latency probe, so
  portal terminals can connect.
- Relay capacity and placement reviewed for interactive traffic.
