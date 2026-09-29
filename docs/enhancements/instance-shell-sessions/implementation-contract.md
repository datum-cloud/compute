# Instance shell sessions: implementation contract

This is the build contract for [Instance shell sessions](./README.md). It fixes
the interfaces that separate pieces of work depend on, so each can be built and
reviewed on its own. Where this file and the design differ, raise it on the
design PR instead of diverging.

- [Work breakdown](#work-breakdown)
- [API](#api)
- [Control plane](#control-plane)
- [Cell](#cell)
- [Connection protocol](#connection-protocol)
- [Clients](#clients)
- [Runtime providers](#runtime-providers)
- [Datum Connect](#datum-connect)
- [Audit](#audit)
- [Deployment](#deployment)
- [Open questions](#open-questions)

## Work breakdown

| Wave | Work | Repo | Depends on |
|---|---|---|---|
| 1 | API, IAM, quota, feature gate, runtime class feature | compute | This contract |
| 1 | Clean shutdown, key file option, CLI image | app | This contract |
| 1 | Verify Unikraft idle parking, shell and process groups | unikraft-provider (staging lab) | Exec policy on one lab instance |
| 1 | Verify project events reach the activity pipeline | activity, infra (read only) | None |
| 2 | Control-plane session controllers | compute | Wave 1 API |
| 2 | Shell agent and cell packaging | compute | Wave 1 API and app |
| 2 | `datumctl compute exec` | compute | Wave 1 API |
| 2 | `exec` feature on the general-purpose runtime class | kata-provider | Wave 1 API |
| 2 | Browser terminal | cloud-portal | Wave 1 API |
| 3 | Staging wiring | infra | Released compute and app images |

Every piece of work is a draft pull request in one repository.

## API

### Resource

`InstanceConsoleSession`, `compute.datumapis.com/v1alpha`, namespaced in the
project. Plural `instanceconsolesessions`, short name `ics`.

```go
type InstanceConsoleSessionSpec struct {
    // +required
    InstanceRef InstanceConsoleSessionInstanceRef `json:"instanceRef"`
    // +required
    // +kubebuilder:validation:MaxLength=63
    ContainerName string `json:"containerName"`
    // +required
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=64
    Command []string `json:"command"`
    // +optional
    Stdin bool `json:"stdin,omitempty"`
    // +optional
    Terminal bool `json:"terminal,omitempty"`
    // +required
    // +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
    ClientPublicKey string `json:"clientPublicKey"`
    // +optional
    // +kubebuilder:default="15m"
    TTL *metav1.Duration `json:"ttl,omitempty"`
}

type InstanceConsoleSessionInstanceRef struct {
    // +required
    Name string `json:"name"`
    // +required
    UID types.UID `json:"uid"`
}

type InstanceConsoleSessionStatus struct {
    Connection    *InstanceConsoleSessionConnection `json:"connection,omitempty"`
    ConnectBefore *metav1.Time                      `json:"connectBefore,omitempty"`
    StartedAt     *metav1.Time                      `json:"startedAt,omitempty"`
    ExpiresAt     *metav1.Time                      `json:"expiresAt,omitempty"`
    EndedAt       *metav1.Time                      `json:"endedAt,omitempty"`
    ExitCode      *int32                            `json:"exitCode,omitempty"`
    Conditions    []metav1.Condition                `json:"conditions,omitempty"`
}

type InstanceConsoleSessionConnection struct {
    // Hex iroh endpoint ID of the tunnel endpoint.
    EndpointID string `json:"endpointID"`
    // Relays the tunnel endpoint is reachable through, nearest first.
    RelayURLs []string `json:"relayURLs"`
    // host:port the client names in its CONNECT request.
    Target string `json:"target"`
}
```

Validation, in the CRD with CEL where possible:

- The whole spec is immutable: `self == oldSelf`.
- `command` totals at most 16 KiB.
- `ttl` is at least 1m and at most 1h.
- `metadata.name` must be set through `generateName` by clients; the API does
  not enforce it.

`status.connection.target` is an addition to the design's API example. Clients
need it to open the tunnel stream.

### Condition and reasons

One condition, `Ready`. Reasons are exported constants in `api/v1alpha`.

| Status | Reasons |
|---|---|
| Unknown | `Pending` |
| True | `SessionReady`, `Connected` |
| False (terminal, never changes) | `Completed`, `Expired`, `Revoked`, `NotConnected`, `AgentShutdown`, `AgentLost`, `TooManySessions`, `NoShell`, `CommandUnavailable`, `InstanceNotRunning`, `InstanceNotFound`, `Invalid` |

`Completed` covers any normal process exit, zero or not; `status.exitCode`
carries the code.

### Feature gate and runtime class

- Feature gate `InstanceConsoleSessions`, Alpha, default false, in
  `internal/features`. When off, the session controllers and agent do not run
  and creates are rejected by the admission webhook with a clear message.
- `RuntimeClassFeature` value `exec`, with a customer-facing description in the
  feature description map.

### IAM

In `config/components/iam`:

- A `ProtectedResource` for `InstanceConsoleSession`, parent
  `resourcemanager.miloapis.com/Project`, permissions `create`, `get`, `list`,
  `watch`, `delete`.
- `compute.datumapis.com-admin` gains all of them. `compute-viewer` gains
  `get`, `list` and `watch`, so viewers can see who has sessions open but
  cannot open one.

### Quota

- A `ResourceRegistration` for `compute.datumapis.com/instanceconsolesessions`,
  counted per object.
- A `ClaimCreationPolicy` that claims one unit when a session is created.
  Claims are owned by the session and released when it is deleted.
- A default limit of 100 per project in compute's service configuration.

Follow network-services-operator's `config/quota/claim-policies` layout.

## Control plane

Controllers run in the management plane, behind the feature gate.

- **Admission.** Validate the request against the project's Instance: name and
  UID match, the container exists, the Instance's runtime class has the `exec`
  feature. Reject with a 4xx and an actionable message; do not create a
  session that is already known to fail.
- **Delivery.** Copy the session to the Karmada hub namespace
  `ns-<project-namespace-uid>`, labelled with the Instance's location and
  runtime class, and with the project session's UID in
  `compute.datumapis.com/session-uid`. Route it to the cell that serves the
  Instance's location and runtime class, reusing the WorkloadDeployment
  federator's placement. Extending the existing PropagationPolicy with a
  selector for sessions is preferred over new policies.
- **Status.** The shell agent writes status to the hub copy directly, the way
  the cell Instance controller writes Instances back. Do not rely on Karmada
  status aggregation. A controller copies hub status onto the project session.
- **Deletion.** When the project session is deleted, delete the hub copy.
  A finalizer on the project session waits up to five minutes for the cell to
  confirm cleanup, then records a `CleanupUnconfirmed` event and removes the
  finalizer.
- **Cleanup.** Once a session is terminal and its end event is recorded,
  delete it.
- **Events.** Emit `SessionStarted` when status first shows `Connected`, and
  `SessionEnded` when it first shows a terminal reason, on the project session.
  See [Audit](#audit).

## Cell

Two workloads per cell in namespace `compute-shell-system`, each a two-replica
StatefulSet. Ordinal `n` of each pairs with ordinal `n` of the other.

### Shell agent

Ported from the prototype. Responsibilities:

- **Claim.** Watch session copies delivered to the cell. Claim one by writing
  `status.connection` with its paired endpoint's ID, relay URLs and target, and
  `connectBefore` 60 seconds from now. Claims use optimistic concurrency; a
  conflict means another agent won.
- **Checks before claiming.** The Instance's pod exists and is running; the
  container exists; a shell and the requested executable are present
  (`command -v`). The result is cached per pod UID and container. Failures end
  the session with `InstanceNotFound`, `InstanceNotRunning`, `NoShell` or
  `CommandUnavailable`.
- **Per-instance limit.** Three Lease slots per instance,
  `slot-<hash(instance-uid)>-<n>`, holder the session UID. Taking a slot is a
  create; failure on all three ends the session with `TooManySessions`.
- **Liveness.** Each agent renews Lease `agent-<endpoint-id[:16]>` every 5
  seconds with a 15-second duration. An agent that sees another's lease expire
  ends its sessions with `AgentLost` and releases their slots.
- **Exec.** Call the cell apiserver's `pods/exec` for the Instance's pod, with
  the WebSocket `v5.channel.k8s.io` protocol. Wrap the command as
  `sh -c 'echo $$ > <dir>/.datum-exec-<uid>; exec "$@"' sh <command...>` so the
  agent can stop the process group when the session ends for any reason.
- **Deadlines.** `connectBefore` passes without a connection: `NotConnected`.
  `expiresAt`, set at start to `startedAt + ttl`: `Expired`.
- **Draining.** On SIGTERM stop claiming, write a notice to terminal sessions,
  wait up to 30 seconds, then end them with `AgentShutdown`.
- **Orphan sweep.** On start and every minute, stop processes whose marker file
  names a session that no longer exists or is terminal.
- **Concurrency cap.** At most 200 open sessions per agent; further claims wait.
- **Identity key.** Create the paired endpoint's secret
  `endpoint-key-<n>` (32 raw bytes) if it is missing. Rotate every 30 days: stop
  claiming, let open sessions end or expire, replace the secret, restart the
  endpoint, resume.

The agent holds the cell apiserver and Karmada hub credentials. It is never
reachable from outside the cell except through its paired endpoint.

### Tunnel endpoint

Runs the Datum Connect CLI `serve` with one TCP proxy to its paired agent,
`exec-agent-<n>.exec-agent.compute-shell-system.svc.cluster.local:7777`. That
host and port is `status.connection.target`. No service account token, no
Kubernetes API access, identity key mounted read-only.

### Network policy

- Endpoint: egress to relays (443/TCP, 7842/UDP for QUIC address discovery) and
  DNS; egress to its agent on 7777; no ingress. Traffic always goes through a
  relay in this version, because direct paths would need UDP egress to any
  address.
- Agent: ingress from its endpoint on 7777 only; egress to the cell apiserver,
  the Karmada hub and DNS.

Ship both a Kubernetes NetworkPolicy and a CiliumNetworkPolicy; the prototype
showed the second is needed to police traffic to the node's own apiserver.

## Connection protocol

Version 1. Clients and the agent must reject anything else.

1. **Tunnel.** Dial `status.connection.endpointID` through
   `status.connection.relayURLs` with ALPN `iroh-http-proxy/1`, using the
   session's client key as the iroh identity. Open a bidirectional stream and
   send `CONNECT <target> HTTP/1.1`. Expect `200`.
2. **Request.** On that stream send an HTTP/1.1 WebSocket upgrade:

   ```
   GET /v1/sessions/<project-session-uid>/exec HTTP/1.1
   Sec-WebSocket-Protocol: v5.channel.k8s.io
   X-Datum-Timestamp: <unix seconds>
   X-Datum-Signature: <base64 ed25519 signature>
   ```

   The signature covers `datum-exec-v1\n<project-session-uid>\n<timestamp>`
   with the key named in `spec.clientPublicKey`. The agent accepts timestamps
   within 30 seconds of its clock.
3. **Refusals** before the upgrade:
   - `403` signature does not match
   - `404` unknown session, or not claimed by this agent
   - `409` session already consumed or not yet ready
   - `410` revoked, expired or past `connectBefore`
   - `503` agent draining; create a new session
4. **Stream.** Standard `v5.channel.k8s.io` channels: 0 stdin, 1 stdout,
   2 stderr, 3 status, 4 resize, and the v5 close signal for stdin. Frames
   larger than 16 MiB end the session.
5. **Notices.** The agent writes maintenance notices only to terminal sessions,
   on stdout. Non-terminal sessions never get injected bytes.
6. **Ending.** The agent always sends one status message on channel 3, then
   closes with WebSocket code 1000.
   - Exit 0: `metav1.Status{Status: Success}`.
   - Non-zero exit: `Status: Failure`, `Reason: NonZeroExitCode`, exit code in
     `details.causes[{type: ExitCode, message: <code>}]`, as Kubernetes does.
   - Platform ending: `Status: Failure`, `Reason: <terminal reason>`,
     `Message: <user-facing text>`.

The first successful upgrade consumes the session; the agent records
`Connected` before relaying any bytes.

## Clients

### datumctl

`datumctl compute exec <instance> [-c container] [-i] [-t] -- <command...>` in
the compute plugin (`internal/cmd/compute`).

- Generate an ed25519 key in memory; never write it anywhere.
- Look up the Instance for its UID. Pick the only container, or require `-c`.
- Create the session with `generateName: <instance>-`, watch it until
  `SessionReady` or a terminal reason, then connect.
- Exit code: the command's exit code; 1 with the reason for any platform ending;
  2 for usage errors. Map a quota denial to "too many open sessions in this
  project".
- Delete the session when the command ends or on interrupt.
- Pin `github.com/tmc/go-iroh` to one version, shared with the portal.

### Cloud Portal

- An **Open shell** action on the instance page, shown when the user has
  `create` on sessions and the runtime class has `exec`.
- Connect in a Web Worker that loads the go-iroh WebAssembly build on first use.
  Hold the key only in the worker; clear it when the terminal closes.
- Reuse the existing xterm terminal panel. Build on `main`, not
  `feat/terminal-datumctl`.
- Relay-only; the browser cannot use direct paths.

## Runtime providers

- **kata-provider.** Add `exec` to the general-purpose runtime class and to the
  capability check. Instance pods keep their current name, namespace and
  `managed-by: kata-provider` label.
- **unikraft-provider.** No code change. Cells run with `execPolicy: always`.
  Add `exec` to the unikernel runtime class in infra's service configuration.

The agent finds an Instance's pod by the Instance's name and namespace and
accepts `managed-by` of `kata-provider` or `infra-provider-unikraft`.

## Datum Connect

In `datum-cloud/app`:

- Exit cleanly on SIGTERM: shut down the router and endpoint so relays drop
  the endpoint at once. The prototype's patch shows the change.
- Accept the identity key from a file path, so a read-only secret works without
  an init container copying it.
- Build and publish a CLI container image on release, separate from the
  desktop bundles. Replace the stale Dockerfile, whose `gateway` command no
  longer exists.

## Audit

- **Creation.** From the project API audit log, actor = requester.
- **Start and end.** Kubernetes Events on the project session from the session
  controller: reasons `SessionStarted` and `SessionEnded`. Annotations carry
  instance, container, requester and, for `SessionEnded`, the terminal reason
  and exit code.
- **ActivityPolicy.** Shipped by compute for `InstanceConsoleSession`: one audit
  rule for `create`, and event rules for the two reasons.

## Deployment

In infra, staging first:

- Enable the cell component in `apps/compute-system/edge` for staging cells.
- Set the Unikraft exec policy to `always` for staging cells.
- Add `exec` to the unikernel runtime class entry.
- Turn on `InstanceConsoleSessions` on the staging control plane.
- Allow browser access to the relays' latency probe with a response header at
  the relay gateway.

## Open questions

Answered in wave 1; each answer may change this contract.

- Does an open exec session keep a Unikraft instance from parking when idle?
- Does the Unikraft sandbox plugin bring its own shell, and can the agent stop a
  process group through it?
- Do Events from project control planes reach the activity pipeline?
- Does relay-only traffic meet the five-second goal, or do direct paths need
  wider UDP egress from the endpoint?
- Does the projected Instance carry the cell Instance's identity, so the
  control plane can map `instanceRef.uid` to the cell pod?
