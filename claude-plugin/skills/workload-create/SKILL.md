---
name: workload-create
description: "Create a Datum Compute workload from a manifest: check prerequisites, choose runtime class, locations and instance type, render, dry-run, confirm, then create. Use when someone wants to deploy or run something new on Datum Cloud Compute and needs more than the deploy-container walkthrough covers."
---

This skill is compute's own procedure, the one Datum's in-product assistant
follows. datum-mcp serves it, so the text always matches the tools you have.

1. If `compute_guide` is not among your tools, load compute's tools: call
   datum-mcp's `toolsets` with `action: "load"` and `names: ["compute"]`.
2. If you have not already in this conversation, call `compute_guide` with no
   skill. It explains how to read compute's conditions, which the procedure
   assumes you know.
3. Call `compute_guide` with `skill: "workload-create"` and follow
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

You are at a shell, so the section "If the user has a shell" applies. For a
single container image, with or without a public URL, the `deploy-container`
skill is the shorter path. Where the text says you cannot build or push an
image, that is true of the in-product assistant, not of you: with the person's
agreement you can run `datumctl compute build`.
