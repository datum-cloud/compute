# Consumer Compute User Journeys

**Issue:** [datum-cloud/enhancements#823 — Consumer Compute Experience: CLI-First Workloads, Portal-Assisted Debugging](https://github.com/datum-cloud/enhancements/issues/823)
**Status:** Draft

---

## Summary

[#823](https://github.com/datum-cloud/enhancements/issues/823) draws a line between `datumctl` (create and manage) and the portal (understand and debug), but it describes the split in terms of capabilities, not the sequence a consumer actually lives through to get there. This document walks that sequence end to end — from someone who has never heard of Datum Compute through a workload running unhealthy in production — and calls out, at each step, whether the experience it implies exists today, is in flight, or is still undesigned.

The goal is to surface gaps before they're discovered by a consumer instead of a spec. Each journey ends with a **Gap** callout where the current state falls short of what #823 promises.

---

## Journey 1 — Before signing up

**Who:** a developer evaluating Datum Compute against Fly.io, Railway, or a hyperscale offering, with no account yet.

They land on docs or a blog post and need to answer one question fast: *can I run a container here, and what does that look like day to day?* Since #823 makes `datumctl` the primary surface, the first thing this person should see is a real command, not a marketing screenshot.

**What should happen:** docs lead with a copy-pasteable `datumctl compute deploy` example (per [`datumctl-compute-dx.md`](./datumctl-compute-dx.md)) and a one-line statement of positioning — edge-native, container-based, not a hyperscale competitor — before any signup prompt.

**Current state:** onboarding starts at account creation; there's no public-facing CLI example that works pre-signup. A prospective consumer can't try the primary surface without first going through account → org → billing → project provisioning.

> **Gap:** the CLI-first pitch of #823 is not visible until *after* signup, which is backwards for someone still deciding whether to sign up.

---

## Journey 2 — Getting access to compute

**Who:** someone who has just created an account and organization, trying to figure out what's required before they can deploy.

**Path today:** account profile → org profile → billing setup → project provisioning (an animated portal screen: "Setting up your organization" → "Setting up your project"). No step in this flow mentions compute specifically.

**What #823 implies:** a consumer should reach a clear, single answer — "you can deploy now" — before writing a spec. Today that answer doesn't exist as a UI moment. It's implicit: once a project is `Ready`, the compute controller engages it automatically, without the consumer taking any compute-specific action.

**In flight:** [`service-entitlement-gating.md`](../compute/development/rfcs/service-entitlement-gating.md) (proposed) would change this — engagement would depend on an active Milo `ServiceConsumer` entitlement rather than project readiness alone. That's a backend correctness fix; it doesn't by itself produce a consumer-visible "compute is available" signal.

> **Gap:** whether gated by entitlement or not, there's no explicit moment — in the portal or the CLI — that tells a consumer "compute is accessible in this project." The signal is either implicit (always on) or, post-RFC, present in the API but not surfaced anywhere a person looks.

---

## Journey 3 — Enabling compute

**Who:** a consumer who has access, about to run their first deploy.

**What should happen:** before committing to a workload spec, the consumer checks capacity. `datumctl compute quota` (proposed in [`datumctl-compute-dx.md`](./datumctl-compute-dx.md)) is the natural place — a plain-language answer instead of raw condition fields.

**Current state:** `compute quota` doesn't exist yet; quota state exists on the backend (`InstanceQuotaGranted` condition, `QuotaGranted` on `Workload` status — [`api/v1alpha/instance_types.go`](../../api/v1alpha/instance_types.go)) but is only visible today through raw `describe` output on resources that don't exist until after a deploy attempt.

**Failure mode to avoid:** a consumer applies a workload, quota is denied, and the only feedback is a condition string discovered by chance. #823's CLI-first framing only holds if the terminal is where this is caught — at `apply`/`deploy` time, not buried in a later `describe`.

> **Gap:** there is currently no pre-flight step. "Enabling compute" is not a consumer action today — it's a byproduct of project readiness — so there's nothing to build confidence before the first real deploy.

---

## Journey 4 — Once something is deployed

**Who:** a consumer who has run `datumctl compute deploy` (or `apply -f`) and now needs to know if it worked.

**Happy path:** status returns healthy, the CLI hands back a portal URL for the workload, and the consumer never needs to open a browser. This direction — CLI outputs a portal link — has no open dependency; it's a small addition to whatever `deploy`/`status` output ships.

**Unhealthy path — this is where #823's two-surface split is tested:**

1. **CLI** shows a plain-language condition and a next step (`datumctl compute status` mockups in [`datumctl-compute-dx.md`](./datumctl-compute-dx.md) already model this well). This part is designed, not yet built.
2. **CLI hands off to portal** for anything requiring history or logs — the workload detail view #823 calls for: current status/conditions, an activity timeline, and log access.
3. **Portal workload detail view does not exist yet.** No "workload" surface exists in the portal beyond generic resource views. The activity timeline capability is built and wired (`app/resources/activity-logs/`), but its only shipped consumer is DNS zones — a workload timeline would be new work following that pattern, not a reuse of an existing view.
4. **Logs have no backend to point to.** Customer-facing workload logs are still in research ([datum-cloud/enhancements#714](https://github.com/datum-cloud/enhancements/issues/714) — storage backend, multi-tenancy, and query interface are all open questions). The portal can't "provide access to logs" as a UI feature when the log storage and query layer underneath don't exist.
5. **Contextual docs** (link to the doc explaining *this* condition, not a generic index) depend on conditions being enumerable and stable enough to map 1:1 to doc pages — feasible today since conditions already exist on the API, independent of the logs/timeline gaps.
6. **Notifications** (created, degraded, failed placement, deleted) reuse existing Activity + email infra, so once a workload activity timeline exists, wiring notifications is comparatively cheap — the timeline is the blocker, not the notification mechanism.

> **Gap:** of the three pillars #823 promises for debugging in the portal — conditions, activity timeline, logs — only conditions exist today on the API. The other two require new portal work (timeline) and an entirely separate, still-unscoped effort (logs backend). The CLI side of this journey (`compute status`, `compute logs`) is further along as a design than the portal side is as an implementation.

---

## Cross-journey gap summary

| Journey | What #823 promises | What exists today |
|---|---|---|
| Pre-signup | CLI-first value visible before commitment | Nothing public; CLI only reachable post-signup |
| Getting access | Clear "you can deploy" signal | Implicit — no consumer-facing signal, gated or not |
| Enabling compute | Pre-flight quota confidence | No `compute quota` command; state only visible after a failed deploy |
| Post-deploy (happy) | CLI ⇄ portal cross-links | Not yet implemented, but low-risk to add |
| Post-deploy (unhealthy) | Conditions + timeline + logs + contextual docs in portal | Conditions exist; timeline is new work; logs are pre-design ([#714](https://github.com/datum-cloud/enhancements/issues/714)) |

**Sequencing implication:** the portal workload detail view and `datumctl compute` are both prerequisites for #823's core promise, but logs are a hard dependency the enhancement doesn't control — it inherits #714's timeline. If #823 ships status + activity timeline + contextual docs first and treats logs as a fast-follow once #714 lands, the debugging experience is still coherent at each stage rather than shipping a portal view with a dead "logs" tab.

---

## Open questions

- Does "getting access to compute" need a consumer-visible action at all, or is the goal an always-on experience with entitlement enforcement staying purely a backend/billing concern?
- Should the pre-signup CLI example run against a real (rate-limited, sandboxed) endpoint, or stay illustrative?
- Where does `datumctl compute quota` read from once entitlement gating ships — does a denied `ServiceConsumer` produce a distinguishable message from a granted-but-exhausted quota?
