---
status: proposed
---

# Mounting ConfigMaps and Secrets into Compute Instances (Unikraft Provider)

> Drafted: 2026-05-30 against compute branch `fix/federated-workload-health` and `datum-cloud/unikraft-provider` `main`. Revised 2026-05-31 after a second review round and a sequencing decision.
>
> **This is the foundational referenced-data delivery design for compute, and it ships first** — before [Image Pull Credentials](./image-pull-credentials.md). It *introduces* the cross-plane machinery (the resolver, the scoped project-plane client, companion materialization, the referenced-data scheduling gate, and provider gate-honoring). Image-pull-credentials later becomes a **consumer** of this machinery: a pull secret is just one more kind of referenced data routed through the same resolver and gate. Nothing here depends on that RFC landing.

## Table of Contents

- [Summary](#summary)
- [What this enables for users](#what-this-enables-for-users)
- [Background: how config reaches an instance today](#background-how-config-reaches-an-instance-today)
- [The core problem: two broken hops](#the-core-problem-two-broken-hops)
- [Blocking dependencies](#blocking-dependencies)
- [Design principles](#design-principles)
- [Proposed design](#proposed-design)
  - [Phase 1 — env injection from ConfigMap/Secret keys](#phase-1--env-injection-from-configmapsecret-keys)
  - [Phase 2 — file mounts from ConfigMap/Secret volumes](#phase-2--file-mounts-from-configmapsecret-volumes)
  - [Cross-plane delivery: the referenced-data resolver](#cross-plane-delivery-the-referenced-data-resolver)
  - [Scheduling gate and provider consumption](#scheduling-gate-and-provider-consumption)
  - [Rotation and rollout](#rotation-and-rollout)
- [Alternatives considered](#alternatives-considered)
- [Security considerations](#security-considerations)
- [Failure modes and UX](#failure-modes-and-ux)
- [File-level change list](#file-level-change-list)
- [What review changed](#what-review-changed)
- [Decided (previously open)](#decided-previously-open)
- [Open questions for human decision](#open-questions-for-human-decision)

---

## Summary

A Datum compute `Workload` can already *describe* config and secret mounts. The
Instance API models them:

- `InstanceSpec.Volumes[].VolumeSource.ConfigMap` / `.Secret`
  (`api/v1alpha/instance_types.go:292,296`) — populate a volume from a
  ConfigMap/Secret.
- `SandboxContainer.VolumeAttachments[]` with a `MountPath`
  (`instance_types.go:206`) — attach a named volume into the container at a path.
- `SandboxContainer.Env[]` is `corev1.EnvVar`, so `ValueFrom.ConfigMapKeyRef`
  and `SecretKeyRef` already parse (`instance_types.go:139`).

But describing a mount is *all* that works today. Like image pull credentials,
this is **two independent broken hops**, and the more powerful of the two
capabilities is gated on an external vendor:

1. **The bytes have no cross-plane path.** The ConfigMap/Secret lives in the
   user's project namespace; the Instance runs on an edge cell; nothing moves
   the data between them. The federation `PropagationPolicy` selects **only
   `WorkloadDeployment`** objects (`workloaddeployment_federator.go:284-293`), so
   a referenced ConfigMap/Secret never lands on the cell. An Instance is created
   on the cell with a dangling reference. There is no scheduling gate guarding
   this, so the Instance is not even held back — it simply has no data.

2. **The last hop for *file mounts* does not exist and cannot be built by us
   alone.** kraftlet/UKC — which actually runs the instance — has **no
   file-injection capability**. The UKC SDK `CreateInstanceRequest`
   (`unikraft.com/cloud/sdk` `model_create_instance_request.go`) exposes `Env`
   (inline `map[string]string`) and `Volumes` (references to *pre-created
   persistent disks* by name/UUID with a mount point) — and **nothing for inline
   files, projected volumes, ConfigMap/Secret data, or cloud-init provisioning**.
   The provider doesn't even set Pod volumes today
   (`unikraft-provider/internal/controller/instance_controller.go:180-239`), and
   they would be ignored if it did. Mounting ConfigMap/Secret data **as files**
   is impossible until UKC's API accepts injected file content — the same shape
   of blocker as the registry-auth gap.

The result is a clean split. **Env injection** (ConfigMap/Secret *keys* surfaced
as environment variables) maps onto UKC's inline `Env` and is **buildable today**
once the bytes reach the edge. **File mounts** (ConfigMap/Secret rendered as
files at a path) are **blocked on UKC** and should be designed now but shipped
when the upstream capability exists.

This RFC keeps the **user contract unchanged** (create a ConfigMap/Secret,
reference it from the Workload template), **introduces a single referenced-data
resolver** as foundational platform infrastructure (image-pull-credentials will
later route pull secrets through it rather than the reverse), and is explicit that
secret bytes must **never be inlined into the Instance spec** (which is stored
across planes and projected back to the user).

## What this enables for users

Today a user can only set literal environment variables on a container. They
cannot point an instance at a ConfigMap or Secret they manage — so application
config, API keys, database URLs, TLS material, and feature flags either get
hard-coded into images or pasted as plaintext literals into the Workload.

The happy path: a user creates a `ConfigMap` named `app-config` and a `Secret`
named `app-secrets` in their project namespace, references them from the Workload
template (`envFrom` or per-key `valueFrom`), and applies the Workload. With no
further action, every instance the platform schedules — in every POP cell the
workload is placed in — comes up with those keys present as environment
variables. The user never learns that the instance runs on a cell thousands of
miles from where they created the Secret.

With this work:

- **Config and secrets are first-class, managed separately from the workload.**
  A user creates a `ConfigMap` (app config) and a `Secret` (credentials) in their
  project, then references them by name from the Workload template. The platform
  delivers that data to every instance, in every POP cell the workload is placed
  in, without the user knowing federation exists. This is the standard
  twelve-factor pattern — configuration lives outside the image, not baked in or
  pasted as plaintext literals.
- **Secrets stay secret.** Values are never written into the Workload or Instance
  spec, never projected back to the project plane in plaintext, and travel only
  as Secret objects over the federation channel.
- **Phase 1 (env):** keys from a ConfigMap/Secret appear as environment variables
  inside the instance — the common case for twelve-factor apps. This is the
  capability the Unikraft provider can deliver with no upstream vendor dependency
  (it maps onto UKC's inline `Env`), so it's the first to ship.
- **Phase 2 (files):** a ConfigMap/Secret is mounted as files at a path — needed
  for config files and certificates. Lands when Unikraft Cloud supports file
  injection; the user-facing API is identical, so no rework for users.

## Background: how config reaches an instance today

The flow `Workload → WorkloadDeployment → Instance` copies the instance template
**verbatim** at each level:

- `WorkloadReconciler` copies `workload.Spec.Template` into each
  `WorkloadDeployment` (`workload_controller.go:439`).
- The stateful instance-control strategy copies
  `deployment.Spec.Template.Spec` straight into `Instance.Spec` with **no
  reference resolution** (`instancecontrol/stateful/stateful_control.go:86`).

On the cell side, the Unikraft provider's `InstanceReconciler` turns an Instance
into a downstream Kubernetes Pod for kraftlet, mapping **only** container
`Name`, `Image`, `Env` (literal `Value` only — `ValueFrom` is dropped), `Ports`,
and a memory limit (`instance_controller.go:189-195`). It ignores
`Volumes`, `VolumeAttachments`, `ImagePullSecrets`, `Command`, and `Args`.
kraftlet then issues a UKC `CreateInstanceRequest`.

So a referenced ConfigMap/Secret is dropped twice: it never leaves the project
plane, and even if it arrived, the provider wouldn't read it.

## The core problem: two broken hops

```
            project plane                     federation                 edge cell                    UKC
  ┌─────────────────────────────┐      ┌──────────────────┐      ┌────────────────────┐      ┌─────────────────┐
  │ Workload.template            │      │ Karmada hub      │      │ WorkloadDeployment │      │ kraftlet Pod    │
  │  → WorkloadDeployment        │ ───▶ │ ns-{project-uid} │ ───▶ │  → Instance        │ ───▶ │  → UKC instance │
  │ ConfigMap / Secret (project) │      │  WD only         │      │ (dangling ref)     │      │ Env ✓  files ✗  │
  └─────────────────────────────┘      └──────────────────┘      └────────────────────┘      └─────────────────┘
        HOP 1: bytes never propagated ───────────────────────────────────┘        HOP 2: no file-injection on UKC ─┘
```

- **Hop 1 (delivery) — ours to fix.** Get the referenced ConfigMap/Secret data
  from the project namespace to `ns-{project-uid}` on each placed cell. The
  PropagationPolicy must carry it, something must materialize it, and the
  Instance must be held until it lands.
- **Hop 2 (consumption) — split.** Env injection lands in UKC's inline `Env`
  (buildable now). File mounts need a UKC file-injection API that does not exist.

## Blocking dependencies

- **B1 (file mounts only): Unikraft Cloud file injection.** UKC's
  `CreateInstanceRequest` has no `files`/`user_data`/metadata field and its volume
  API creates only empty, sized volumes with no content/init/snapshot/clone path —
  so there is no way to get file bytes into an instance today. UKC must add a
  file-injection capability (see [the upstream ask](#the-upstream-ask-what-would-unblock-b1)).
  Tracked the same way as the registry-auth gap. Until then, `ConfigMap`/`Secret`
  *volume* sources with `VolumeAttachments` are rejected at admission for the
  Unikraft provider, or accepted-but-not-satisfied with a clear condition (see
  [Open questions](#open-questions-for-human-decision)).
- **B2: federation merge.** The resolver + PropagationPolicy extension live in the
  management/federation path, so they require the federation work to be present.
- **B3: a scoped project-plane client (this RFC builds it).** The resolver needs to
  read project-namespace ConfigMaps and Secrets. The quota per-project client
  **cannot** be reused — it targets a Milo control-plane path serving only quota
  resources, not core ConfigMaps/Secrets ([memory: quota client is
  quota-scoped]). This RFC introduces the dedicated full-Kubernetes project-plane
  client; because ConfigMaps/Secrets ship first, **this is the RFC that owns
  building it**, and image-pull-credentials reuses it later. This is a dependency
  we build, not one we wait on.

## Design principles

1. **Build the referenced-data delivery path here, once, as foundational
   infrastructure.** Management-plane resolution → labeled companion object →
   PropagationPolicy → scheduling gate → finalizer cleanup is general machinery,
   not specific to config/secrets. Build **one resolver** that handles all
   referenced data; ConfigMaps/Secrets are its first consumer, and pull secrets
   (image-pull-credentials) become a later consumer of the same path. Designing it
   general from the start is what lets image-pull-credentials be a thin addition
   rather than a parallel system.
2. **Secret bytes never enter an Instance or Workload spec.** Resolution that
   inlines values would leak them into etcd on every plane and into the
   user-visible projected Instance. References travel down; bytes travel only as
   Secret objects.
3. **Resolve in the management plane.** It already has legitimate project access.
   Don't grant the shared edge identity broad project-Secret read.
4. **Gate, don't race.** An Instance must not start until its referenced data is
   confirmed present on the cell. Mirror the network/quota gate model.
5. **One user contract across phases.** The API the user writes is identical for
   env and file mounts; only the delivery/consumption backend differs by phase.
6. **Degrade honestly.** On the Unikraft provider, file mounts are surfaced as
   unsupported (clear condition) rather than silently ignored.
7. **References are same-namespace and authorized.** A Workload may only
   reference ConfigMaps/Secrets in its own project namespace (the API uses
   name-only `LocalSecretReference`/`LocalObjectReference`, never a namespace),
   and admission must verify the submitting principal can actually read the named
   object — mirroring how `validateInstanceNetworkInterfaces` runs a
   `SubjectAccessReview` for referenced Networks
   (`instance_validation.go:118-141`). The resolver is a fixed system identity; it
   must not become a confused deputy that reads objects the Workload author
   couldn't.

## Proposed design

### Phase 1 — env injection from ConfigMap/Secret keys

The buildable-today capability. Surface keys from a ConfigMap/Secret as
environment variables inside the instance.

**API.** `ValueFrom` already parses on `Env`. Add the bulk form to match
Kubernetes ergonomics and avoid per-key boilerplate:

- Add `EnvFrom []EnvFromSource` to `SandboxContainer` (`instance_types.go`),
  mirroring `corev1.EnvFromSource` (`ConfigMapRef` / `SecretRef` + optional
  `Prefix`). Regenerate deepcopy + CRDs (`make manifests generate`).
- Add validation for `ValueFrom.ConfigMapKeyRef` / `SecretKeyRef` and `EnvFrom`
  in `internal/validation/instance_validation.go` (key/name presence, `optional`
  semantics).

**Consumption (Unikraft provider).** The provider's `InstanceReconciler` already
holds two clients: an **upstream** client for the cell cluster where the Instance
(and the companion objects) live, and a **downstream** client for the kraftlet
cluster where it creates Pods (`instance_controller.go:64-68,80`). The companion
ConfigMap/Secret lands in `ns-{project-uid}` on the **cell** — i.e. behind the
*upstream* client — so the provider reads it there, resolves `ValueFrom`/`EnvFrom`
to concrete key/values, and inlines them into the kraftlet Pod's container `Env`
(which kraftlet forwards to UKC's inline `Env`). Concretely, extend
`buildPodSpecFromContainers` (`instance_controller.go:189-195`, which today copies
only `env.Value`) to resolve references via the upstream client — **not** by
emitting Pod `ValueFrom` refs (kraftlet does not resolve those, and the downstream
kraftlet cluster has no copy of the companion objects).

> Note: this is the one place secret bytes land in a Pod spec — the *downstream
> kraftlet Pod*, which is infra-internal and never projected to the user. The
> upstream Instance still carries only references. This boundary is deliberate
> and called out in [Security](#security-considerations).

### Phase 2 — file mounts from ConfigMap/Secret volumes

The user writes `InstanceSpec.Volumes[]` with a `ConfigMap`/`Secret` source and a
`SandboxContainer.VolumeAttachments[]` with a `MountPath`. The data is rendered as
files under that path — the case that env injection cannot serve: config files an
app reads by path (`nginx.conf`, `application.yaml`), TLS certificate/key pairs,
CA bundles, `.json`/`.toml` blobs, and anything binary or larger than is sane for
an environment variable.

This phase is **blocked on Unikraft Cloud (B1)**. The compute-side API and
delivery can and should land now; the Unikraft *consumption* waits on an upstream
capability that does not exist today. The two halves are decoupled below.

#### The user contract and fidelity target

"Mount a ConfigMap/Secret as files" is the standard Kubernetes projected-volume
contract, and the platform should match it so behavior is unsurprising:

- **key → path projection.** Each ConfigMap/Secret key becomes a file. Default
  layout is one file per key named after the key; `items` lets the user remap
  specific keys to relative paths (e.g. key `server.crt` → `tls/server.crt`).
- **`defaultMode` / per-item `mode`.** File permission bits (e.g. `0400` for a
  private key).
- **`optional`.** A missing source is skipped rather than holding the Instance.
- **read-only mount** at the attachment's `MountPath`.

#### API gaps to close (independent of B1 — do now)

These make the *spec* complete and portable across providers regardless of UKC:

- Implement **secret volume validation** — currently a bare TODO
  (`instance_validation.go:251`).
- Implement **ConfigMap `items` key→path projection** — currently **forbidden** by
  validation (`instance_validation.go:341-343`); required for selective file
  mapping and `defaultMode`. Do the same for Secret volumes.
- Validate `defaultMode`/`mode` range and path safety (no `..`, no absolute
  item paths) so a malicious or fat-fingered template can't escape the mount root.
- Align the `Secret` volume struct naming with `ConfigMap`
  (`instance_types.go:295` TODO) or document the divergence.

#### Why this is blocked on UKC (B1), precisely

UKC's instance-create API has **no path for file bytes**. From the SDK
(`unikraft.com/cloud/sdk` `platform`):

- `CreateInstanceRequest` exposes `Env map[string]string`, `Args []string`, and
  `Volumes []CreateInstanceRequestVolume` — and **no `files`, `user_data`,
  `cloud-init`, or metadata field of any kind**.
- `CreateInstanceRequestVolume` references a volume by `Name`/`Uuid`, optionally
  *creates* one by giving `SizeMb`, mounts it `At` a path, optionally `Readonly`.
- The Volumes service (`CreateVolume`, `AttachVolume`, …) creates **empty, sized**
  volumes only. There is **no content-upload, no init-data, no snapshot, and no
  clone** endpoint — no way to get bytes into a volume through the API.

kraftlet (the `unikraft-cloud/k8s-operator` virtual kubelet) compounds this: its
`Instance` CRD spec *is* `platform.CreateInstanceRequest`, so it never reads Pod
`Volumes`/`VolumeMounts`. Even if the provider populated Pod volumes, kraftlet
would drop them. This is the same shape of blocker as the registry-auth gap
([memory: unikraft no pull-secret support]).

#### The upstream ask (what would unblock B1)

We should request **one** of the following from Unikraft Cloud, ordered by
preference for how cleanly it maps onto the ConfigMap/Secret-volume contract:

1. **A `files` array (or `user_data`) on `CreateInstanceRequest`** — entries of
   `{path, content (base64), mode}`. This is the cleanest fit: the provider
   renders the projected volume into a file list and hands it over at create time,
   no separate volume lifecycle to manage. (Mirrors how the GCP path uses
   cloud-init `write_files`.)
2. **Volume init-data on `CreateVolumeRequest`** — `init_data: [{path, content,
   mode}]` so a volume is born populated, then attached read-only.
3. **A volume content-upload endpoint** — `POST /volumes/{uuid}/content` — provider
   creates → uploads → attaches. Most round-trips; weakest fit.

Option 1 is the recommended ask. Track it alongside the registry-auth request as a
named Unikraft Cloud dependency; this RFC's Phase 2 cannot ship without it.

#### Reference consumption: the GCP provider already does this

`infra-provider-gcp` is the proof that the *compute API* is a sufficient,
portable contract — it consumes the same `Volumes`/`VolumeAttachments` model and
renders files into the guest today
(`infra-provider-gcp/internal/controller/instance_controller.go`):

- **ConfigMap** → cloud-init `write_files`, materialized at
  `/etc/configmaps/<name>/<key>` before boot, symlinked to the attachment
  `MountPath` (`buildConfigMaps`, ~`:693-731`).
- **Secret** → synced to GCP Secret Manager via Crossplane, fetched at boot by an
  injected `populate_secrets.py` and written to `/etc/secrets/content/<name>/<key>`
  (`reconcileSecrets`, ~`:733-998`).
- It **honors scheduling gates** (`:119-122`) and reads the referenced objects
  from its **upstream** cluster directly.

Two honest caveats this revealed (correcting an earlier draft claim that "GCP is
unblocked immediately"):

- **GCP does not use the companion-object delivery this RFC proposes.** It fetches
  the originals from *its* upstream and transforms them inline, because in its
  topology the provider's upstream is the cluster that holds the originals. The
  federated Unikraft path needs companions specifically because the *edge cell*
  does not hold the originals — see the note in
  [delivery](#cross-plane-delivery-the-referenced-data-resolver). So what is truly
  "provider-agnostic" is the **Instance volume API**, not the delivery mechanism;
  each provider sources the bytes however its topology and substrate allow.
- **GCP's own gaps are real:** `items` projection is stubbed and env `ValueFrom`
  is an explicit TODO (`:441`). So closing the validation/API gaps above benefits
  GCP too, and Phase 1's env path is likewise per-provider work.

#### Workarounds while B1 is unmet — and why none is the product answer

- **Entrypoint/init shim decoding base64 env into files.** *Technically feasible*
  (UKC has `Env`), but rejected as the platform behavior: it requires wrapping the
  user's image (we don't own arbitrary user entrypoints), pushes secret bytes
  through `Env` (the very exposure Phase 1 confines and Secret volumes exist to
  avoid), and inherits env size limits. At most a documented user-side escape
  hatch, never the platform's answer.
- **Pre-populated / cloned volume.** *Not feasible* — no content, init, snapshot,
  or clone API exists (above).
- **Bake config into the image.** The status quo, and exactly what this feature
  exists to eliminate. Not a path forward.

Conclusion: there is no acceptable interim mechanism for file mounts on Unikraft;
Phase 2 genuinely waits on B1. The compute-side API completeness and
companion-delivery still land in Phase 1 so that (a) the spec is correct and
validated, and (b) any provider whose substrate accepts files is ready to consume
it the moment its consumption code is written.

#### Interim UX on Unikraft (before B1)

A user may still write a volume-backed ConfigMap/Secret mount in a Workload placed
on Unikraft cells. The platform must not silently drop it. Either reject at
admission with a message naming the unsupported capability, or admit and surface
`ReferencedDataReady=False / Reason=FileMountsUnsupported` on the Instance — see
[Open questions](#open-questions-for-human-decision). Whichever is chosen, the
signal must be distinguishable from "companion not yet landed" so a user can tell
"not supported here" from "still propagating."

### Cross-plane delivery: the referenced-data resolver

A new management-plane controller, the **`ReferencedDataController`**:

1. **Collect references.** For each WorkloadDeployment, walk the template for all
   referenced ConfigMaps/Secrets: every `Env.ValueFrom.*Ref`, every `EnvFrom.*Ref`,
   and every `Volumes[].ConfigMap/.Secret` (and, once image-pull-credentials lands,
   `imagePullSecrets`). Produce a deduplicated set of `(kind, name)` in the project
   namespace.
2. **Read with a scoped project-plane client** (B3) — the dedicated
   full-Kubernetes client this RFC introduces, **not** the quota client.
3. **Materialize labeled companions** into `ns-{project-uid}`: a derived
   ConfigMap or Secret per referenced object, deterministic name, labeled
   `compute.datumapis.com/referenced-data` (+ a kind/source label). Same
   etcd-namespace as the WorkloadDeployment so it co-propagates.
4. **Stamp the expected set on the WorkloadDeployment.** The resolver writes the
   list of expected companion names as an annotation
   (`compute.datumapis.com/expected-referenced-data`) on the WorkloadDeployment
   *before* (or with) materialization. Karmada propagates the WD — annotation
   included — to each cell, giving the cell-side gate-clearing reconciler an
   explicit contract for which companions to wait for (see
   [scheduling gate](#scheduling-gate-and-provider-consumption)). This is the
   resolved form of the naming-determinism question: an explicit list, not
   reverse-engineered names.
5. **Extend the PropagationPolicy `resourceSelectors`**
   (`workloaddeployment_federator.go:284-293`) to *always* include a selector for
   the `referenced-data` label, independent of whether any companion exists yet.
   The selector matches by label continuously, so a companion materialized **after**
   the policy is in place is still picked up by Karmada on its next sync — this is
   what closes the federator-vs-resolver ordering race (the two are independent
   controllers; the label selector makes ordering irrelevant). Karmada lands
   matching companions in `ns-{project-uid}` on each placed cell, beside the
   Instance.

**Fan-out (one hub object, N cell replicas).** There is exactly **one** companion
per `(kind, source-name)` in `ns-{project-uid}` on the Karmada hub. Karmada's
PropagationPolicy replicates that single object to each placed cell. N cities = N
cell-side copies but **one** hub object — so creation, update, and deletion are all
single-writes at the hub; Karmada garbage-collects the cell replicas. The resolver
never enumerates cells.

**Ref-counting and lifecycle.** A source ConfigMap/Secret may be referenced by
several WorkloadDeployments in the same project. The single hub companion is
shared; the resolver tracks the referencing WDs (a `referenced-data`-owner label
set, or an explicit refs list on the companion) and deletes the companion only
when the **last** referencing WD stops referencing it (template edited to drop the
reference, or WD deleted). A **finalizer on the WorkloadDeployment** drives this:
on WD delete/update the resolver decrements the ref set and removes the finalizer
once it has reconciled the companion. Cleanup uses finalizers, **not** cross-cluster
owner refs (Karmada/cross-plane GC does not honor them — [memory: WD UID
cross-plane mismatch]). Deleting the hub companion lets Karmada GC the cell
replicas.

**Single-cluster mode.** In `single` mode there is no Karmada hop. The resolver
**still runs** — it materializes the companion as a local copy in the project
namespace's mapped location and stamps the same expected-set annotation; delivery
is a local copy rather than a PropagationPolicy change, and the gate clears once
the local companion exists. The path must be exercised in tests; absence of
Karmada must not silently disable delivery.

**Why companions, and not just "read the originals."** A provider can skip
companions if its own upstream cluster already holds the referenced objects — this
is what `infra-provider-gcp` does (it `Get`s the ConfigMap/Secret from its upstream
and transforms it inline). That works because of *its* topology. In the federated
Unikraft path the provider's local cluster is the **edge cell**, which never holds
the user's project objects — so the bytes must be delivered there out-of-band.
The companion mechanism is what bridges that gap; it is not redundant with a
provider that happens to sit next to the originals.

### Scheduling gate and provider consumption

- Add a `compute.datumapis.com/referenced-data` **scheduling gate** to Instances
  that reference any ConfigMap/Secret. The instance-control strategy already adds
  network/quota gates at create time (`stateful_control.go:94-107`); add this gate
  there when the template carries any reference, defined alongside the others in
  `instancecontrol/scheduling_gates.go`.
- **Who clears it.** There is no cell-side reconciler that validates companions
  today — this must be built. The cell-side `WorkloadDeploymentReconciler`/
  `InstanceReconciler` is the natural home (it already clears the quota gate at
  `instance_controller.go:233`). It clears the referenced-data gate only when
  **all** expected companions are present in the cell namespace, and surfaces a
  `ReferencedDataReady` condition with per-object reasons.
- **How it knows the expected set (decided).** The cell reconciler does **not**
  reverse-engineer names. The resolver stamps the expected companion names on the
  WorkloadDeployment as the `compute.datumapis.com/expected-referenced-data`
  annotation (see [delivery](#cross-plane-delivery-the-referenced-data-resolver)),
  Karmada propagates it to the cell, and the gate-clearing reconciler waits for
  exactly that set to be present in the cell namespace. An explicit propagated list
  — not a recomputed name — makes the cross-plane contract observable and survives
  naming-scheme changes. (The earlier draft left this as an open question; it is
  now decided.)
- **Observability — a crisp condition, not a silent hang.** A held gate must be
  diagnosable. Surface a `ReferencedDataReady` condition with explicit Reasons:
  `Resolving` (resolver working), `AwaitingPropagation` (companions not yet on the
  cell), `SourceNotFound` / `SourceUnauthorized` / `SourceTooLarge` (resolver can't
  materialize — names the offending object), `NameMismatch` (expected-vs-present
  diff, surfaced on the condition message), `FileMountsUnsupported` (Phase 2 on
  UKC), and `Ready`. Emit Kubernetes Events on each transition and metrics
  (`compute_referenced_data_companions_total`,
  `compute_referenced_data_resolve_failures_total{reason}`, gate-wait duration), to
  match the signal bar set by [quota-failure-modes](./quota-failure-modes.md).
- The Unikraft provider must **honor scheduling gates** — today its `Reconcile`
  proceeds straight to Pod creation without inspecting `Spec.Controller.SchedulingGates`
  (`instance_controller.go:58-105`). It must requeue while any gate is present.
  **This RFC introduces gate-honoring in the provider** (image-pull-credentials was
  previously assumed to add it; since this ships first, that work lands here), so a
  gated Instance is never handed to UKC with missing data.

### Rotation and rollout

**Decided:** running instances do **not** auto-roll on content change; the platform
provides an explicit restart path. Rationale: an implicit mass-roll on every
edit to a referenced object is surprising and risky at fleet scale, and UKC has no
API to mutate a running instance's env anyway — the value only changes by replacing
the instance. So rotation behaves like Kubernetes (mounting a changed ConfigMap
doesn't roll a Deployment), but unlike bare Kubernetes we ship a first-class
restart so operators aren't stuck.

- **Rotation (source content changes).** The resolver re-reads source objects on a
  periodic requeue and on Workload reconciles, tracking source `resourceVersion`;
  on change it **re-materializes the companion** (single hub write; Karmada
  re-propagates to cells). The latest bytes are therefore staged at the edge for
  the next instance creation or roll. Running instances keep their current values
  until replaced — surface the last-synced `resourceVersion` on the WorkloadDeployment
  status so staleness is observable.
- **Why content isn't in the template hash.** `instancecontrol.ComputeHash` hashes
  the template spec, which holds only **references**, not resolved content
  (`instancecontrol/controller_utils.go:17-21`). We deliberately do **not** fold
  resolved content into it — that would be the auto-roll behavior we rejected.
- **The restart path (reuses existing machinery, no new roll engine).** The
  template hash **does** include template metadata, so changing an annotation on
  the Workload template changes the Instance template hash, and the stateful
  strategy already performs an in-place, descending-ordinal, wait-for-Ready rolling
  update when the stored hash differs (`stateful/stateful_control.go:129-139`,
  ordering at `:171-172`; `needsUpdate` at `stateful_control_util.go:11-13`). The
  feature: a convention annotation (e.g. `compute.datumapis.com/restartedAt`) on the
  Workload template that flows `Workload → WorkloadDeployment → Instance` and forces
  exactly this roll. Setting/bumping it (by the user, or by an operator/automation
  after a known rotation) rolls every instance, which re-reads the freshly
  re-materialized companion. This is the entire restart mechanism — a documented
  annotation plus the rolls compute already does. (An opt-in *auto*-roll — fold a
  content hash in behind a per-Workload flag — remains a possible future addition,
  not part of this RFC.)

## Alternatives considered

- **Inline resolved values into the Instance spec in the management plane.**
  Simplest delivery (no companion objects, no gate). **Rejected:** leaks secret
  bytes into etcd on every plane and into the user-visible projected Instance —
  violates principle 2. Also breaks the immutability/diff model.
- **Scoped per-project provider read at the edge (no companions).** The strongest
  alternative, raised in review: give the provider a per-project scoped client (in
  the spirit of the quota client) so it reads the referenced ConfigMaps/Secrets
  directly from the project plane and resolves at the edge. *Steelman:* it deletes
  most of this design — no companion materialization, no ref-counting, no
  PropagationPolicy extension, no data scheduling gate, no cell-side gate-clearing
  reconciler; rotation becomes "re-read," and it is far less to ship first. It is
  also how `infra-provider-gcp` already works in its topology. **Rejected for the
  delivery of secret bytes:** it requires the edge — a shared, lower-trust plane
  that runs many tenants' instances — to hold a credential that can read project
  ConfigMaps/Secrets. Even scoped per-project, that widens edge blast radius for the
  most sensitive payload, and the resolution boundary (broad project-secret read
  stays in the management plane) is the whole point of principle 3. The leanness
  doesn't justify moving secret read to the edge. *(Considered for a config-only
  hybrid — ConfigMaps via edge read, Secrets via companions — but rejected to avoid
  maintaining two delivery mechanisms and two failure-mode sets for one feature.)*
- **Reuse the quota per-project client to read project Secrets from the edge.**
  **Rejected / not possible:** that client hits a Milo path serving only quota
  resources, not core Secrets ([memory: quota client is quota-scoped]).
- **Propagate the user's original objects via Karmada directly (no companion).**
  **Rejected:** couples cell namespace contents to arbitrary project objects,
  loses the labeling/scoping boundary, and complicates rotation/cleanup.
- **A separate controller for pull-secrets, parallel to this one.** **Rejected:**
  same collect→materialize→propagate→gate machinery. Since this ships first and
  general, image-pull-credentials becomes a thin consumer (add `imagePullSecrets`
  to the collected reference set, reuse the gate and scoped client) rather than a
  parallel cross-plane system.
- **Env-only, skip file mounts entirely.** Viable as Phase 1 scope, but the API
  already models volumes and users will expect config files/TLS; designing
  delivery once (provider-agnostic) keeps Phase 2 cheap.

## Security considerations

- **Bytes never in user-visible specs.** Workload/Instance specs and the projected
  Instance carry references only. Resolved values exist as Secret objects in
  `ns-{project-uid}` (project plane, hub, cell) and — for env — in the downstream
  kraftlet Pod, which is infra-internal and not projected to users.
- **Companion Secrets are Secrets end-to-end**, never ConfigMaps, never inlined.
  ConfigMap companions carry only non-secret config.
- **Scoped read grant.** The resolver's project-plane client needs
  `get`/`list`/`watch` on `configmaps` and `secrets` in project namespaces —
  scope this via Milo IAM exactly as image-pull-credentials scopes pull-secret
  read. No broader access than required.
- **Cell namespace isolation.** Companions land in the per-project `ns-{uid}`
  namespace, so one project cannot read another's data on a shared cell.
- **Authorization at admission (mechanism confirmed).** Per principle 7, admission
  runs a `SubjectAccessReview` confirming the Workload author can `get` each
  referenced ConfigMap/Secret in their own namespace. The Network check already
  does exactly this with the *submitter's* identity — it builds the SAR from
  `opts.AdmissionRequest.UserInfo` (`Username`/`Groups`/`UID`/`Extra`)
  (`instance_validation.go:113-131`), so the pattern transfers directly: same
  `UserInfo`, verb `get`, resources `configmaps`/`secrets`, name from the reference,
  namespace = the Workload's. This is the gate that prevents a user from naming an
  object they couldn't read themselves; the resolver's system identity is never the
  authority. The reconcile-time condition (`SourceUnauthorized`) is the backstop,
  not the primary control.
- **kraftlet Pod exposure boundary — the sharpest edge.** Inlining secret-derived
  env into the downstream Pod is the cost of UKC's inline-only `Env`. The Pod is
  created in the *downstream kraftlet cluster* via the downstream client
  (`instance_controller.go:80,120-127`), in `ns-{project-uid}`, and is **not** the
  object the `InstanceProjector` sends back to the project plane (it projects
  Instances, not Pods). Two obligations make "infra-internal" real rather than
  asserted: (1) the downstream kraftlet Pod must **never** be written back to the
  Karmada hub or projected upstream — state this as an invariant and add a test
  that fails if a Pod with inlined env appears on the hub; (2) anyone with
  `pods/get` in the downstream `ns-{project-uid}` can read the values, so that
  namespace's RBAC on the kraftlet cluster must be locked to platform components.
  If UKC later accepts secret-aware env (resolving a reference rather than a
  literal), drop the inlining entirely.
- **Etcd at rest.** Companion Secrets exist in etcd on the project plane, the
  Karmada hub, and each cell; the inlined kraftlet Pod adds the downstream cluster.
  This is the same at-rest exposure image-pull-credentials accepts as a trade-off —
  it presumes etcd encryption-at-rest on every plane. Call that assumption out
  explicitly and confirm it per environment rather than leaving it implicit.

## Failure modes and UX

- **Referenced object missing in project ns.** Resolver cannot materialize a
  companion → Instance gate stays held → `ReferencedDataReady=False` with the
  missing object named. `optional: true` sources are skipped, not held.
- **Companion not yet landed on cell.** Gate held; condition surfaces "waiting for
  data on cell". Normal transient state during placement.
- **Source rotated but instances not rolled.** Stale-by-design (see
  [rollout gap](#rotation-and-rollout)); surface last-synced `resourceVersion` on
  the WorkloadDeployment status so it's observable.
- **File-mount requested on Unikraft (B1 unmet).** Either rejected at admission
  with a clear message, or accepted with `ReferencedDataReady=False /
  Reason=FileMountsUnsupported` — decide in [open questions].
- **Object too large / too many keys.** Bound companion size; reject oversized
  references with a clear condition rather than failing propagation silently.
- **Provider can't read companion on cell.** Instance not started;
  surface a provider condition rather than booting with missing config.
- **Unauthorized reference.** Author lacks read on a named object → rejected at
  admission with a clear message, not deferred to a silent reconcile failure.
- **Gate stuck on name mismatch.** If the cell reconciler's expected companion set
  disagrees with what landed (naming-scheme drift), the gate never clears.
  Mitigated by the explicit expected-name list (option (a) above); surface the
  expected-vs-present diff on the condition so it's diagnosable, not a silent hang.
- **Single-cluster mode.** Local copy path must be exercised in tests; absence of
  Karmada must not silently disable delivery.

## File-level change list

**compute (API + management):**
- `api/v1alpha/instance_types.go` — add `EnvFrom` to `SandboxContainer`; align
  Secret volume struct naming.
- `internal/validation/instance_validation.go` — implement secret-volume
  validation (`:251`), ConfigMap `items` projection (`:341-343`), `EnvFrom` and
  `Env.ValueFrom` validation, and a `SubjectAccessReview` that the author can read
  each referenced ConfigMap/Secret (pattern at `:118-141`).
- `internal/quota/`-style new package — **scoped project-plane client** (B3): a
  dedicated full-Kubernetes client for reading project-namespace ConfigMaps/Secrets.
  This RFC owns it; image-pull-credentials reuses it later.
- `internal/controller/` — **new `ReferencedDataController`**: collect refs, scoped
  project-plane read, materialize one shared companion per `(kind, source-name)`,
  stamp the `expected-referenced-data` annotation on the WD, ref-count referencing
  WDs, finalizer-driven cleanup.
- `internal/controller/workloaddeployment_federator.go:284-293` — extend the
  PropagationPolicy `resourceSelectors` to *always* include the `referenced-data`
  label selector (ordering-race-free).
- `internal/controller/instancecontrol/scheduling_gates.go` +
  `stateful_control.go:94-107` — define and add the `referenced-data` gate when the
  template carries any reference.
- cell-side reconciler (new gate-clearing logic, beside the quota-gate clear at
  `instance_controller.go:233`) — wait for exactly the `expected-referenced-data`
  set, clear the gate, set `ReferencedDataReady` with the Reason enum + events +
  metrics.
- restart path: thread a `compute.datumapis.com/restartedAt` template annotation
  `Workload → WorkloadDeployment → Instance` (template metadata already feeds
  `ComputeHash`, so the existing roll triggers — no new roll engine).
- `make manifests generate`; tests including the single-cluster local-copy path.

**unikraft-provider:**
- `internal/controller/instance_controller.go:58-105` — **honor scheduling gates**
  (requeue while any gate is present); this RFC owns introducing this.
- `internal/controller/instance_controller.go:189-195` — honor `Env.ValueFrom` and
  `EnvFrom` by resolving companion objects via the **upstream (cell)** client and
  inlining values into the kraftlet Pod `Env`.
- File mounts: reject/hold pending B1; map to UKC injection field when available.

## What review changed

### Round 2 + sequencing decision (2026-05-31)

A second review (fact-check, implementation/ops, product/strategy) plus a product
decision that **ConfigMaps/Secrets must ship before image-pull-credentials**
reshaped the design:

- **Dependency inverted — this RFC is now foundational and self-contained.** It
  *introduces* the resolver, the scoped project-plane client (B3), provider
  gate-honoring, and the gate; image-pull-credentials becomes a later consumer.
  Nothing here waits on that RFC.
- **Delivery model decided: management-plane companions.** The scoped-edge-read
  alternative (raised as the strongest hole — it would delete most of the
  machinery) is now recorded in [Alternatives](#alternatives-considered),
  steelmanned, and rejected for secret bytes because it widens edge blast radius;
  the resolution boundary stays in the management plane.
- **Rotation decided: no auto-roll + an explicit restart path.** Rewritten to reuse
  the existing in-place ordered roll (template metadata already feeds `ComputeHash`)
  via a `restartedAt` template annotation — no new roll engine. Auto-roll rejected
  as a surprising fleet-wide mass-roll; UKC can't update a running instance's env
  regardless.
- **Round-2 gaps closed:** fan-out (one hub object, N Karmada replicas);
  ref-counting + finalizer lifecycle for shared companions; the federator-vs-resolver
  ordering race (label selector always present); the gate name contract (decided —
  `expected-referenced-data` annotation propagated on the WD); observability
  (`ReferencedDataReady` Reason enum + events + metrics); single-cluster local-copy
  path made explicit; the SAR mechanism confirmed against the Network check
  (`UserInfo` from the admission request).

### Round 1

The first review round (technical-correctness and security/product) reshaped the
initial draft:

- **The cell-side gate-clearing reconciler does not exist** and the original draft
  hand-waved it. Now called out as net-new work, homed in the cell reconciler
  beside the quota-gate clear, with an explicit **expected-companion-name contract**
  to avoid a stuck-forever gate from naming drift.
- **Provider client confusion.** The draft said "resolve on the cell" without
  noting the provider's two clients. Pinned: companions are read via the
  **upstream** (cell) client; the downstream kraftlet cluster has no copy.
- **Downstream-Pod secret exposure** was dismissed as "infra-internal." Hardened
  into two explicit obligations (never write the Pod back to the hub; lock down
  downstream RBAC) plus a test, rather than an assertion.
- **Authorization** was missing entirely. Added a same-namespace constraint and a
  `SubjectAccessReview` at admission, mirroring the existing Network check, so a
  user can't name an object they can't read (confused-deputy risk).
- **Etcd-at-rest** assumption made explicit, consistent with image-pull-credentials.
- **Product happy-path** narrative added up front; the draft led with the security
  guarantee rather than what users gain.
- **Unproven concerns down-weighted:** Karmada multi-kind `resourceSelectors` is a
  standard feature (one policy, multiple selectors, same placement) and was
  confirmed workable; the ordering race is genuinely mitigated by the gate; all
  spot-checked file:line citations held.
- **Phase 2 / GCP claim corrected.** A follow-up review of `infra-provider-gcp`
  showed it mounts ConfigMaps/Secrets today (cloud-init `write_files`; Secret
  Manager + boot script) but does **not** use companion delivery — it reads the
  originals from its upstream and transforms inline. The original "GCP unblocked
  immediately" line was misleading: what's provider-agnostic is the Instance
  volume *API*, not the delivery mechanism. Phase 2 was also expanded with the
  precise UKC capability gap (no `files`/`user_data`/volume-content API), the
  recommended upstream ask, the projected-volume fidelity target, and a
  workaround analysis concluding none is an acceptable platform answer.

## Decided (previously open)

- **Delivery model:** management-plane companions (not scoped-edge-read). See
  [Alternatives](#alternatives-considered).
- **Rotation:** no auto-roll; explicit restart via a `restartedAt` template
  annotation. See [Rotation and rollout](#rotation-and-rollout).
- **Gate name contract:** explicit `expected-referenced-data` annotation propagated
  on the WorkloadDeployment, not recomputed names.
- **One resolver, not two:** this RFC builds the general resolver; pull secrets are
  a later consumer.
- **Sequencing:** ships before image-pull-credentials; this RFC owns the scoped
  client and provider gate-honoring.

## Open questions for human decision

1. **File-mount UX under B1:** reject at admission, or accept-and-hold with a
   clear condition (`FileMountsUnsupported`)? Reject is honest; hold lets the same
   Workload "just work" once UKC ships injection.
2. **Scoped project-plane client (B3) granularity:** can Milo IAM scope the
   resolver's read to specific types/labels per project namespace, or is it broad
   `configmaps,secrets: [get,list,watch]`? This RFC owns building the client;
   image-pull-credentials inherits whatever scoping is chosen here.
3. **Companion size/key limits** and behavior on breach (per-object and aggregate
   across a WorkloadDeployment fanned out to N cells).
4. **Does `EnvFrom` belong in v1 of this work**, or is per-key `ValueFrom`
   sufficient for the first release?
5. **VM runtime (`VirtualMachineRuntime`)** consumption is out of scope for the
   Unikraft provider (sandbox-only today) — confirm deferral.
