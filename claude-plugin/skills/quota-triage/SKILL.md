---
name: quota-triage
description: "Explain a Datum Compute workload blocked on project quota: QuotaNotGranted, QuotaExceeded, QuotaNoBudget or a quota check that never finished. Use when a workload won't start because of CPU or memory limits."
---

This skill is compute's own procedure, the one Datum's in-product assistant
follows. datum-mcp serves it, so the text always matches the tools you have.

1. If `compute_guide` is not among your tools, load compute's tools: call
   datum-mcp's `toolsets` with `action: "load"` and `names: ["compute"]`.
2. If you have not already in this conversation, call `compute_guide` with no
   skill. It explains how to read compute's conditions, which the procedure
   assumes you know.
3. Call `compute_guide` with `skill: "quota-triage"` and follow
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

`datumctl compute quota` shows what the project is allowed and has used;
`--constrained` lists only what is at its limit.
