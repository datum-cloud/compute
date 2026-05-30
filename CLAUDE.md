# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`compute` defines the APIs and core controllers for the `compute.datumapis.com`
API group (`Workload`, `WorkloadDeployment`, `Instance`). It does **not**
provision infrastructure itself — it expresses intent that infrastructure
providers (e.g. `infra-provider-gcp`) act on, and it federates deployments
across edge/POP cells via Karmada. Networking types (`Network`,
`NetworkBinding`, `SubnetClaim`) come from the separate
`network-services-operator` repo.

## Commands

Build/test/lint use **Make**. The e2e environment is driven by **Task** (`Taskfile.yaml`).

```sh
make build        # build bin/manager (runs manifests, generate, fmt, vet first)
make run          # run controller locally against ~/.kube/config
make test         # envtest-backed unit + integration tests (excludes /e2e)
make lint         # golangci-lint (v2.1.5); lint-fix to auto-fix
make manifests    # regenerate CRDs/RBAC/webhook config (controller-gen)
make generate     # regenerate zz_generated.deepcopy.go + defaults
```

After editing any `*_types.go` in `api/v1alpha/`, run `make manifests generate`
(or just `make build`/`make test`, which depend on both).

**Single test** — `make test` resolves envtest assets via `KUBEBUILDER_ASSETS`.
For integration tests that need an apiserver, set it first:

```sh
export KUBEBUILDER_ASSETS=$(bin/setup-envtest use 1.31.0 --bin-dir bin -p path)
go test ./internal/controller/... -run TestName        # testify
go test ./internal/controller/... -args -ginkgo.focus "pattern"   # Ginkgo specs
```

Pure unit tests (no apiserver) run with plain `go test ./pkg/...`.

**E2E** (Kind + Karmada, Chainsaw): `task e2e:up` to stand up clusters and join
them to Karmada, `task e2e:test` to run, `task e2e:down` to tear down.

Tool versions are pinned in `Makefile`: envtest K8s `1.31.0`, controller-gen
`v0.16.4`, golangci-lint `v2.1.5`. Boilerplate header (AGPL-3.0-only) is
`hack/boilerplate.go.txt`.

## Architecture

### Resource hierarchy

A single `Workload` fans out into many `Instance`s through two levels, each
owned and reconciled by the level above:

```
Workload  ──(per placement × city)──▶  WorkloadDeployment  ──(per replica)──▶  Instance
```

- **Workload** (`api/v1alpha/workload_types.go`) — user-facing. Holds an
  instance template and a list of `Placements` (city codes + min/max replicas).
- **WorkloadDeployment** (`workloaddeployment_types.go`) — one per
  city/placement. Status aggregates its instances' replica counts/conditions.
- **Instance** (`instance_types.go`) — a single container (`SandboxRuntime`) or
  VM (`VirtualMachineRuntime`). Carries network interfaces, volumes, scheduling
  gates, and quota conditions.

Objects are stamped with `compute.datumapis.com/workload-uid` and
`...workload-deployment-uid` labels for indexed lookups (`internal/controller/indexers.go`).

### Controllers are split by plane

The binary (`cmd/main.go`) enables controller sets via flags. The two planes run
in different clusters in production:

**Management plane** (`--enable-management-controllers`):
- `WorkloadReconciler` — Workload → desired WorkloadDeployments; aggregates status back.
- `WorkloadDeploymentFederator` — replicates project-namespace WorkloadDeployments
  into the downstream Karmada control plane and creates a `PropagationPolicy` per
  city code (city-code label selector routes to matching cells). Federation only
  (requires `--upstream-kubeconfig`).
- `InstanceProjector` — watches Instances that edge cells wrote back to Karmada
  and creates read-only Instance projections in the originating project cluster,
  owned by the WorkloadDeployment. Federation only.

**Cell / edge plane** (`--enable-cell-controllers`):
- `WorkloadDeploymentReconciler` — drives instance lifecycle via the instance-control
  strategy, reconciles networking (NetworkBinding/SubnetClaim), manages scheduling gates.
- `InstanceReconciler` — manages quota (`ResourceClaim` against Milo project control
  planes), clears the quota scheduling gate when granted, and writes the Instance
  back upstream/to Karmada for projection.

### Instance control strategy

`internal/controller/instancecontrol/` computes Create/Update/Delete/Wait actions
for a deployment's instances. The `stateful/` implementation behaves like a
StatefulSet: ordered creation (wait for Ready before next), reverse-order updates
and deletes, template-hash tracking for rolling updates. Scheduling gates
(`scheduling_gates.go`) block an instance until networking and quota are ready.

### Multi-cluster (Milo + multicluster-runtime)

Controllers run on `sigs.k8s.io/multicluster-runtime` (`mcmanager.New`) with a
pluggable cluster provider chosen by discovery mode (`internal/config/config.go`):

- **Single** mode — one local cluster (`mcsingle`), no project discovery.
- **Milo** mode (`milomulticluster`) — each Milo project becomes a runtime cluster;
  the cluster name doubles as the `projectID`.

Reconcilers receive `mcreconcile.Request` (carries `ClusterName`). `InstanceProjector`
is the exception — it uses a plain single-cluster `manager.Manager` pointed at the
downstream Karmada control plane.

### Quota (Milo)

