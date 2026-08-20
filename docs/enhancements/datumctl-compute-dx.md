# `datumctl compute` — Developer Experience

**Status:** Draft

---

## Summary

This document proposes a `compute` subcommand group in `datumctl` designed around the workflows developers actually perform: deploying a workload, watching it roll out across cities, understanding why something isn't running, and inspecting instances when something goes wrong.

The goal is to close the gap between "I have a container image" and "my workload is healthy across multiple locations" without requiring developers to understand the platform's internal resource model or write YAML to do common things.

---

## The problem today

Running a workload on Datum Cloud today requires a developer to:

1. Write a YAML manifest with the correct `apiVersion`, `kind`, and nested spec structure.
2. Apply it with `datumctl apply -f` and wait with no visibility into what's happening.
3. Run `datumctl get workloads` to check status, and then manually interpret raw condition fields.
4. Look up individual instance names to get logs.

Each of these steps has friction that compounds. A developer who hits a quota block on their first deploy gets a raw API condition with no explanation and no next step. Someone who wants to tail logs from their app across two cities has to discover instance names, then run multiple commands.

This experience works. It doesn't feel like a product yet.

---

## Who this is for

The primary audience is a **backend developer** deploying a containerized service to Datum Cloud for the first time or as part of their daily workflow. They are comfortable with the terminal. They may have used Heroku, Railway, Fly.io, or GCP before. They should not need to know anything about how the platform's internal resource model works to deploy and operate their application.

The secondary audience is a **platform operator** or **DevOps engineer** who needs scripting-friendly access to the full resource hierarchy for automation and debugging.

---

## Workflows

The design centers on five workflows, ordered by frequency.

### 1. Deploy a workload

The developer has a container image. They want it running in one or more cities.

The fastest path requires no YAML:

```
$ datumctl compute deploy api \
    --image=ghcr.io/acme/api:1.4.2 \
    --instance-type=d1-standard-2 \
    --city=DFW,IAD \
    --min=2 \
    --port=8080

Resolving workload "api" in project acme-prod...
  Workload does not exist — creating.
  Placement "default": cities=[DFW, IAD], min=2

Applying...
  workload/api created

Waiting for rollout. Ctrl-C to detach (rollout continues in background).

  PLACEMENT  CITY  DESIRED  READY  PHASE
  default    DFW        2      0   Starting
  default    IAD        2      0   Starting
  default    DFW        2      2   Running
  default    IAD        2      2   Running

Rollout complete in 47s.

  Instances:
    DFW  api-dfw-0   203.0.113.10
         api-dfw-1   203.0.113.11
    IAD  api-iad-0   198.51.100.20
         api-iad-1   198.51.100.21

Saved workload config to ./workload.yaml — commit this file to manage deployments declaratively.
```

If a developer prefers an interactive walk-through:

```
$ datumctl compute deploy
? Workload name:                  api
? Container image:                ghcr.io/acme/api:1.4.2
? Instance type [d1-standard-2]:
? Cities (comma-separated) [DFW]: DFW,IAD
? Min replicas per city [1]:      2
? Expose port (optional):         8080

  workload:      api
  image:         ghcr.io/acme/api:1.4.2
  instance type: d1-standard-2
  cities:        DFW, IAD
  replicas:      min=2
  ports:         8080/tcp

Proceed? (Y/n)
```

For teams managing workloads declaratively, `deploy` also accepts a manifest file. It shows a human-readable diff before applying, rather than applying silently:

```
$ datumctl compute deploy -f workload.yaml

Changes to workload "api":
  image: ghcr.io/acme/api:1.4.1 → ghcr.io/acme/api:1.4.2
  min replicas (default/DFW): 2 → 3

Apply? (Y/n) y
workload/api updated
```

All three paths — flags, interactive, manifest — converge on the same underlying representation. A developer can start with flags and graduate to a manifest when they need multi-placement topology, custom networking, or volume configuration.

For automated pipelines, pass `-y` to skip the confirmation prompt. The CLI also suppresses the prompt automatically when stdin is not a terminal.

### 2. Check workload health

The developer wants to know if their workload is healthy and how many instances are running across each city.

```
$ datumctl compute status api

Workload     api                             project: acme-prod
Image        ghcr.io/acme/api:1.4.2
Updated      47s ago                         Revision #7

Health       Available — all placements at desired replicas

             CITY  READY  DESIRED  TYPE
  default    DFW   2/2    2        d1-standard-2
             IAD   2/2    2        d1-standard-2
```

When something is wrong, the status view explains it in plain terms and tells the developer what to do next:

```
$ datumctl compute status api

Workload     api                             project: acme-prod
Image        ghcr.io/acme/api:1.4.3
Updated      1m ago                          Revision #8

Health       Degraded — 2 instances blocked in IAD

             CITY  READY  DESIRED  TYPE
  default    DFW   2/2    2        d1-standard-2
             IAD   2/4    4        d1-standard-2   [degraded]

  IAD: 2 instances could not start — quota exceeded
    Requested 4 CPU. 2 CPU available in IAD.

  Next steps:
    Reduce replicas:   datumctl compute scale api --min=2
    Check quota:       datumctl compute quota
    View instances:    datumctl compute instances --workload=api
```

The developer never sees raw condition names or internal state reasons. If they need that level of detail for debugging or scripting, `datumctl compute workloads describe api` exposes it.

### 3. Watch a rollout

When a developer updates their workload (new image, changed replica count, config change), they can watch the rollout progress city by city:

