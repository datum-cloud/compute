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

Let users open a live shell inside a running instance from the CLI or the Cloud
Portal to inspect its state and run diagnostic commands.

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
  general-purpose (Kata) or unikernel (Unikraft) instance whose image contains
  one. Both runtimes use the same CLI and portal workflow.
- The p95 time from accepted session creation to the first prompt is less than
  five seconds on supported production networks.
- The project activity log provides a complete history of shell access.
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

#### An AI agent investigates a failure

An authorized operations agent detects a failed health check, creates a session
through the API, runs a targeted diagnostic command and reports its findings.

### Architecture

After session creation, shell traffic travels straight from the user to the
cell that runs the instance. Traffic is encrypted end to end, the control plane
is not in the data path and cells accept no inbound connections.

![C4 container diagram](./c4-container-diagram.png)

Source: [c4-container-diagram.puml](./c4-container-diagram.puml)

#### End-to-end flow

The client can be `datumctl`, the Cloud Portal or an authorized AI agent. Each
uses the same API and data path.

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
- **Short-lived:** Must connect within 60 seconds; lasts at most one hour
- **Per-instance limit:** At most three shells can run in an instance at once
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
- **Relay distance adds latency:** Route clients through Datum's regional relays
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
  generateName: web-0-
spec:
  instanceRef:
    name: web-0
    uid: 3d39d44d-93fb-4d78-a14a-d76a9876aa71
  containerName: app
  command: ["sh"]
  terminal: true
  clientPublicKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  ttl: 1h
status:
  connection:
    endpointID: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
    relayURLs:
      - https://iroh-relay.us-central-1.datumconnect.net
  connectBefore: "2026-09-29T18:01:00Z"
  expiresAt: "2026-09-29T19:00:00Z"
  conditions:
    - type: Ready
      status: "True"
      reason: SessionReady
      message: "Connect before 2026-09-29T18:01:00Z"
      lastTransitionTime: "2026-09-29T18:00:01Z"
      observedGeneration: 1
```

The request fields have these semantics:

- **`instanceRef`** includes the name and UID of an instance in the same
  project. Requiring the UID prevents a session from reaching a replacement
  instance that reused the name.
- **`containerName`** is required. The CLI and portal fill it automatically for
  a single-container instance and require a choice when several exist.
- **`command`** is a required argument vector, not a shell string. It contains
  1–64 elements and at most 16 KiB in total.
- **`terminal`** requests a pseudoterminal and connects standard input. When
  false, the command receives no input and returns separate standard output and
  error streams, which is the default path for AI agents and automation.
- **`clientPublicKey`** is the client's 32-byte iroh public key encoded as 64
  lowercase hexadecimal characters. The corresponding private key never enters
  the API.
- **`ttl`** defaults to one hour and cannot exceed one hour. The client still
  has only 60 seconds after the session becomes ready to connect.

Admission rejects an unknown instance or container, a mismatched instance UID,
an empty command, an invalid key, or a TTL outside the allowed range. The whole
spec is immutable. A client changes a request by deleting it and creating
another one.

The status fields are output only:

- **`connection`** contains the tunnel endpoint's public identity and relay
  URLs. It contains no bearer credential.
- **`connectBefore`** and **`expiresAt`** define the connection and execution
  deadlines. **`startedAt`** and **`endedAt`** appear as the command advances.
- **`exitCode`** appears only when the runtime reports a normal process exit.
  `datumctl` returns it to its caller. Without one, `datumctl` returns a nonzero
  code and shows the condition reason.
- **`Ready`** is the only condition type. It is `Unknown` while the request is
  pending, `True` when the client can connect and `False` after rejection or
  termination. Terminal reasons are `Completed`, `Expired`, `Revoked`,
  `NotConnected`, `AgentShutdown`, `AgentLost`, `TooManySessions`, `NoShell`,
  `InstanceNotRunning`, `InstanceNotFound` and `Invalid`.

A ready session never returns to pending, and terminal status does not change.
The controller holds a finalizer while a session can own a process or
reservation. Deleting an active session sets `Revoked`, stops the process,
records the end event and releases the finalizer. The cleanup controller can
then remove a terminal resource after 24 hours without changing its status.

### Runtime and lifecycle

- **Runtime capability.** Runtime classes advertise shell support, and sessions
  for classes without the `exec` feature are refused. The agent also checks
  that the selected image contains the requested command before accepting a
  session:
  - **General-purpose (Kata):** The runtime supports exec natively.
  - **Unikernel (Unikraft):** The Unikraft exec plugin provides exec when the
    cell's exec policy enables it.
- **Existing Unikraft instances.** The provider reconciles its exec annotation
  onto existing instance Pods. Kraftlet must activate the plugin without
  recreating the instance; otherwise, existing instances require recreation.
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
  endpoint to clients but does not authorize a shell, so a leak is confined to
  one cell. The agent rotates the secret; open shells on that endpoint end and
  users start new sessions.
- **Scale-to-zero:** opening a shell does not wake an instance. A session for a
  stopped instance fails before connecting and tells the user to start an
  instance. This avoids changing workload scale or incurring cost as a side
  effect of a diagnostic action.
- **Tunnel code:** the tunnel endpoint runs the Datum Connect CLI, so shells
  and Datum Connect share one tunnel implementation. Its license prevents
  linking it into `datumctl`; it does not prevent cells from running it as a
  separate program.

## Production readiness review questionnaire

### Feature enablement and rollback

- **Enable / disable:** a compute feature gate and the shell capability on each
  runtime class control the feature. Setting the Unikraft provider's exec
  policy to `always` enables that runtime. Disabling any applicable switch
  stops new sessions; open sessions end with a clear reason and their processes
  are cleaned up.
- **Rollback:** Disable the feature without changing instances or workloads.

### Rollout

Staging cells first, then a preview for selected projects, then production
cells through the normal release tag. Before preview, staging must validate the
latency goal and verify Unikraft behavior for new and existing instances,
missing shells, process cleanup and scale-to-zero.

### Monitoring requirements

- **For users:** each session's `Ready` condition and reason.
- **For operators:** sessions opened, active and ended by reason; time from
  creation to first output; agent health per cell.

### Dependencies

- **Relays:** New and open shells in the affected region fail; instances are
  unaffected
- **Federation to cells:** New sessions are not delivered; open shells continue
- **Kata runtime:** Shells fail for Kata instances on the affected node
- **Unikraft exec plugin:** Shells fail for unikernel instances on the affected
  node

### Scalability

One new API type, one object per session, created only on user action and
bounded by the per-instance limit and cleanup policy. Tests must cover
simultaneous session creation, reservation release and agent failover.

### Troubleshooting

- **Instance not running:** Explain that the user needs a running instance.
- **Instance missing:** Prompt the user to refresh the instance list.
- **Image has no shell:** Tell the user to deploy an image with a supported
  shell.

## Implementation history

- 2026-09-29: Local prototype on a Kata cell, from the CLI and from a browser
  (Chromium and WebKit); this document drafted.

## Drawbacks

- Operating cost and failure surface grow in every cell.

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
- A released container image of the Datum Connect CLI, including the fix that
  lets it shut down cleanly.
- Relays served over TLS, with browser access allowed on their latency probe, so
  portal terminals can connect.
- Relay capacity and placement reviewed for interactive traffic.
