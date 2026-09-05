# Skill: stalled transient state

Use when `actionability` is `stalled`, or when a reason classified `transient`
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

## Procedure

1. **Quantify it.** `workloads_list` gives `rootCauseFor` ("5d") and
   `rootCauseSince`; `workload_diagnose` gives `inStateFor` on the root cause.
   Call `reason_explain` for `expectedWithin` — the window the reason is
   supposed to clear inside. Quote both numbers. "Five days against an expected
   thirty minutes" is the whole finding.

2. **Check whether it is one object or all of them.** `instances_list` for the
   workload. Every instance stuck in the same state points at the provider or
   controller for that cell; one stuck among healthy siblings points at that
   object. Say which — it decides who Datum pages.

3. **Look underneath before escalating.** Read `contributingConditions` from
   `workload_diagnose`. A stalled pointer reason (`InstancesProvisioning`,
   `PendingQuota`, `SchedulingGatesPresent`) often has a real cause below it
   that arrived after the stall began. If one is there, that is the answer —
   follow its skill instead.

4. **Route by which layer stopped moving:**

   - `ProgrammingInProgress`, `PendingProgramming`, `Provisioning` — an
     infrastructure provider took the job and stopped. Nothing in the workload
     spec will move it.
   - `PendingEvaluation`, `PendingQuota` — quota. Load `quota-triage`; a
     sustained pending claim is a stuck quota backend.
   - `Resolving`, `AwaitingPropagation` — referenced data never reached the
     cell. Load `referenced-data-triage`.
   - `SchedulingGatesPresent` — the gating controller never removed the gate.

5. **Do not suggest a restart or a spec edit to shake it loose.** Deleting and
   recreating a workload destroys the evidence Datum needs and often reproduces
   the same stall. Offer it only if the customer asks and accepts that.

## Reporting

Lead with the duration and the expectation. Then: whether the workload is
serving at all, whether every replica or only one is affected, and that the
cause has not been reported by any controller — which is why it needs a human.
Hand over the object name, the condition type, the reason, the
`lastTransitionTime`, and the condition message verbatim.