```
$ datumctl compute rollout api

Rolling workload "api"  rev #7 → #8

  PLACEMENT  CITY  UPDATED  READY  OLD   PHASE
  default    DFW         0      2    2   Pending
  default    IAD         0      2    2   Pending
  default    DFW         1      1    1   Updating
  default    DFW         2      2    0   Done
  default    IAD         1      1    1   Updating
  default    IAD         2      2    0   Done

Rollout complete in 1m 12s.
```

If the rollout stalls because of a resource or scheduling issue, the output pauses on the affected row and gives an explanation:

```
  default    IAD         1      1    1   Blocked

    2 instances waiting: quota exceeded in IAD
    The rollout will resume when quota becomes available.
    Ctrl-C to detach — the rollout continues in the background.
```

`Ctrl-C` always detaches from the watch. It never cancels the rollout itself.

Rollout history is accessible at any time:

```
$ datumctl compute rollout history api

  REV   WHEN          IMAGE                        CHANGES               BY              STATUS
  #8    2m ago        ghcr.io/acme/api:1.4.3       image updated         alice@acme.io   active
  #7    3h ago        ghcr.io/acme/api:1.4.2       min replicas 2 → 3    ci-deploy       —
  #6    yesterday     ghcr.io/acme/api:1.4.2       LOG_LEVEL info → warn  bob@acme.io    —
```

To roll back to a previous revision:

```
$ datumctl compute rollout undo api --to-revision=7
Creating revision #9 (copy of #7)...
Rollout started. Run `datumctl compute rollout api` to watch progress.
```

Undo creates a new revision rather than rewriting history — the audit trail stays append-only. The platform retains the 20 most recent revisions per workload; revisions beyond that are no longer available for undo.

### 4. Get logs

`datumctl compute logs` treats the workload as the target, not the individual instance. By default it returns logs across all instances and prefixes each line with the city and instance short name:

```
$ datumctl compute logs api --follow

Tailing logs for workload "api" in DFW, IAD. Ctrl-C to stop.

[DFW/api-dfw-0]  10:14:02  GET  /healthz       200   3ms
[IAD/api-iad-1]  10:14:02  GET  /v1/users       200  18ms
[DFW/api-dfw-1]  10:14:03  POST /v1/login       401   4ms
[IAD/api-iad-0]  10:14:03  GET  /healthz        200   2ms
```

Common filters reduce the output without requiring instance name lookup:

```
$ datumctl compute logs api --city=IAD --follow
$ datumctl compute logs api --since=15m
$ datumctl compute logs api -c worker --follow
```

All filters translate to label selectors against the platform's telemetry system. There is no per-city fan-out — the CLI queries a single endpoint and the label index handles scoping.

### 5. Inspect and debug instances

When something is wrong with a specific instance, `datumctl compute instances` gives a per-instance view across the whole project:

```
$ datumctl compute instances

  NAME          WORKLOAD  CITY  INTERNAL IP   TYPE            AGE   STATUS
  api-dfw-0     api       DFW   10.4.1.5      d1-standard-2   2d    Running
  api-dfw-1     api       DFW   10.4.1.6      d1-standard-2   2d    Running
  api-iad-0     api       IAD   10.5.1.7      d1-standard-2   2d    Running
  api-iad-1     api       IAD   10.5.1.8      d1-standard-2   2d    Running
  worker-dfw-0  worker    DFW   10.4.1.9      d1-standard-4   6h    Running

5 instances — 5 Running, 0 Pending, 0 Failed
```

Pass a workload name to narrow the view:

```
$ datumctl compute instances --workload=api
```

Instances that haven't started show why, inline:

```
  api-iad-2     api       IAD   —              —             d1-standard-2   30s   Pending (quota exceeded)
  api-iad-3     api       IAD   —              —             d1-standard-2   30s   Pending (network provisioning)
```

Drilling into a single instance gives the full picture with actionable context:

```
$ datumctl compute instances describe api-iad-2

Instance     api-iad-2
Workload     api / default / IAD
Type         d1-standard-2
Age          1m 12s

Status       Not running — quota exceeded
             Requested 4 CPU. 2 CPU available in IAD.

Runtime
  Image:     ghcr.io/acme/api:1.4.3
  Env:       DATABASE_URL (from secret), LOG_LEVEL=info
  Ports:     8080/tcp

Network      Waiting for addresses (not yet scheduled)

Next steps
  datumctl compute scale api --min=2
  datumctl compute quota
```

---

## Command reference

### Short-form commands (the everyday interface)

```
datumctl compute deploy             Deploy or update a workload
datumctl compute status             Show health across all cities
datumctl compute instances          List all instances (--workload, --city to filter)
datumctl compute logs               Stream logs (--workload, --city, --instance, -c/--container)
datumctl compute rollout            Watch a rollout in progress
datumctl compute rollout history    List recent revisions
datumctl compute rollout undo       Roll back to a previous revision
datumctl compute scale              Adjust replica counts
datumctl compute restart            Restart instances (rolling)
datumctl compute destroy            Delete a workload
datumctl compute quota              Show project quota usage
```

### Resource commands (for scripting and advanced use)

```
datumctl compute workloads [get | describe | delete | edit]
datumctl compute workloads rollout [status | history | undo]
datumctl compute workloads set image NAME CONTAINER=IMAGE

datumctl compute instances [get | describe | logs]

datumctl compute cities [list | describe]
datumctl compute instance-types [list | describe]
datumctl compute quota [--breakdown | --constrained | --city=CITY]
```

