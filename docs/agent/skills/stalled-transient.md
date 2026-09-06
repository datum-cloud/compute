# Skill: stalled transient state

Use when `actionability` is `stalled`, when a cause carries
`pattern: NoTerminalStateReported`, or when a reason classified `transient`
— `ProgrammingInProgress`, `Provisioning`, `PendingProgramming`,
`SchedulingGatesPresent`, `Starting`, `Stopping`, `Resolving`,
`AwaitingPropagation`, `NetworkProvisioning`, `InstancesProvisioning` — has held
far longer than it should.

## The one thing to know

**Stalled is a conclusion drawn from the clock, not something anyone reported.**
Nothing said anything was wrong; the status still claims the work is under way,
and only the elapsed time contradicts it. That makes it different from a fault
Datum reported as theirs. Do not say "this is Datum's problem" — say it has
taken far longer than it should and hand over the evidence.

The failure this exists to prevent is the opposite one: reporting a five-day
`ProgrammingInProgress` as "normal, wait a few minutes" because the reason said
transient.

## The second thing to know

**A reported state can itself be stale or wrong.** What compute shows you is
what was last written into the customer's project, and a failure that never gets
passed upward leaves a status that has stopped describing reality.

On 2026-08-27 three workloads read `Programmed: Unknown` /
`ProgrammingInProgress`, "Instance is provisioning". Down at the machine, the
container had exited with code 1 and was restarting — `InstanceCrashing`, which
is **the customer's to fix**. The crash was never passed upward. Anyone reading
the status at face value told the customer "this isn't a problem with your
workload, escalate to Datum", which was confident, actionable, and wrong.

So the rule extends: the reason is a claim, the elapsed evidence outranks it,
and neither is licence to rule the customer's own workload out.

## Procedure

1. **Quantify it, and use the larger number.** `workload_diagnose` gives two
   ages on the root cause and they answer different questions:

   - `inStateFor` — how long the *status* has said this.
   - `failingFor` — a floor on how long the *object* has been broken, taken
     from its `creationTimestamp` (`objectAge`) when that is longer.

   A crash rewrites `lastTransitionTime`, so `inStateFor` measures churn and
   systematically understates — worst in the crash-loop case that matters most.
   `failingFor` is the number to quote to the customer. `inStateFor` is still
   true; it just answers "when was this last touched", not "how long has this
   been broken".

   **A large gap between them is itself a finding**: nine hours of "in state" on
   an object broken for nine days means something is rewriting the status
   without ever finishing. Say so.

   Then call `reason_explain` for `expectedWithin` — how long this step should
   take. "Nine days, against thirty minutes" is the whole finding.

   Two things the tools will not give you, on purpose. An age is omitted rather
   than guessed when a timestamp cannot be believed — real Instances carry
   `lastTransitionTime: "1970-01-01T00:00:00Z"`, and a twenty-thousand-day age
   is a placeholder, not a stall. And `failingFor` never escalates
   `actionability` by itself: an object can be nine days old and have broken a
   minute ago.

2. **Check whether anything was reported at all.** `reported` on the cause is
   false when the condition status is `Unknown`, meaning nothing has reported
   success *or* failure. That is materially different from `False`, which is a
   reported failure with a message you can quote. In plain terms: "Datum hasn't
   reported back on this either way" versus "Datum told us it failed, and here
   is what it said".

   When nothing has been reported about an object that has outlived its own
   window, the cause carries `pattern: NoTerminalStateReported`. Read it as:
   something is working on this and never saying how it turned out. It does
   **not** name a culprit — see step 5.

3. **Check whether it is one object or all of them.** `instances_list` for the
   workload. Every instance stuck the same way points at the place they all
   run; one stuck among healthy siblings points at that object. Say which — it
   decides who Datum wakes up.

4. **Look underneath before escalating.** Read `contributingConditions` from
   `workload_diagnose`. A stalled pointer reason (`InstancesProvisioning`,
   `PendingQuota`, `SchedulingGatesPresent`) often has a real cause below it
   that arrived after the stall began. If one is there, that is the answer —
   follow its skill instead.