`internal/quota/` — `ProjectQuotaClientManager` caches per-project clients, each
rewriting the REST host to
`/apis/resourcemanager.miloapis.com/v1alpha1/projects/{projectID}/control-plane`.
`InstanceReconciler` creates a `ResourceClaim` in the project's control plane,
watches it (`PendingEvaluation` → `QuotaAvailable`/`QuotaExceeded`), and removes
the quota scheduling gate on grant. **Claims are immutable — created once, never
updated** (the absent Update path is intentional). Quota is optional: a missing
quota kubeconfig means quota-disabled, not fatal.

### Federation data flow (when `--upstream-kubeconfig` is set)

1. Federator replicates WorkloadDeployments into Karmada namespace `ns-{project-uid}`
   (via Milo's `MappedNamespaceResourceStrategy`) and creates city-code PropagationPolicies.
2. Karmada propagates each WorkloadDeployment to POP cells whose label matches the city code.
3. Edge `WorkloadDeploymentReconciler`/`InstanceReconciler` create Instances + ResourceClaims,
   then write Instances back to Karmada (labeled with owner cluster/namespace/deployment-uid).
4. `InstanceProjector` resolves those labels and projects each Instance back into the
   project cluster for the user to observe; status flows back up the same chain.

## Working with subagents

This is a large multi-cluster codebase — controller flows, generated code, and
federation paths span many files. Keep the main thread as an **orchestrator**:
delegate substantive work to subagents and synthesize their results, rather than
loading raw file dumps and command output into the main context.

- **Delegate by default.** Diagnosis, code search, cluster/`kubectl` checks,
  multi-file edits, and PR prep should run in subagents. The main thread plans,
  dispatches, and integrates the conclusions.
- **Read-only fan-out → `Explore`.** Use it to locate code or trace a flow across
  many files when you only need the conclusion, not the file contents.
- **Match the specialized agent to the task** (see their descriptions):
  `datum-platform:plan` for design before implementation;
  `datum-platform:api-dev` for Go on the API server/controllers;
  `datum-platform:test-engineer` for tests; `datum-platform:sre` for
  Kustomize/CI/RBAC/manifests; `datum-platform:code-reviewer` as a post-change
  gate; `datum-platform:tech-writer` for docs.
- **Parallelize independent work** — dispatch multiple agents in one turn when
  their tasks don't depend on each other.
- **Give each agent a self-contained brief.** It doesn't share the main thread's
  context: state the goal, the relevant paths, and the exact shape of the result
  you want back. Have it return findings/paths, not large verbatim file contents.
- **Don't double-run.** Once a search or task is delegated, wait for the result
  instead of also doing it inline.

## GitHub commentary (PRs, issues, comments)

Use the `gh` CLI and invoke the Datum convention skills **before** writing, so
all GitHub content matches the platform's house style.

**Lead with the product, not the implementation.** Frame PRs, issues, and
comments around what changes for users and operators of the platform — the
capability gained, the problem solved, the behavior they'll observe — before the
technical detail. Open with the user/product impact (e.g. "Workloads now
schedule across POP cells by city" rather than "Adds WorkloadDeploymentFederator
and a PropagationPolicy per city code"), then explain the implementation as
supporting context. Issues should describe the user-facing gap or desired
outcome first; PR summaries should answer "what can the platform now do, and why
does it matter" before "how it's wired." Keep the mechanism — it matters for
review — but make product value the headline.

- **Pull requests** — invoke `datum-platform:pr-conventions` before drafting the
  title and body. Titles follow the commit format (`<type>(<scope>): <subject>`,
  imperative, ≤72 chars); the body uses the skill's required sections (Summary,
  etc.). End PR bodies with the Claude Code attribution footer.
- **Commits** — invoke `datum-platform:commit-conventions` for message format
  (`<type>: <subject>` types: feat/fix/docs/refactor/test/chore) and include the
  `Co-Authored-By` trailer. Only commit/push when the user asks.
- **Issues and review comments** — apply the same tone and structure: a clear
  prose summary first, scannable bullets only where they help, type-prefixed
  titles for issues, and concrete file/line references (`path:line`). Keep
  comments specific and actionable rather than generic.

Don't open/merge PRs, push, or post comments unless the user has asked or
durably authorized it — these are outward-facing actions.

## Conventions

This repo follows the shared Datum platform conventions. Relevant skills:
`datum-platform:go-conventions`, `:controller-runtime-patterns`,
`:k8s-apiserver-patterns`, `:capability-quota`, `:commit-conventions`,
`:pr-conventions`. golangci excludes `lll` for `api/*` and `dupl` for `internal/*`.

### Code comments

Comment to explain **why**, not **what**. The code already shows what it does;
a comment earns its place by capturing the non-obvious reason, constraint, or
consequence a future reader can't infer from the code itself.

- **Why, not what.** Don't narrate the code (`// loop over instances`,
  `// set the label`). Explain the rationale: the invariant being upheld, the
  edge case being guarded, the external contract being honored.
- **Be concise.** One tight sentence usually beats three. Cut a comment down to
  the load-bearing fact; if the code is self-explanatory, write no comment.
- **Go-forward only.** Comments describe the code as it stands now, not the
  change that produced it. No diff narration, fix history, or "previously
  this…" / "now we…" storytelling — that belongs in the commit message or PR,
  not the source. A reader on `main` a year from now has no diff in view.
- **Don't restate identifiers.** If the function/variable name already says it,
  the comment adds nothing.

Bad: `// Overwrite the WD UID label with the project-cluster WD UID because the
downstream Instance carries the cell-plane UID assigned by Karmada when it
propagated the WD, which never matches…` (verbose, narrates the change).
Good: `// Cell-plane Instances carry the Karmada WD UID; project-side
label lookups need the project WD UID.`
