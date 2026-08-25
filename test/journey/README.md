# Consumer deployment journey (k6)

A black-box end-to-end suite that walks the path a Datum customer actually
walks and asserts on what the control plane *does*, not just on HTTP status
codes:

```
project → subscribe to compute → create a network → deploy a workload to a
location → instances become Ready → tear down
```

It talks to the public Datum API over HTTPS with a bearer token. There is no
dependency on `datumctl`, a kubeconfig, or in-cluster credentials, so the same
file runs on a laptop under `k6 run` and in-cluster under
[k6-operator](https://github.com/grafana/k6-operator).

- Script: [`src/consumer-deployment.js`](src/consumer-deployment.js)
- Deployment manifests: [`../../config/components/k6-consumer-journey`](../../config/components/k6-consumer-journey)

## What it asserts

Each of these is a named check *and* a sample on `datum_stage_success`, so a
threshold gates the whole journey.

| Stage | Assertion |
| --- | --- |
| Project | `status.conditions[Ready]=True`, and the per-project control plane serves requests |
| Subscription | `ServiceEntitlement` for networking and compute reaches `phase=Active` with `Ready=True` |
| Network | `Ready=True` **and** `IPAMAllocated=True` with a real prefix in `status.ipam` |
| Workload | `Available=True`, and `status.readyReplicas` equals what was requested |
| Placement | a `WorkloadDeployment` appears, becomes `Available=True`, and reports a concrete `status.location.name` |
| Network reach | a `NetworkContext` exists for the (network, location) pair the deployment landed in, and is `Ready=True` |
| Instances | every `Instance` reports `Ready`, `Available`, `Programmed` and `QuotaGranted`, and holds an address of the requested IP family |
| Teardown | workload and network delete, instances are garbage collected, and no `NetworkContext` or `NetworkBinding` survives |

Every transition is timed into a k6 `Trend`, so a run answers *how long Datum
takes to do each thing*:

```
datum_project_ready                 datum_workload_available
datum_project_control_plane_ready   datum_workload_deployment_available
datum_entitlement_active            datum_network_context_ready
datum_network_ready                 datum_first_instance_observed
datum_journey_total                 datum_instance_ready
datum_workload_teardown             datum_instance_address_assigned
datum_network_teardown              datum_project_teardown
```

Counters flag platform findings rather than hiding them:
`datum_entitlement_gated`, `datum_orphaned_network_contexts`,
`datum_orphaned_network_bindings`, `datum_leaked_resources`.

There are no fixed sleeps. Every readiness wait is a predicate polled at
`DATUM_POLL_INTERVAL_SECONDS` against an explicit deadline, and a timeout
reports the last observed condition set.

The individual stage timeouts add up to more than any sensible wall clock, so
the deploy phase also runs under a single `DATUM_JOURNEY_BUDGET` and each wait
is clamped to whatever is left of it. Teardown sits outside that budget, so a
run in which every stage times out still tears itself down rather than being
killed by k6 with resources still live.

## Running locally

```bash
export DATUM_TOKEN="$(datumctl auth get-token)"
k6 run test/journey/src/consumer-deployment.js
```

Defaults target `datum-technology/datum-cloud` on
`https://api.staging.env.datum.net` and deploy one IPv6 instance to `DFW`.

Useful variations:

```bash
# Deploy to a different city, keep the resources for inspection afterwards.
DATUM_CITY_CODE=RDU DATUM_SKIP_TEARDOWN=true k6 run test/journey/src/consumer-deployment.js

# Exercise project creation and the subscription gate as well.
DATUM_CREATE_PROJECT=true k6 run test/journey/src/consumer-deployment.js

# Clean up after an aborted run (Ctrl-C, pod eviction, ...).
DATUM_RUN_ID=<run id from the log> DATUM_SWEEP_ONLY=true \
  k6 run test/journey/src/consumer-deployment.js
```

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATUM_API_ENDPOINT` | `https://api.staging.env.datum.net` | API endpoint |
| `DATUM_TOKEN` | *(required)* | Bearer token |
| `DATUM_ORG` | `datum-technology` | Organization |
| `DATUM_PROJECT` | `datum-cloud` | Project to reuse |
| `DATUM_CREATE_PROJECT` | `false` | Create a throwaway project per run |
| `DATUM_PROJECT_PREFIX` | `k6j` | Name prefix when creating one |
| `DATUM_REQUIRE_COMPUTE` | `true` in reuse mode, `false` in create mode | Fail if the compute subscription is not Active |
| `DATUM_NAMESPACE` | `default` | Namespace inside the project control plane |
| `DATUM_CITY_CODE` | `DFW` | Placement city code |
| `DATUM_PLACEMENT_NAME` | `us-central` | Placement name |
| `DATUM_REPLICAS` | `1` | `minReplicas` for the placement |
| `DATUM_IP_FAMILY` | `IPv6` | Network and interface IP family |
| `DATUM_IMAGE` | `docker.io/scotwells/datum-compute-hello-world:latest` | Container image |
| `DATUM_IMAGE_PULL_SECRET` | `dockerhub-pull` | Pull secret, which must already exist in the namespace; set to empty to omit |
| `DATUM_INSTANCE_TYPE` | `datumcloud/d1-standard-2` | Instance type |
| `DATUM_MTU` | `1460` | Network MTU |
| `DATUM_POLL_INTERVAL_SECONDS` | `5` | Poll interval for every wait |
| `DATUM_TIMEOUT_PROJECT_READY` | `180` | Seconds |
| `DATUM_TIMEOUT_CONTROL_PLANE` | `180` | Seconds |
| `DATUM_TIMEOUT_ENTITLEMENT` | `180` | Seconds |
| `DATUM_TIMEOUT_NETWORK_READY` | `300` | Seconds |
| `DATUM_TIMEOUT_WORKLOAD_AVAILABLE` | `900` | Seconds |
| `DATUM_TIMEOUT_INSTANCE_READY` | `900` | Seconds |
| `DATUM_TIMEOUT_NETWORK_CONTEXT` | `600` | Seconds |
| `DATUM_TIMEOUT_TEARDOWN` | `600` | Seconds |
| `DATUM_JOURNEY_BUDGET` | `1500` | Seconds; ceiling on the deploy phase. Stage timeouts are clamped to what is left of it so teardown always gets to run |
| `DATUM_MAX_DURATION` | `60m` | k6 scenario cap |
| `DATUM_RUN_ID` | generated | Override the run id |
| `DATUM_SKIP_TEARDOWN` | `false` | Leave everything behind |
| `DATUM_SWEEP_ONLY` | `false` | Delete this run id's leftovers and exit |
| `DATUM_SUMMARY_FILE` | `summary.json` | Where the JSON summary is written |

## Safety on shared environments

Staging is shared. The suite is written so it can only ever remove its own
resources:

- Every object it creates is named `k6-<runId>-*` and labelled
  `app.kubernetes.io/managed-by=k6-consumer-journey` **and**
  `test.datumapis.com/k6-run-id=<runId>`.
- Every delete goes through `safeDelete`, which re-reads the object and refuses
  unless both labels match the current run. Pre-existing resources — including
  the live `ipv6-hello` workload and its `ipv6-test` network — are never
  candidates.
- Teardown runs from a `finally` block, so a failed assertion still cleans up,
  and a final label-selector sweep in k6's `teardown()` catches anything the
  first pass missed.
- Each run creates its own network, so nothing shared can be stranded.

### The NetworkContext hazard

Deleting a `Workload` garbage-collects the `NetworkContext` for its
(network, location) pair, and a `NetworkBinding` that had already resolved to
that context is never reconciled again — see
[network-services-operator#401](https://github.com/datum-cloud/network-services-operator/issues/401).

Teardown is ordered around this: workload first, wait for its instances to
drain, then the run's own network, and finally an explicit check that no
`NetworkContext` or `NetworkBinding` referencing the run's network survived.
If one does, `datum_orphaned_network_contexts` goes non-zero and the
`no NetworkContext survives the run (nso#401)` check fails. That is a real
finding about the platform, and the suite reports it rather than cleaning up
around it.

## Running under k6-operator

The staging cluster already runs k6-operator in `k6-system` (see
`infra/infrastructure/testing/k6-operator`). The manifests in
[`config/components/k6-consumer-journey`](../../config/components/k6-consumer-journey)
follow the same shape as the activity load test:

- `kustomization.yaml` is a Component that generates the script ConfigMap from
  `generated/consumer-deployment.js` and pulls in the `TestRun`.
- `generated/consumer-deployment.js` is a checked-in copy of
  `src/consumer-deployment.js`, kept in sync with `task journey:generate`. The
  copy exists because a Flux OCI source cannot reference files outside the
  kustomization directory.

```bash
# Sync the generated copy after editing the source.
task journey:generate

# Validate without deploying.
task journey:validate

# Apply into a cluster that already has k6-operator.
kubectl apply -k config/components/k6-consumer-journey

# Watch it.
kubectl logs -l k6_cr=compute-consumer-journey -f
```

The token is **not** in the manifests. The `TestRun` expects a Secret named
`compute-k6-consumer-journey-token` in the same namespace containing a
`DATUM_TOKEN` key, provisioned out of band (ExternalSecret, sealed secret, or
manually):

```bash
kubectl create secret generic compute-k6-consumer-journey-token \
  --from-literal=DATUM_TOKEN="$(datumctl auth get-token)"
```

A bearer token from `datumctl auth get-token` is short-lived, so a scheduled
in-cluster run needs a long-lived service-account-style credential rather than
a copied user token. That credential does not exist yet — it is the one open
prerequisite for putting this on a schedule.

`parallelism` must stay at `1`: the operator would otherwise start several
runners that each generate their own run id but share the same target project,
which works but makes the metrics meaningless.

## Known platform behaviour this suite surfaces

- **Compute cannot be self-served.** `compute.datumapis.com` is
  `GatedByProvider`, so a newly created project's entitlement parks in
  `PendingApproval` until a human approves it. `DATUM_CREATE_PROJECT=true` can
  therefore create a project, request the subscription and delete the project
  again, but it cannot reach a running workload. Reuse mode is the default for
  that reason. `networking.datumapis.com` is self-service and goes `Active`
  within seconds.
- **A failed instance gives a consumer nothing to act on.** When an `Instance`
  goes `Ready=False`, the reason is `Failed` and the message is
  `Instance failed`, with no Events in the project control plane. Everything
  upstream (quota, addressing, VPC attachment) still reports healthy, so the
  suite can prove the platform broke but not why.
