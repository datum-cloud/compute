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
| `embed.go` | Embeds both into the binary, so `cmd/compute-mcp` can serve them with no files to mount beside it. |
| `../../internal/agent` | The reason catalog and the diagnosis walk that back the tools. |

## Status

Landed here: the reason catalog, the diagnosis walk, the knowledge and skills
above, and `cmd/compute-mcp` — the MCP server that publishes the five read-only
tools (`workloads_list`, `workloads_get`, `instances_list`, `workload_diagnose`,
`reason_explain`) over Streamable HTTP.

## HTTP surface

One process answers everything compute's capability document points at:

| Route | Serves |
|---|---|
| `POST /mcp` | Streamable HTTP MCP, stateless. Requires the caller's bearer token and `X-Datum-Project`. |
| `GET /llms-full.txt` | The knowledge document. Public. |
| `GET /runbooks/<name>.md` | One skill. Public. |
| `GET /healthz` | Liveness. |

The URL says `runbooks` while the directory says `skills`: the path belongs to
the agent framework and is already baked into shipped capability documents, so
it is not compute's to rename. Both document routes are unauthenticated on
purpose — the assistant fetches them to build a system prompt, before it holds
any project context, and they are static text with no tenant data in them.

Three properties of the server are worth knowing before you deploy it:

- **A project is a control plane, not a namespace.** `X-Datum-Project` selects a
  project by rewriting the API host path to
  `/apis/resourcemanager.miloapis.com/v1alpha1/projects/<project>/control-plane`,
  the same rewrite `internal/quota`, `internal/referenceddata` and the datumctl
  plugin perform. Within that control plane compute's objects live in the
  `default` namespace.
- **Every read runs as the caller.** The server holds no credential of its own
  for the project control plane. It takes the bearer token off the request and
  builds a client with it, so a tool call can never see more than the person who
  asked could see themselves, and the server needs no impersonation privilege.
- **The project comes from a header, not a tool argument.** Tool arguments are
  chosen by the model; a model that could name its own project would be one
  prompt injection away from another tenant's workloads. The caller sets
  `X-Datum-Project` after authenticating the user.

Compute publishes no mutating tool. Allow-list enforcement is the gateway's job,
but a tool that does not exist cannot be called through any path.

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
| `stalled-transient` | A transient reason that has outlived its expected window |

A skill never grants privileges. It can only direct the model toward tools that
are independently on the enforced allow-list, which is why these go through the
same review gate as any published configuration.

## Keeping this honest

Every reason in `internal/agent`'s catalog is classified user-actionable,
platform fault, or transient — the distinction that decides whether a customer
should change their spec or escalate. `TestCatalogCoversEveryAPIReason` parses
`api/v1alpha` and fails when a reason is added without being classified, so the
catalog cannot silently fall behind the API.

A transient reason also declares how long it should take. `stalled` is the
fourth actionability the tools can report, derived at read time when a
condition has held a transient reason past that window — never written in the
catalog and never stored.
`TestTransientReasonsThatSayWaitDeclareAWindow` fails when a reason tells the
reader to wait without saying how long is reasonable, because a transient claim
nobody can falsify is how a wedged workload reads as healthy.

When you add a condition reason, add its catalog entry in the same change, give
it a window if it is transient, and update the relevant skill if the triage
procedure changes.
