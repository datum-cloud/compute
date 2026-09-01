# Skill: quota triage

Use when a workload is blocked on quota — `QuotaNotGranted` at the top, or any
`QuotaGranted=False` condition on an instance.

## Procedure

1. **Get the real reason.** `QuotaNotGranted` on the Workload or
   WorkloadDeployment is a pointer. Call `workload_diagnose`, or read the
   Instance's `QuotaGranted` condition via `instances_list`. Never report
   `QuotaNotGranted` as the cause.

2. **Separate the four cases.** They look alike and lead to opposite advice:

   | Reason | Meaning | Who acts |
   |---|---|---|
   | `QuotaExceeded` | Asked for more than the allowance permits; explicitly denied | Customer |
   | `QuotaNoBudget` | No AllowanceBucket configured for the project at all | Datum |
   | `PendingEvaluation` | Claim not created yet, or first evaluation in flight | Nobody — wait |
   | `QuotaBackendUnavailable` | Quota backend unreachable (network/TLS/401/503) | Datum |

   `QuotaMisconfigured`, `QuotaProjectNotFound`, `QuotaNamespaceNotFound`, and
   `QuotaProjectIDUnresolvable` are all platform faults too.

3. **For `QuotaExceeded`, quantify it.** The condition message carries the
   requested amount and the remaining allowance. Quote both. Then give the
   customer the three real options: reduce replica count, reduce per-instance
   resources, or request a quota increase.

4. **For `PendingEvaluation`**, check how long. Minutes is normal. Sustained
   `PendingEvaluation` is a stuck quota backend — treat it as
   `QuotaBackendUnavailable` and escalate.

5. **Check the split.** `instances_list` shows how many instances got quota and
   how many did not. Partial grants are the common case: the workload is serving
   at reduced capacity, which is worth saying explicitly.

## Do not

Do not suggest spec changes for `QuotaNoBudget` or any `Quota*` platform fault —
the project cannot provision its own allowance, and the customer will burn time
trying.
