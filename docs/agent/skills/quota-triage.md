# Skill: quota triage

Use when a workload is blocked on its project's quota — `QuotaNotGranted` at
the top, or any `QuotaGranted=False` condition on an instance.

Say **quota**, or how much CPU and memory the project is allowed to use. The
customer already sees the word in `QuotaExceeded`, so it is worth keeping and
worth glossing once. What they cannot read is everything around it: not the
service that evaluates it, and not the request compute files against it.

## Procedure

1. **Get the real reason.** `QuotaNotGranted` on the Workload or
   WorkloadDeployment is a pointer. Call `workload_diagnose`, or read the
   Instance's `QuotaGranted` condition via `instances_list`. Never report
   `QuotaNotGranted` as the cause.

2. **Separate the four cases.** They look alike and lead to opposite advice:

   | Reason | What actually happened | Who acts |
   |---|---|---|
   | `QuotaExceeded` | Asked for more than the quota allows; turned down | Customer |
   | `QuotaNoBudget` | The project has no compute quota set up at all | Datum |
   | `PendingEvaluation` | The check has not finished yet | Nobody — wait |
   | `QuotaBackendUnavailable` | Datum could not reach the service that checks quota | Datum |

   `QuotaMisconfigured`, `QuotaProjectNotFound`, `QuotaNamespaceNotFound`, and
   `QuotaProjectIDUnresolvable` are all Datum's to fix too.

   The first two are the pair that matters. Being over quota and having no
   quota at all read the same to a customer and lead to opposite advice: one
   they fix by asking for less, the other they cannot fix at all.

3. **For `QuotaExceeded`, quantify it.** The status message carries the amount
   requested and the amount left. Quote both. Then give the customer the three
   real options: fewer replicas, less CPU or memory per instance, or ask Datum
   to raise the project's quota.

4. **For `PendingEvaluation`**, check how long. Minutes is normal. If it stays
   there, the checking service itself is stuck — treat it as
   `QuotaBackendUnavailable` and hand it to Datum.

5. **Check the split.** `instances_list` shows how many instances were cleared
   and how many were not. Partial is the common case: the workload is serving at
   reduced capacity, which is worth saying explicitly.

## Do not

Do not suggest workload changes for `QuotaNoBudget` or any other `Quota*` fault
that is Datum's — a project cannot set up its own quota, and the customer will
burn time trying.
