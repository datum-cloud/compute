---
name: manage-workload
description: "Operate a running Datum Compute workload with datumctl compute: read its status and logs, scale it, restart it, watch a rollout, change its image or environment, and delete it. Use when someone wants to check on, debug, resize, restart, update or remove a workload that already exists on Datum Cloud."
---

# Operate a workload

Everything here runs at the person's terminal with `datumctl compute`. Pass
`--project <name>` when the person has more than one project, so a command
never lands in the wrong one.

Read commands are yours to run. Every command that changes the workload
changes what is serving the person's traffic, so show the exact command and
run it only after they agree to it.

## Read: yours to run

| Question | Command |
|---|---|
| What is running, and is it healthy? | `datumctl compute workloads` (`--health=degraded` for the unhealthy ones) |
| One workload's config, health per location, and URL | `datumctl compute workloads describe <name>` |
| Its instances | `datumctl compute instances --workload=<name>` |
| One instance in full | `datumctl compute instances describe <instance>` |
| What the app printed | `datumctl compute logs <name> --since=1h` |
| What the URL served | `datumctl compute logs <name> --alb --since=15m` |
| Quota left | `datumctl compute quota` |

Logs are merged across instances, oldest first. Narrow them with
`--instance`, `--location` and `--search`; `--previous` shows an instance's
output from before it was last replaced, which is where a crash loop's error
usually is. Use `-o json` when you need to filter the output yourself. Do not
use `--follow` from a non-interactive shell: it never returns.

When the answer is "it is not working", do not reason from these tables alone.
Call `compute_workload_diagnose` with the workload name (load the `compute`
toolset with datum-mcp's `toolsets` first if it is missing) and follow the skill it
names; the top-level status is usually a pointer, not the cause.

## Change: show first, run after a yes

| Change | Command | What it does |
|---|---|---|
| Fixed size | `datumctl compute scale <name> --min=3` | Instances per location. |
| Autoscale | `datumctl compute scale <name> --max=10 --cpu-percent=70` | `--max=0` turns autoscaling off. |
| Restart | `datumctl compute restart <name>` | A rolling restart; `--location` limits it to one location. Needed after a ConfigMap or Secret changes, since a running process keeps what it read at startup. |
| New image | `datumctl compute deploy <name> --image=<ref> --city=<same> --yes` | See redeploys, below. |
| Environment | `datumctl compute deploy <name> --image=<same> --city=<same> --env=KEY=VALUE --yes` | `--env=KEY-` removes one. The same `NAME-` form removes an import or a mount. |
| Watch a rollout | `datumctl compute rollout <name>` | Only in a terminal the person is watching; it runs until interrupted. Prefer `workloads describe` to check progress yourself. |

Before scaling up, check that the new size fits: instances times instance size,
against `datumctl compute quota`. A scale past quota is accepted, and the extra
instances then sit at `QuotaExceeded`.

A redeploy is `deploy` again with the same name. It needs `--image` and the
locations every time (`--location`, `--city` or `--location-selector`, as the
workload has them), and `--instance-type` whenever the workload uses one other
than the default, which a redeploy otherwise resets. Read all three from
`datumctl compute workloads describe <name> -o yaml` first and repeat them.
Environment variables, imports, mounts, labels and scale settings it does not
name carry forward.

On any redeploy, leave `--http-port`, `--http2` and `--no-http` off unless the
person asked to change the URL. Adding one to a routine redeploy can put a
second URL in front of a workload that is already served.

Setting or removing a `--label` replaces every instance. Say so before running
it.

## Delete: confirm the name

```bash
datumctl compute destroy <name> --yes
```

This deletes the workload, all its instances and its Datum-managed URLs, and
cannot be undone. Say all three, name the workload exactly, and run it only
after the person has confirmed that name. Custom domains are kept, because a
verified domain belongs to the project rather than the workload.

## Reporting

Lead with the effect on the person's traffic: "api now runs 3 instances in each
of DFW and IAD", "the restart is rolling; 2 of 4 instances are back". Then the
workload name and anything you would check next.
