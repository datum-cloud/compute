# Scoping: Auto-creating an ALB / HTTP proxy to expose a Workload

## 1. The NetworkService API (PR 411)

**Status: `datum-cloud/network-services-operator#411` is OPEN and a DRAFT.** Branch `proto/network-service` → `main`. The body says verbatim: *"Draft: this is a working prototype to prove the design, not a merge candidate."* Design doc is `datum-cloud/enhancements#870`. Everything below can change.

**GVK:** `networking.datumapis.com/v1alpha`, `Kind: NetworkService`, **namespaced**. Defined in `api/v1alpha/networkservice_types.go` (new, 305 lines); CRD at `config/crd/bases/networking.datumapis.com_networkservices.yaml`; registered as an IAM `ProtectedResource` parented to `resourcemanager.miloapis.com/Project` in `config/iam/protected-resources/networkservices.yaml`, so it is a **user-facing, project-scoped** resource.

```yaml
apiVersion: networking.datumapis.com/v1alpha
kind: NetworkService
metadata: {name: storefront, namespace: default}
spec:
  networkInterfaces:                 # REQUIRED
    selector:                        # REQUIRED metav1.LabelSelector, CEL-validated non-empty
      matchLabels: {compute.datumapis.com/workload-name: storefront}
  ports:                             # REQUIRED, 1..16, unique name + unique number (CEL)
    - name: http                     # DNS label, <=63, ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$
      port: 8080                     # 1..65535
      protocol: TCP                  # optional, default TCP, enum{TCP} only
  trafficDistribution:               # optional, defaulted
    strategy: Nearest                # enum{Nearest} only
status:
  summary: {locations: 2, members: 6, healthy: 5}
  locations:                         # listType=map on name, MaxItems=64
    - {name: us-central-1, members: 3, healthy: 2, serving: true}
  conditions: [MembersResolved, Ready]   # both default-seeded Unknown/Pending
```

Key semantics:
- **The user gets no hostname or IP from a NetworkService.** Its status is membership/health only. The URL comes from the HTTPProxy in front of it.
- **Backends are selected by label, not referenced.** Nothing in the type names a Workload. Membership = `NetworkInterface` objects **in the same namespace** matching the selector (`internal/controller/networkservice_controller.go`, `matchingInterfaces(ctx, cl, service.Namespace, selector)`).
- Member health is read from the **new `HolderAvailable` condition on `NetworkInterface`** (`api/v1alpha/networkinterface_types.go`, +38 lines). NSO only ever writes it `Unknown`; True/False is the holder's (compute's) to write.
- Conditions: `MembersResolved` (reasons `NoMatchingInterfaces`, `MultipleNetworks`, `InvalidSelector`) and **`Ready` — "wait on this one rather than on what it summarizes."**

**Label plumbing (already working in this repo):** compute stamps `compute.datumapis.com/{workload-name,placement-name,city-code,instance-index}` on the `NetworkInterfaceClaim` it creates — `internal/controller/networkinterfaceclaim.go:220-238` (`desiredNetworkInterfaceClaimLabels`). PR 411's new `internal/controller/networkinterface_labels.go` allow-lists the `compute.datumapis.com/` prefix and copies those keys claim→interface, and stamps `networking.datumapis.com/location`. So **`matchLabels: {compute.datumapis.com/workload-name: <name>}` is the intended selector and needs no new labelling work in compute.**

**HTTPProxy gains a fourth backend form** (`api/v1alpha/httpproxy_types.go`, +48/-1):

```go
type NetworkServiceBackendRef struct {
    Name string `json:"name"` // NetworkService in the same namespace
    Port string `json:"port"` // names a spec.ports[].name, not a number
}
```
```yaml
rules:
  - backends:
      - networkService: {name: storefront, port: http}
```
Mutually exclusive with `endpoint`/`connector`/`instance` (CEL). **Backend TLS is rejected for `networkService` backends** — the edge always reaches members over plaintext HTTP. New reasons: `NetworkServiceBackendNotFound`, `NetworkServiceMembersUnreferenced` (>100 members: the proxy serves the first shard and says so).

**Gateway API relation:** indirect. HTTPProxy reuses `gatewayv1` types for `Hostname`, `HTTPRouteMatch`, `HTTPRouteFilter`, `GatewayStatusAddress`, and NSO translates it into downstream Gateway/EnvoyProxy resources. The CLI never touches Gateway API objects.

