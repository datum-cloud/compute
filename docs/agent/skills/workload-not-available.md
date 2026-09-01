# Skill: workload not available

Use when someone asks why a Workload is not running, not available, or stuck.

## Procedure

1. **Diagnose before you read.** Call `workload_diagnose` with the workload
   name. It walks Workload -> WorkloadDeployment -> Instance and returns the
   leaf cause. Do not assemble the tree by hand first — the top-level reason is
   usually a pointer, not a cause.

2. **Read `rootCause.actionability` before anything else.** It decides what you
   tell the customer:
   - `user` — name the exact field or object to change.
   - `platform` — say plainly that this is Datum's to fix, and that no workload
     change will help. Do not offer spec edits.
   - `transient` — say what it is waiting on and roughly how long is normal.

3. **Check the blast radius.** `instances.ready` vs `instances.total` tells you
   whether this is total failure or partial degradation. A workload with 2/6
   ready is serving — say so; the customer's page may not be down.

4. **If several instances fail for different reasons**, `instances.blocked`
   lists each one's own reason. Fix the most common cause first, then re-check;
   a second cause often disappears with the first.

5. **Follow the suggested skill.** `suggestedSkill` names the runbook for the
   specific subsystem — load it rather than improvising.

6. **If `rootCause` is null** but replicas are missing, the controllers have not
   yet written status. Say the workload was just created or the controller is
   behind, and suggest re-checking shortly.

## Reporting

State, in this order: whether it is serving, the leaf cause in the customer's
terms, who has to act, and the single next step. Quote the condition message
verbatim when it names an object (an image tag, a ConfigMap) — it is the most
actionable line available.
