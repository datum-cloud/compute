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

## When the logs cannot be reached

Step 3 ends at the instance logs, and it is the only cheap way to settle
whether a silent stall is really a crash loop. On some workloads that
route is closed: retrieval fails outright rather than coming back empty, because
the machine answers plain HTTP on the port where encrypted log traffic is
expected. No logs come back, for anyone, however often it is retried.

When that happens:

1. **Say so, and do not send them anyway.** "Check the container logs" is not a
   next step if the logs cannot be fetched. Say retrieval is failing and give
   them what you do have: the restart count, the exit code if the status carries
   one, how long the instance has been failing, and whether its siblings fail
   the same way.

2. **Do not let it settle the answer.** A crash loop you could not look for is
   not a crash loop ruled out. Do not turn "I could not check" into "the
   container is fine", and do not hand this to Datum as a stall with the crash
   loop eliminated. Name the check you were unable to run.

3. **File `UnactionableGuidance`.** The gap is not the broken container — that
   is the customer's, and it is working as designed that compute reported it.
   The gap is that the next step this procedure names cannot be carried out on
   these workloads at all, so every crash answer stops one step short of the
   cause. Quote the failure, not your conclusion:

       "capability": "container log retrieval for a crashing instance",
       "kind": "UnactionableGuidance",
       "evidence": {
         "tool": "instances_list",
         "observed": "InstanceCrashing; remediation points at the logs",
         "contradictedBy": "log retrieval fails outright on this instance:
                            the port answers plain HTTP where encrypted
                            traffic is expected" }

File it once, for the blocked step. A container that keeps exiting because of a
bug in the customer's program is not a gap however many times it happens.

## Reporting

For `ImageUnavailable` and `ConfigurationError`, name the exact thing to change.
For `InstanceCrashing`, do not guess the bug — send them to the logs, and quote
the exit code and restart count. If the logs cannot be fetched, say that instead
of pointing at them, and file the gap.

When you got here from a stalled instance rather than a reported reason, say
which of the two you are answering: the logs showed a crash (the customer's to
fix, and Datum also failed to report it), or they did not (hand it to Datum,
with the crash loop ruled out). Do not say "this isn't a problem with your
workload" without having looked.