**Where the URL comes from** (already in the pinned dep, `api/v1alpha/httpproxy_types.go:240-280`):
- `status.canonicalHostname` — platform-managed stable `<uid>.datumproxy.net`. **This is the zero-config URL.**
- `status.addresses`, `status.hostnameStatuses[]` (per-hostname `Verified`, `DNSRecordProgrammed`, `Available`, `CertificateReady`).
- Conditions: `Accepted`, `Programmed`, `HostnamesVerified`, `CertificatesReady`, `DNSRecordsProgrammed`.
- `spec.hostnames` is **optional**; a custom hostname needs a verified `Domain` in the same namespace (auto-created if absent, but still requires user verification).

## 2. The CLI today

Entry point `cmd/datumctl-compute/main.go` → `internal/cmd/compute/root.go` (50 lines): `plugin.NewRootCmd("compute", …)` from `go.datum.net/datumctl/plugin`, which supplies persistent `--org`, `--project`, `-o/--output`. A `PersistentPreRunE` runs `util.RunActivationGate`. Subcommands are plain cobra `Command()` constructors registered at `root.go:35-46`.

- **Client:** `internal/cmd/compute/util/client.go:34-69`. `client.New` (controller-runtime), bearer token from `plugin.Token()`, host = `https://<apiHost>/apis/resourcemanager.miloapis.com/v1alpha1/projects/<project>/control-plane`. Scheme registers `computev1alpha`, **`networkingv1alpha` (already!)**, `locationsv1alpha1`, `quotav1alpha1`. Everything lives in namespace `"default"` (`util.ResourceNamespace`, `client.go:24`).
- **Workload creation:** `internal/cmd/compute/deploy/deploy.go:131-263`. Typed structs, `c.Get` → `c.Create`/`c.Update`. `--port` produces exactly one `NamedPort{Name: "http", Port: n, Protocol: TCP}` (`deploy.go:183-187`). One interface on network `"default"` (`deploy.go:210-215`).
- **Precedent for auto-creating a dependent networking resource:** `ensureNetwork` (`deploy.go:384-428`) checks for the `Network`, prompts `"Create it now? (Y/n)"`, creates a minimal auto-IPAM `Network`, refuses in non-interactive mode without `--yes`. **The exposure flow should mirror this exactly.**
- **Readiness waiting:** `internal/cmd/compute/watch/watch.go:39-92` — 2s `time.Ticker` poll of `WorkloadDeploymentList` selected by `compute.datumapis.com/workload-uid`, tabwriter rows, `signal.NotifyContext` for Ctrl-C detach.
- **Status/conditions helpers:** `internal/cmd/compute/util/conditions.go` — `FindCondition`, `ReadinessBlock` (with an explicit rule: *"Callers must not branch on specific reason values — display whatever the server emits"*), `InstanceStatus`/`InstanceStatusDetail`.
- **Output:** `util/printer.go` (`PrintJSON`/`PrintYAML`), `util/table.go` (`NewTabWriter`). `-o yaml/json` exists **only on read commands** (`workloads`, `instances`, `quota`, `access`).
- **No `--dry-run` anywhere in the plugin.** A grep over `internal/cmd/` and `cmd/` returns nothing.
- **Delete:** `internal/cmd/compute/destroy/destroy.go:82-84` deletes only the `Workload`.
- **Completions:** `util/completion.go` — `CompleteWorkloadNames`, `CompleteCityCodes`, `CompleteOutputFormats`.

**Dependency status (the key finding):** `go.mod:14-17` already pins `go.datum.net/network-services-operator v0.26.1-0.20260821014231-aceb24b1b569` as a **direct** dependency, with a comment saying it is pinned to main for the `Prepared` condition. That pin **has `httpproxy_types.go` but NOT `networkservice_types.go`**, and its `HTTPProxyRuleBackend` has no `NetworkService` field. Re-pinning is required and is the hard blocker. Once re-pinned, **no scheme change is needed** — PR 411 adds `NetworkService`/`NetworkServiceList` to the existing `SchemeBuilder` in `api/v1alpha/groupversion_info.go` (+4), which `util/client.go:53` already calls.

