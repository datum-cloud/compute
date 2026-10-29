---
name: instance-not-ready
description: "Triage Datum Compute instances that never become Ready: ImageUnavailable, InstanceCrashing, ConfigurationError, or an instance stuck provisioning. Use when a workload's instances exist but are failing, crash-looping or can't pull their image."
---

This skill is compute's own procedure, the one Datum's in-product assistant
follows. datum-mcp serves it, so the text always matches the tools you have.

1. If `compute_guide` is not among your tools, load compute's tools: call
   datum-mcp's `toolsets` with `action: "load"` and `names: ["compute"]`.
2. If you have not already in this conversation, call `compute_guide` with no
   skill. It explains how to read compute's conditions, which the procedure
   assumes you know.
3. Call `compute_guide` with `skill: "instance-not-ready"` and follow
   the procedure it returns. Its preamble maps the assistant's tool names onto
   datum-mcp's.

In Claude Code:

- The `compute_*` tools read the active project. If one says no project is
  set, call `context` and follow its `next_step`.
- If `toolsets` is missing too, datum-mcp is not running: ask the person to
  install it with `brew install datum-cloud/tap/datum-mcp` and restart Claude
  Code.
- You are at the person's terminal. Read-only `datumctl compute` commands
  (`workloads`, `instances`, `logs`, `quota`) are yours to run. Show any command
  that changes the project, such as `deploy`, `scale`, `restart` or `destroy`,
  and run it only after the person agrees to that exact command.

The instance logs this procedure points to are
`datumctl compute logs <workload>`; add `--instance <name> --previous` for an
instance that was replaced or restarted.
