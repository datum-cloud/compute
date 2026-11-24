# datum-compute

A Claude Code plugin for Datum Cloud Compute. It lets Claude deploy a container
to Datum Cloud and put it on a public URL, operate the workloads you already
run, and work out why one is not running and who has to fix it.

## Install

```bash
claude plugin marketplace add datum-cloud/claude-code-plugins
claude plugin install datum-compute@datum-claude-code-plugins
```

Installing it also installs the `datum-cloud` plugin, which runs datum-mcp,
Datum's MCP server; this plugin's skills load datum-mcp's compute tools when
they need them. You also need two tools on your `PATH`:

- **datum-mcp**: `brew install datum-cloud/tap/datum-mcp`, or see the
  [datum-mcp install guide](https://github.com/datum-cloud/datum-mcp#install-the-binary).
  It signs you in through your browser the first time a tool needs it.
- **datumctl** with its compute plugin, for building, deploying and reading
  logs: `datumctl plugin install compute`.

## Skills

| Skill | Use it to |
|---|---|
| `deploy-container` | Build an image from a Dockerfile, deploy it, and serve it on a public HTTPS URL |
| `manage-workload` | Read status and logs, scale, restart, update or delete a workload |
| `workload-create` | Create a workload from a manifest, for anything the deploy flags cannot express |
| `workload-not-available` | Find out why a workload is not running, and who fixes it |
| `instance-not-ready` | Triage image pull failures, crash loops and rejected configuration |
| `quota-triage` | Tell being over quota apart from having no quota at all |
| `referenced-data-triage` | Fix missing, unreadable or oversized ConfigMaps and Secrets |
| `placement-triage` | Explain why a workload cannot be placed in a location |
| `stalled-transient` | Handle a workload that has been "in progress" for far too long |
| `instance-command-check` | Suggest one safe command to run inside an instance |

`deploy-container` and `manage-workload` are written for Claude Code. The
others are the procedures Datum's in-product assistant follows, published from
[`docs/agent/skills`](../docs/agent/skills) in this repository: each plugin
skill loads its procedure through datum-mcp's `compute_guide`, so the text is
always the one that matches your datum-mcp's tools. A test fails when a skill is
published there without an entry here.

Custom domains, DNS and load balancer problems belong to the `datum-networking`
plugin.