Compute also already writes the condition NSO reads: `internal/controller/networkinterface_holder.go:27` declares `networkInterfaceHolderAvailable = "HolderAvailable"` (as a local literal; `datum-cloud/compute#254`, still open, swaps it for the NSO constant).

## 3. Proposed UX

### Alternative A — flag on `deploy`
```
datumctl compute deploy api --image=… --city=DFW --port=8080 --expose-http
datumctl compute deploy api … --expose-http --hostname=api.example.com
```
Pros: single command from source to URL; matches the DX arc in `docs/enhancements/datumctl-compute-dx.md` (whose interactive mock at line 91 already prompts `? Expose port (optional): 8080`). Cons: no way to expose an existing workload; no way to unexpose without editing a manifest; couples exposure lifecycle to deploy lifecycle; `deploy -f workload.yaml` has nowhere sensible to put the flag.

### Alternative B — dedicated `expose` verb group
```
datumctl compute expose <workload> [--port=8080|--port-name=http] [--hostname=…] [--wait] [-o yaml]
datumctl compute expose status <workload>
datumctl compute unexpose <workload>
```
Pros: exposure has its own lifecycle (hostnames, DNS, certs, delete semantics) and deserves its own verbs; works on existing workloads; `unexpose` is discoverable; `expose -o yaml` gives a manifest-first path without inventing `--dry-run`. Cons: two commands to a URL.

### Recommendation: **B as the foundation, A as a thin caller.**

Build `internal/cmd/compute/expose/` with an exported `Ensure(ctx, c, out, opts)` that owns all resource construction and readiness. Then `deploy --expose-http` is ~10 lines calling `Ensure` after `watch.Rollout` returns. Rationale:

1. Exposure state must be inspectable and removable independent of the workload — `expose status` / `unexpose` are not optional, so B's surface has to exist regardless. A alone cannot get there.
2. The custom-hostname path (Domain verification, TXT records, cert issuance) is inherently multi-step and interactive; it needs a command that can be re-run to poll. Bolting that onto `deploy` makes `deploy` unpredictable.
3. `deploy` already sets the precedent for auto-creating a dependency with a prompt (`ensureNetwork`), so `--expose-http` fits naturally as sugar without owning the logic.
4. `expose -o yaml` printing the two objects gives the manifest-driven users (`deploy -f`) what they need, and is cheaper than retrofitting `--dry-run` across the plugin.

**Recommended phase-1 scope: no `--hostname`.** Ship the zero-config path only — `status.canonicalHostname` gives a working `https://<uid>.datumproxy.net` with a platform-managed cert and no user DNS. Defer custom hostnames to phase 2.

Proposed output:
```
$ datumctl compute expose api --port=8080
  networkservice/api created  (selector: compute.datumapis.com/workload-name=api)
  httpproxy/api created
Waiting for endpoints and edge programming. Ctrl-C to detach.

  RESOURCE            STATE
  networkservice/api  Ready (2 locations, 4 members, 4 healthy)
  httpproxy/api       Programmed

  https://a1b2c3d4.datumproxy.net
```

## 4. Resources created, order, ownership, deletion

Order (each `Get` → `Create`-or-`Update`, matching `deploy.go:238-249`):

1. **Preflight.** Workload exists; resolve the port. Prefer an existing `NamedPort` from `workload.Spec.Template.Spec.Runtime.Sandbox.Containers[*].Ports` (or `VirtualMachine.Ports`); require `--port`/`--port-name` only when ambiguous. Fail early with a clear message if the workload declares no ports.
2. **`NetworkService/<workload>`** in `default`:
   - `spec.networkInterfaces.selector.matchLabels = {compute.datumapis.com/workload-name: <workload>}` (constant `computev1alpha.WorkloadNameLabel`, `api/v1alpha/labels.go:23`).
   - `spec.ports = [{name: <sanitized port name>, port: <n>, protocol: TCP}]`.
   - Leave `trafficDistribution` unset (the type comment explicitly says *"Leave it unset"*).
3. **`HTTPProxy/<workload>`** in `default`: `spec.rules[0].backends[0].networkService = {name: <workload>, port: <name>}`. Omit `spec.hostnames` in phase 1. Omit `matches` (CRD defaults to `PathPrefix: /`). Never set backend `tls`.
4. **Wait** (opt-out with `--no-wait`): poll `NetworkService` for `Ready=True`, then `HTTPProxy` for `Programmed=True` and non-empty `status.canonicalHostname`. Surface blocking reason+message verbatim via `util.ReadinessBlock`, per the existing house rule.