5. **Route by what stopped moving — without ruling the workload out:**

   - `ProgrammingInProgress`, `PendingProgramming`, `Provisioning` — something
     took the job and reported nothing further. Do **not** say this cannot be a
     problem with the workload. From where the customer sits, infrastructure
     that never reports back and a container that starts and exits immediately
     look identical, and the second is `InstanceCrashing` — theirs to fix. Load
     `instance-not-ready` and rule out a crash loop *before* escalating, and
     escalate alongside it rather than instead of it.
   - `PendingEvaluation`, `PendingQuota` — the quota check. Load
     `quota-triage`; a check that never finishes is the checking service stuck.
   - `Resolving`, `AwaitingPropagation` — a ConfigMap or Secret never arrived.
     Load `referenced-data-triage`.
   - `SchedulingGatesPresent` — a scheduling gate was never cleared.

6. **Do not suggest a restart or an edit to shake it loose.** Deleting and
   recreating a workload destroys the evidence Datum needs and often reproduces
   the same stall. Offer it only if the customer asks and accepts that.

## Reporting

Lead with `failingFor` and how long it should have taken, and give `inStateFor`
beside it when the two differ. Then: whether the workload is serving at all,
whether every replica or only one is affected, and whether anything reported a
cause.

Keep what is known apart from what it suggests, and say which is which. Known:
how old the object is, what the status says, the message, the timestamps.
Suggested and not established: that it is stuck, and who is at fault. Do not
promote the second into the first — "nine days with nothing reported" is a
finding a person can act on; "Datum's infrastructure is broken" is a guess that
has already been wrong once.

Watch the first sentence in particular. "None of these is anything you can fix"
is a verdict, and if the body then walks it back to "probably not yours, but not
provably not yours", the opening was wrong. When nothing has been reported,
neither side is ruled out yet: open with what is known and how long it has been
going on, and let the verdict wait for evidence that supports one.

Hand over the object name, the condition type, the reason, the
`lastTransitionTime`, and the status message verbatim.

## When the classification itself was wrong

This skill exists because of one answer. A nine-day stall came back classified
`transient` with a remediation of "Wait.", and was read out to the customer as a
normal in-flight state that would clear in a few minutes. That reading was a
fair reading of what was handed over. It was also wrong, and nothing in the
conversation announced it — a person found the truth afterwards by looking at
the object itself.

So do not wait to notice that an answer was wrong; you usually will not. Notice
instead the moment you take one back:

**If you told this customer earlier in the conversation that something was
normal, in progress, or worth waiting a few minutes for, and a later tool result
shows it has been in that state for hours or days, that reversal is the signal.**
File `MisleadingOutput` against the tool whose output you believed. You are not
reporting that the workload is broken — that goes to the customer. You are
reporting that reading the output correctly produced the wrong answer, which is
the only failure nobody else will catch.

Two other tells for the same thing:

- **Two numbers in one response that cannot both be true.** `actionability:
  transient`, or a remediation of "Wait.", sitting beside a `failingFor` of `9d`
  and an `expectedWithin` of `30m`. Here you have not said anything yet, so
  there is nothing to retract — file it anyway and answer from the clock.
- **The customer pushing back**: "it has been days", "are you sure", "that is
  not right". Re-read the durations before defending the earlier answer, and
  file if they turn out to be right. Their words are not evidence and must not
  be quoted into the report; the two clocks are.

Evidence for this one is the pair of clocks and the classification they defeat,
copied out of the tool result:

    "capability": "duration-aware classification of transient reasons",
    "kind": "MisleadingOutput",
    "evidence": {
      "tool": "workload_diagnose",
      "observed": "actionability: transient, remediation \"Wait.\"",
      "contradictedBy": "failingFor: 9d, inStateFor: 9h30m, expectedWithin: 30m" }

Do not file when the clock and the classification agree. A two-minute
`ProgrammingInProgress` called transient is the tool being right, and a stall
the tools already returned as `stalled` is the tools working as intended — the
answer there is this procedure, not a report.
