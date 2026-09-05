# Skill: instance not ready

Use when instances exist but are not becoming Ready — `ImageUnavailable`,
`InstanceCrashing`, `ConfigurationError`, or a stuck `Provisioning` — and
whenever a stalled instance carries `pattern: NoTerminalStateReported`.

## A crash may never reach the reason

These four reasons only appear if the failure is passed upward. It is not
always. In the 2026-08-27 staging case the containers had exited with code 1 and
were restarting, while the customer's project still read `Programmed: Unknown` /
`ProgrammingInProgress`, "Instance is provisioning".

So `InstanceCrashing` being absent is not evidence that the container is fine.
An old instance that has never become Ready, with nothing reported about it
either way, is a crash-loop candidate, and step 3 is the cheapest way to settle
it — the logs are there either way.

## Procedure

1. **Identify which of the four it is** from the Instance's `Ready` (and
   `Programmed`) condition. All four are reported by the infrastructure that
   runs the container, not by compute itself.

2. **`ImageUnavailable`** — the image could not be downloaded. It is one of:
   the tag does not exist, the repository path is wrong, the registry was
   unreachable, or credentials for a private registry are missing. The status
   message usually says which (`manifest unknown` = bad tag or path;
   `unauthorized` = credentials). Check the image on the workload first — it is
   the most common cause by a wide margin.

3. **`InstanceCrashing`** — the container started and keeps exiting. Datum
   delivered and started it correctly; the program inside is failing. Point at
   the instance logs and the exit code in the status message. Common causes: a
   start command that fails, a missing environment variable or mounted file, or
   something it needs at startup that it cannot reach. Note the restart count —
   a high count with a short uptime means it is failing immediately, which
   usually means configuration rather than load.

4. **`ConfigurationError`** — the machine refused the configuration before the
   program ever ran (an environment variable it could not set, a device that is
   not there). This is something in the workload, not a bug in the application.
   Distinguish it from `InstanceCrashing`: here the program never ran at all.

5. **`Provisioning`** — normal. The container is being created and the image
   unpacked. Say to wait. Only if it persists well beyond a few minutes should
   you treat it as Datum's problem.

6. **Check whether every instance fails the same way.** `instances_list` for the
   workload: all of them failing the same way points at the workload or the
   image; one failing among healthy siblings points at one machine or one
   location, which is Datum's.

## Reporting

For `ImageUnavailable` and `ConfigurationError`, name the exact thing to change.
For `InstanceCrashing`, do not guess the bug — send them to the logs, and quote
the exit code and restart count.

When you got here from a stalled instance rather than a reported reason, say
which of the two you are answering: the logs showed a crash (the customer's to
fix, and Datum also failed to report it), or they did not (hand it to Datum,
with the crash loop ruled out). Do not say "this isn't a problem with your
workload" without having looked.