**Ownership and labels** — set on both objects:
- `ownerReferences: [{apiVersion: compute.datumapis.com/v1alpha, kind: Workload, name, uid, controller: false, blockOwnerDeletion: false}]`. Same namespace, so this is legal; cross-group is fine.
- `labels: {compute.datumapis.com/workload-name: <name>, compute.datumapis.com/workload-uid: <uid>}` — existing constants `WorkloadNameLabel` / `WorkloadUIDLabel` (`api/v1alpha/labels.go:19-23`). The labels, not the ownerRef, are what `expose status` and `unexpose` list on; they also survive a project control plane whose GC behaviour is unverified.

**On delete:**
- `unexpose <workload>`: delete `HTTPProxy` first, then `NetworkService` (proxy-first avoids a window where the proxy reports `NetworkServiceBackendNotFound`), selected by the UID label. Warn that any `Domain` created for a custom hostname is deliberately left behind (it is a project-level ownership record, not per-workload).
- `destroy <workload>` (`destroy.go`): list the labelled `HTTPProxy`/`NetworkService`, include them in the confirmation summary, and delete them explicitly rather than trusting owner-reference GC. Do not silently rely on GC until it is confirmed to run in project virtual control planes.
- The `Network` is never deleted (consistent with `ensureNetwork` never cleaning up).

## 5. Implementation plan

**Dependencies**
- `go.mod:14-17` — re-pin `go.datum.net/network-services-operator` to a commit carrying `NetworkService` + the `networkService` backend field. **Blocked on PR 411 merging** (it is an explicit non-merge-candidate today). Update the existing pin comment, which currently explains the `Prepared` condition rationale.
- No new modules. `sigs.k8s.io/gateway-api v1.5.1` is already required (`go.mod:29`) for the `gatewayv1.Hostname` types.

**Scheme registration:** none. `networkingv1alpha.AddToScheme` at `internal/cmd/compute/util/client.go:53` picks up the new kinds automatically. Add a defensive `meta.IsNoMatchError` check so an older control plane yields *"HTTP exposure is not available in this project"* rather than a raw REST mapper error.

**Files to touch**

| File | Change |
|---|---|
| `go.mod` / `go.sum` | Re-pin NSO |
| `internal/cmd/compute/expose/expose.go` *(new)* | `Command()`, `Ensure()`, `Remove()`, `Status()` |
| `internal/cmd/compute/expose/resources.go` *(new)* | Pure builders: workload → `NetworkService` + `HTTPProxy`. Unit-testable, no client. |
| `internal/cmd/compute/expose/wait.go` *(new)* | Ticker-based readiness, modelled on `watch/watch.go:39-92` |
| `internal/cmd/compute/expose/*_test.go` *(new)* | Builder table tests + fake-client flow tests |
| `internal/cmd/compute/root.go:35-46` | Register `expose.Command()`, `unexpose.Command()` |
| `internal/cmd/compute/deploy/deploy.go` | `--expose-http` flag (`~line 85`), validation in `runDeploy`, call `expose.Ensure` after `watch.Rollout` (`:262`) |
| `internal/cmd/compute/destroy/destroy.go:55-84` | List + summarize + delete exposure resources |
| `internal/cmd/compute/util/conditions.go` | `NetworkServiceStatus()` / `HTTPProxyStatus()` summarizers, same shape as `InstanceStatus` |
| `internal/cmd/compute/util/completion.go` | `CompleteExposedWorkloads` for `unexpose` |
| `docs/enhancements/datumctl-compute-dx.md` | Update the interactive mock (line 91) and add the exposure flow |

**RBAC:** the CLI acts as the end user via `plugin.Token()`; there is no service account to grant. The user needs `networking.datumapis.com/networkservices.{create,get,list,watch,update,patch,delete}` and the equivalent `httpproxies` permissions. PR 411 adds `networkservices.{create,update,patch,delete}` **only to `config/iam/roles/networking-admin.yaml`** and read verbs to `networking-viewer.yaml`. **A project member holding only compute roles will get a 403.** This is a cross-repo prerequisite: either the compute roles need these permissions, or the docs must state that `networking-admin` is required to expose a workload. Worth raising with the networking team before build starts.

