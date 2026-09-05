# Skill: workload not available

Use when someone asks why a Workload is not running, not available, or stuck.

## Procedure

1. **Diagnose before you read.** Call `workload_diagnose` with the workload
   name. It walks Workload -> WorkloadDeployment -> Instance and returns the
   leaf cause. Do not assemble the tree by hand first — the top-level reason is
   usually a pointer, not a cause.

2. **Read `rootCause.actionability` before anything else.** It decides what you
   tell the customer:
   - `user` — name the exact thing to change: the image, the replica count, the
     missing ConfigMap or Secret, the network name.
   - `platform` — say plainly that this one is Datum's to fix and that no
     change to their workload will clear it. Do not offer workarounds.
   - `transient` — say what it is waiting on and roughly how long is normal.
   - `stalled` — it is taking far longer than it should and nobody has said
     why. Load `stalled-transient`; do not answer it from here.

3. **Check the blast radius.** `instances.ready` vs `instances.total` tells you
   whether this is total failure or partial degradation. A workload with 2/6
   ready is serving — say so; the customer's site may not be down.

4. **If several instances fail for different reasons**, `instances.blocked`
   lists each one's own reason. Fix the most common cause first, then re-check;
   a second cause often disappears with the first.

5. **Follow the suggested skill.** `suggestedSkill` names the runbook for the
   specific area — load it rather than improvising.

6. **If `rootCause` is null** but replicas are missing, nothing has written a
   status yet. Say the workload was just created, or that Datum is behind, and
   suggest re-checking shortly.

## Reporting

Say, in this order: whether it is serving, what is actually wrong in the
customer's own terms, who has to act, and the single next step. Then the
identifiers — the object name, the image tag, the missing ConfigMap — quoted
from the status message verbatim, because those are what they need if they have
to escalate.

Do not open with a verdict the body then walks back. If the diagnosis says
`stalled`, or carries `pattern: NoTerminalStateReported`, then who is at fault
is not yet established: say what is known first, and reach a verdict only if the
evidence gets you one.
