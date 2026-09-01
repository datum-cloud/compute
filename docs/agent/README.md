# Agent capabilities

What compute publishes to an AI assistant: the knowledge it reads, the skills it
may follow, and (via `internal/agent`) the diagnosis it can run.

This is the provider side of the Datum AI Agent Framework. A service registers
agent capabilities alongside its catalog registration, entitlement decides which
projects receive them, and the assistant composes a conversation from exactly
the services a project is entitled to. Compute owns what appears here; the
assistant owns the document schema that carries it.

## Contents

| Path | Role |
|---|---|
| `llms-full.txt` | Knowledge. The compute resource model and, critically, how to read its conditions. Fetched over HTTP and appended to the system prompt. |
| `skills/*.md` | Skills. Reviewed, step-by-step triage procedures, loaded on demand. |
| `../../internal/agent` | The reason catalog and the diagnosis walk that back the tools. |

## Status

Landed here: the reason catalog, the diagnosis walk, and the knowledge and
skills above. **The MCP server that serves these as tools is not yet in this
repo** — `llms-full.txt` describes the tool surface (`workloads_list`,
`workload_diagnose`, `reason_explain`, ...) that a compute MCP server will
expose, and a prototype of it runs in the assistant repo's playground today.
`internal/agent` is the logic those tools call; wiring it to a served endpoint,
with per-project scoping of reads, is the remaining step.

## Why the knowledge leads with "how to read conditions"

Compute's top-level condition reasons are deliberately **pointers, not causes**.
`Workload.Available=False` with reason `QuotaNotGranted` names the blocking
subsystem; the real reason — `QuotaExceeded` vs `QuotaNoBudget` vs
`QuotaBackendUnavailable` — lives on an Instance's `QuotaGranted` condition
below it. An assistant that reports the pointer gives a wrong answer that reads
like a right one, so both the knowledge and `internal/agent.Diagnose` are built
around walking through them.

## Skills

Skills use progressive disclosure: only a name and one-line description enter
the system prompt, and the body is fetched when a request matches. That lets
compute publish many procedures at near-zero prompt cost.

| Skill | Covers |
|---|---|
| `workload-not-available` | Top-level triage, symptom to root cause to owner |
| `quota-triage` | `QuotaExceeded` vs `QuotaNoBudget` vs backend faults |
| `instance-not-ready` | `ImageUnavailable`, `InstanceCrashing`, `ConfigurationError` |
| `referenced-data-triage` | Missing, unauthorized, or oversized ConfigMaps/Secrets |
| `placement-triage` | `NoMatchingLocation`, `AmbiguousServingLocation`, `CityCodeMismatch` |

A skill never grants privileges. It can only direct the model toward tools that
are independently on the enforced allow-list, which is why these go through the
same review gate as any published configuration.

## Keeping this honest

Every reason in `internal/agent`'s catalog is classified user-actionable,
platform fault, or transient — the distinction that decides whether a customer
should change their spec or escalate. `TestCatalogCoversEveryAPIReason` parses
`api/v1alpha` and fails when a reason is added without being classified, so the
catalog cannot silently fall behind the API.

When you add a condition reason, add its catalog entry in the same change, and
update the relevant skill if the triage procedure changes.