## 6. Open questions and risks

1. **PR 411 is a draft prototype, not a merge candidate.** Everything is blocked on it. Treat all field names as provisional.
2. **The PR body contradicts the code.** The body's YAML example uses `spec.networkInterfaceClaims:`, but the Go type is `NetworkInterfaces NetworkServiceInterfaceSelector` with json tag `networkInterfaces`, and the CRD/chainsaw test both use `networkInterfaces`. Confirm which survives before writing builders.
3. **Biggest functional risk — membership may not resolve at all.** The controller lists `NetworkInterface` objects **in the NetworkService's own namespace**. Compute creates claims in the cell control plane: *"the claim is served by the control plane the instance runs in"* (`internal/controller/networkinterfaceclaim.go:69-72`). PR 411 states plainly: *"claims are not published to the consumer's project, so membership currently resolves where the claims already are."* Until interfaces are projected into the project control plane, a CLI-written NetworkService in `default` will sit at `MembersResolved=False/NoMatchingInterfaces` forever. **Verify this against a real staging project before committing to the design.**
4. **Multi-city is the default and is currently broken.** PR 411: *"a service with members in two locations binds no VRF and fails every request"* until `datum-cloud/cloud#16` lands. `deploy --city=DFW,IAD` is the documented happy path (`deploy.go:60`). The CLI must detect >1 city and warn loudly, or refuse, rather than producing a silently non-serving URL.
5. **No location coordinates exist yet**, so `Nearest` ranking is not actually computable — cross-location behaviour is untested end to end (single-cell environment).
6. **Single network per service.** A workload with interfaces on two networks yields `MultipleNetworks`. Today `deploy` hardcodes one interface on `"default"` (`deploy.go:210-215`), so this is safe now but fragile; consider adding the network to the selector.
7. **100-member cap.** Past it the proxy serves the first shard and reports `NetworkServiceMembersUnreferenced`. `expose status` must surface it.
8. **Port-name mismatch.** `computev1alpha.NamedPort.Name` (`api/v1alpha/instance_types.go:254-258`) has no pattern constraint; `NetworkServicePort.Name` requires `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, <=63. Needs sanitization with a clear error. `deploy` currently hardcodes `"http"` (`deploy.go:185`), which is safe.
9. **Plaintext to the backend, always.** `networkService` backends reject `tls`. Users terminating TLS in their container will break. Must be documented.
10. **DNS/certs.** Phase 1 is free — `<uid>.datumproxy.net` with platform-managed A/AAAA and certs. Phase 2 (`--hostname`) drags in `Domain` creation, TXT/HTTP verification, `HostnamesVerified`/`CertificatesReady`/`DNSRecordsProgrammed`, and hostname-uniqueness conflicts across the whole platform. Substantially more work than phase 1.
11. **`HolderAvailable` string coupling.** Compute writes the literal `"HolderAvailable"` (`internal/controller/networkinterface_holder.go:27`). If NSO renames it, compute keeps compiling and every member silently reads unhealthy. `datum-cloud/compute#254` fixes this and should land first.
12. **Multi-tenancy.** All resources go to namespace `default` in the project's virtual control plane, so naming collides on the workload name — acceptable, and it makes exposure idempotent per workload, but it means one workload cannot have two proxies.
13. **Alpha API, no conversion guarantees.** Handle `NoKindMatch` gracefully.

## 7. Effort estimate

| Chunk | Est. |
|---|---|
| Re-pin NSO, verify build + scheme, `NoKindMatch` guard | 0.5 d *(gated on PR 411)* |
| Resource builders + table tests (`resources.go`) | 1.5 d |
| `expose` / `unexpose` / `expose status` commands, fake-client tests | 2 d |
| Readiness watcher + status summarizers + condition messaging | 2 d |
| `deploy --expose-http` wiring + interactive prompt | 1 d |
| `destroy` cascade, ownership/labels, completions | 1 d |
| Docs (`datumctl-compute-dx.md`) | 0.5 d |
| **Phase 1 total** | **~8.5 dev-days** |
| Phase 2: `--hostname`, Domain creation + verification UX, cert/DNS status | +4 d |
| Cross-repo prerequisites (IAM roles, interface projection, `#254`) | not estimated — external |

Add ~1 d of slack for API churn while PR 411 is a draft.
