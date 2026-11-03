---
name: instance-command-check
description: "Suggest one safe, read-only command for a person to run inside a Datum Compute instance with datumctl compute exec, when status and logs leave a specific question. Use before proposing to exec into an instance."
---

This skill is compute's own procedure, the one Datum's in-product assistant
follows. datum-mcp serves it, so the text always matches the tools you have.

1. If `compute_guide` is not among your tools, load compute's tools: call
   datum-mcp's `toolsets` with `action: "load"` and `names: ["compute"]`.
2. If you have not already in this conversation, call `compute_guide` with no
   skill. It explains how to read compute's conditions, which the procedure
   assumes you know.
3. Call `compute_guide` with `skill: "instance-command-check"` and follow
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

Some datum-mcp releases have neither this skill nor `compute_exec_suggest`.
If `compute_guide` reports an unknown skill, or the tool is missing, keep to
the same rules: say what the command will check and why it matters, pick one
running instance from `compute_instances_list`, and choose one short read-only
command that prints no environment variables, secrets or tokens and changes
nothing. Show it as
`datumctl compute exec <instance> [-c <container>] -- <command> [args...]`,
say it has not been run, and let the person run it and share what matters.
