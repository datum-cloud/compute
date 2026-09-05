# Skill: instance not ready

Use when instances exist but are not becoming Ready — `ImageUnavailable`,
`InstanceCrashing`, `ConfigurationError`, or a stuck `Provisioning` — and
whenever a stalled instance carries `pattern: NoTerminalStateReported`.

## A crash may never reach the reason

These four reasons are only visible if the provider propagates them. It does not
always. In the 2026-08-27 staging case the pods on the cell had exited with code
1 and were restarting, while the project control plane still read
`Programmed: Unknown` / `ProgrammingInProgress`, "Instance is provisioning".

So `InstanceCrashing` being absent is not evidence that the container is fine.
An old instance that has never become Ready and reports nothing terminal is a
crash-loop candidate, and step 3 is the cheapest way to settle it — the logs
are on the cell either way.

## Procedure

1. **Identify which of the four it is** from the Instance's `Ready` (and
   `Programmed`) condition. All four are reported by the infrastructure
   provider, not by compute itself.

2. **`ImageUnavailable`** — the provider could not pull the image. Cause is one
   of: the tag does not exist, the registry path is wrong, the registry is
   unreachable, or pull credentials for a private registry are missing. The
   condition message usually names which (`manifest unknown` = bad tag or path;
   `unauthorized` = credentials). Check the image reference in the workload spec
   first — it is the most common cause by a wide margin.

3. **`InstanceCrashing`** — the process started and keeps exiting. The platform
   delivered the workload correctly; the application is failing. Point at the
   instance logs and the exit code in the condition message. Common causes: a
   failing entrypoint, a missing environment variable or mount, an unreachable
   dependency at startup. Note the restart count — a high count with a short
   uptime means it is failing immediately, which usually means configuration
   rather than load.

4. **`ConfigurationError`** — the runtime refused the configuration before the
   process ever started (invalid env injection, missing device). This is a spec
   error, not an application bug. Distinguish it from `InstanceCrashing`: here
   the process never ran at all.

5. **`Provisioning`** — transient. The runtime is creating the container or
   unpacking the image. Say to wait. Only if it persists well beyond a few
   minutes should you treat it as a provider problem.

6. **Check whether every instance fails the same way.** `instances_list` for the
   workload: uniform failure points at the spec or image; a single failing
   instance among healthy ones points at one bad cell or host, which is a
   platform matter.

## Reporting

For `ImageUnavailable` and `ConfigurationError`, name the exact field to change.
For `InstanceCrashing`, do not guess the bug — direct them to the logs and quote
the exit code and restart count.

When you got here from a stalled instance rather than a reported reason, say
which of the two you are answering: the logs showed a crash (user-actionable,
and the provider also failed to report it), or they did not (escalate, with the
crash loop ruled out). Do not report "not a spec problem" without having
looked.
