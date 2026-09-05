# Skill: stalled transient state

Use when `actionability` is `stalled`, when a cause carries
`pattern: NoTerminalStateReported`, or when a reason classified `transient`
— `ProgrammingInProgress`, `Provisioning`, `PendingProgramming`,
`SchedulingGatesPresent`, `Starting`, `Stopping`, `Resolving`,
`AwaitingPropagation`, `NetworkProvisioning`, `InstancesProvisioning` — has held
far longer than it should.

## The one thing to know

**Stalled is an inference, not a report.** No controller said anything was
wrong; the reason still claims the work is in flight, and only the elapsed time
contradicts it. That makes it different from a platform fault, where the
controllers named a cause that is Datum's. Do not say "this is a Datum problem"
— say the state has outlived what it should take, and hand over the evidence.

The failure this exists to prevent is the opposite one: reporting a five-day
`ProgrammingInProgress` as "normal, wait a few minutes" because the reason said
transient.

## The second thing to know

**A reported state can itself be stale or wrong.** Compute shows you what the
controllers wrote to the project control plane, and a provider that fails to
propagate a result leaves a reason that has stopped describing reality.

On 2026-08-27 three workloads read `Programmed: Unknown` /
`ProgrammingInProgress`, "Instance is provisioning". On the cell, the container
had exited with code 1 and was restarting — `InstanceCrashing`, which is
**user-actionable**. The unikraft provider never propagated the crash upward.
Anyone reading the reason at face value told the customer "this isn't a spec
problem, escalate to Datum", which was confident, actionable, and wrong.

So the rule extends: the reason is a claim, the elapsed evidence outranks it,
and neither is licence to rule the workload spec out.

## Procedure

1. **Quantify it, and use the larger number.** `workload_diagnose` gives two
   ages on the root cause and they answer different questions:

   - `inStateFor` — how long the *condition* has held this reason.
   - `failingFor` — a floor on how long the *object* has been broken, taken
     from its `creationTimestamp` (`objectAge`) when that is longer.

   A crash rewrites `lastTransitionTime`, so `inStateFor` measures churn and
   systematically understates — worst in the crash-loop case that matters most.
   `failingFor` is the number to quote to the customer. `inStateFor` is still
   true; it just answers "when was this last touched", not "how long has this
   been broken".

   **A large gap between them is itself a finding**: nine hours of "in state" on
   an object failing for nine days means something is rewriting the condition
   without ever finishing. Say so.

   Then call `reason_explain` for `expectedWithin` — the window the reason is
   supposed to clear inside. "Nine days against an expected thirty minutes" is
   the whole finding.

   Two things the tools will not give you, on purpose. An age is omitted rather
   than guessed when the condition's timestamp cannot be believed — real
   Instances carry `lastTransitionTime: "1970-01-01T00:00:00Z"`, and a
   twenty-thousand-day age is a sentinel, not a stall. And `failingFor` never
   escalates `actionability` by itself: an object can be nine days old and have
   failed a minute ago.

2. **Check whether anything reported at all.** `reported` on the cause is false
   when the condition status is `Unknown`, meaning no controller has reported
   success *or* failure. That is materially different from `False`, which is a
   reported failure with a message you can quote.

   When an unreported condition sits on an object that has outlived its own
   window, the cause carries `pattern: NoTerminalStateReported`. Read it as:
   something is acting on this object and reporting nothing either way. It does
   **not** name a culprit — see step 5.

3. **Check whether it is one object or all of them.** `instances_list` for the
   workload. Every instance stuck in the same state points at the provider or
   controller for that cell; one stuck among healthy siblings points at that
   object. Say which — it decides who Datum pages.

4. **Look underneath before escalating.** Read `contributingConditions` from
   `workload_diagnose`. A stalled pointer reason (`InstancesProvisioning`,
   `PendingQuota`, `SchedulingGatesPresent`) often has a real cause below it
   that arrived after the stall began. If one is there, that is the answer —
   follow its skill instead.

5. **Route by which layer stopped moving — without ruling the spec out:**

   - `ProgrammingInProgress`, `PendingProgramming`, `Provisioning` — a provider
     took the job and reported nothing further. Do **not** say this cannot be a
     spec problem. From the project control plane, a provider that never reports
     and a user container that starts and exits immediately look identical, and
     the second is `InstanceCrashing` — user-actionable. Load
     `instance-not-ready` and rule out a crash loop *before* escalating, and
     escalate in parallel rather than instead.
   - `PendingEvaluation`, `PendingQuota` — quota. Load `quota-triage`; a
     sustained pending claim is a stuck quota backend.
   - `Resolving`, `AwaitingPropagation` — referenced data never reached the
     cell. Load `referenced-data-triage`.
   - `SchedulingGatesPresent` — the gating controller never removed the gate.

6. **Do not suggest a restart or a spec edit to shake it loose.** Deleting and
   recreating a workload destroys the evidence Datum needs and often reproduces
   the same stall. Offer it only if the customer asks and accepts that.

## Reporting

Lead with `failingFor` and the expectation, and give `inStateFor` beside it when
the two differ. Then: whether the workload is serving at all, whether every
replica or only one is affected, and whether any controller reported a cause.

Separate what is observed from what is inferred, and say which is which.
Observed: the object's age, the condition status, the message, the timestamps.
Inferred: that it is stuck, and who might be at fault. Do not promote the second
into the first — "nine days with nothing reported" is a finding a human can act
on; "the provider is broken" is a guess that has already been wrong.

Hand over the object name, the condition type, the reason, the
`lastTransitionTime`, and the condition message verbatim.
