# Agent Sandboxes

Status: Proposal
Audience: Product, platform operators, prospective customers
Date: 2026-04-08
Revisited: 2026-10-15

## What we're building

A new Datum compute experience purpose-built for **AI agents that need
their own isolated, ready-to-use environment**. Instead of asking users to
assemble a workload, pick regions, and configure scaling, we're shipping a
small set of resources that let anyone — or any agent — say:

> "Give me a copy of the Python data-science sandbox, just for this session."

…and get a fully initialized, isolated environment back in the time it takes
to load a web page.

We call the new capability **Agent Sandboxes**.

## Why we're building it

The teams building AI agents today are stuck between two bad options:

- **Run everything in one shared container.** Cheap and fast, but anything
  the agent does — installing packages, executing model-generated code,
  touching files — leaks into the next session. One bad command can break
  the environment for everyone.
- **Spin up a full cloud workload per session.** Properly isolated, but
  slow to start, expensive to keep idle, and wildly over-engineered for
  "I just need a Python process for the next 20 minutes." Users have to
  learn deployment concepts that have nothing to do with their problem.

Neither option fits how agents actually work. An agent platform typically
needs **many small, short-lived, strongly isolated environments**, created
on demand, often hundreds or thousands per day, with state that survives a
pause and disappears when the session ends.

This is also where the broader ecosystem is heading. The Kubernetes
community has started a project — [agent-sandbox][upstream] — specifically
to standardize this shape of compute. Datum is well-positioned to offer a
best-in-class version of it: our underlying infrastructure (Unikraft-based
microVMs with snapshot/restore) makes "instant, isolated, stateful" the
default rather than the exception.

The product opportunity is to turn agent sandboxes into a **catalog Datum
ships and curates**, the way cloud providers ship machine images. A customer
can then choose how much ready capacity to reserve for a catalog environment,
while Datum also offers a managed default for customers who do not need to
make that decision.

## What becomes available

Four new resources, layered so that the simple case stays simple and the
advanced case stays possible.

### 1. `Sandbox` — one isolated environment

The core building block. A `Sandbox` represents a single, isolated, stateful
environment running one image. It has a stable name, stable network address,
and persistent storage that survives pause and resume.

This is the lowest-level resource. Most users never touch it directly.

### 2. `SandboxTemplate` — a reusable, curated environment definition

A **named, versioned blueprint** for a kind of sandbox: its image or prepared
starting snapshot, CPU and memory, storage defaults, exposed ports, isolation,
and network policy. Datum ships and maintains a catalog of these out
of the box — `python-agent-runtime`, `node-agent-runtime`, `code-interpreter`,
`headless-browser`, `jupyter-datascience`, and so on. Customers and partners
can publish their own templates into their own namespaces using the same
mechanism.

Templates are the product surface. They are what users browse, pick, and
build against. A published version resolves to an immutable starting state,
so a pool can be replenished without silently changing the environment that
customers receive.

### 3. `SandboxPool` — ready capacity for a template

A pool keeps pre-initialized, unassigned sandboxes ready and replenishes them
as they are claimed. It references one template version and specifies where
copies should be ready, how many to reserve, and what to do when the pool is
empty. A zero-sized pool can still serve claims by starting them on demand.

Pools are separate from templates because ready capacity has an owner, a
location, and a cost. Two customers can use the same curated template without
sharing a capacity budget; one customer can maintain separate production and
development pools. Datum provides a managed default pool for the simple case.
Customers who need a latency guarantee or cost ceiling can create and manage
their own pool. Pool status exposes desired and ready capacity, provisioning
failures, and exhaustion rather than burying these facts on the template.

### 4. `SandboxClaim` — "give me a sandbox for this session"

The user-facing request. A `SandboxClaim` says "I want a fresh sandbox based
on template X, with these small overrides." It can optionally name a pool
when the caller needs specific reserved capacity or placement. Datum selects
a managed default pool when only a template is named. The platform produces
a per-claim `Sandbox` that is a fully independent copy — its own identity,
its own storage, its own lifecycle.

A claim is typically 5–10 lines and can be created by an agent without any
documentation. It is the resource an agent platform creates per session,
per user, or per task. Claim status reports whether allocation used a ready
copy or required a cold start. Overrides that change the prepared environment
may require a cold start; the API must make that behavior visible.

## How this fits the existing platform

Datum already has a `Workload` resource for declarative, multi-region,
horizontally scaled applications. `Workload` is and remains the right tool
for production services. Agent sandboxes are a *different* shape of compute:

| | **Workload** | **Agent Sandbox** |
|---|---|---|
| Cardinality | Many replicas across regions | One environment per session |
| Lifetime | Long-running | Minutes to hours, then gone |
| Scaling | Horizontal, automatic | Each sandbox is one unit; pools scale ready inventory |
| State | Usually external (DB, cache) | Local, persistent across pause |
| Allocation time | Seconds to minutes | Depends on warm availability, startup, or resume |
| Who creates it | A human, once | An agent, thousands of times |

The two live side-by-side. We are not replacing `Workload`; we are adding
the right primitive for the use case it was never designed for.

The underlying repository is now **`compute`**, reflecting that it owns
more than one top-level concept on the Datum compute platform.

---

## User journeys

### Journey A — The agent platform (consumer)

**Persona.** Maya is building an AI coding assistant. When a user asks her
agent to "analyze this CSV and plot the results," the agent needs to write
and execute Python in a fresh, isolated environment, then throw it away.

**Today, without agent sandboxes.** Maya stands up a Kubernetes cluster,
writes a custom controller that creates pods per session, figures out how
to give each pod its own storage, builds a queue of pre-warmed pods to
hide cold starts, writes a janitor to clean up dead sessions, and worries
constantly about whether one user's `pip install` can affect another's.
Months of work before her agent runs its first line of code in production.

**With agent sandboxes.**

1. Maya browses the Datum sandbox catalog and picks `python-data-science`.
   She reads the one-page description: Python 3.12, pandas, numpy,
   matplotlib pre-installed, 2 GB RAM, 10 GB scratch disk, isolated per copy.
2. In her agent code, when a session starts, she creates a `SandboxClaim`
   referencing that template. Five lines of YAML, or one API call.
3. Datum allocates a ready copy from its managed default pool when one is
   available. The claim reports `Ready` with an endpoint her agent can
   connect to. If the pool is empty or her overrides require a different
   starting environment, the claim reports that it is starting a new copy.
4. Her agent uses the sandbox: writing files, executing code, generating
   plots. Everything stays inside that one copy.
5. When the user's session ends — or after 15 minutes of inactivity —
   the sandbox is deleted. Storage goes with it. No cleanup code on
   Maya's side.
6. If Maya wants something not in the catalog, she pushes her own image
   and Datum builds a custom template for her in her own namespace. The
   per-claim experience is identical.
7. As her traffic grows, she creates a dedicated pool for the same template
   in her preferred location. She chooses its ready capacity and what
   happens when that capacity is exhausted, then points production claims
   at the pool. Her development claims continue to use the managed default.

**What Maya does not have to think about to get started:** regions, scaling,
image building, warm pools, cluster sizing, isolation backends, snapshot
management, garbage collection, or the difference between a "container" and
a "VM." She can take control of placement and ready capacity when her
application needs it.

### Journey B — The internal team (operator)

**Persona.** Devon is on the Datum platform team. He owns the catalog of
sandbox templates Datum ships to customers. A new team has asked for a
`headless-browser` sandbox for agents that need to scrape and screenshot
web pages.

**With agent sandboxes.**

1. Devon writes a Dockerfile for the headless-browser environment:
   Chromium, Playwright, a small HTTP wrapper. Standard stuff.
2. He creates a `SandboxTemplate` in the Datum catalog namespace pointing
   at that image. He sets resource sizing, the ports to expose, the security
   baseline, and a default idle timeout of 10 minutes.
3. Datum's build pipeline picks up the new template, builds the image
   for the appropriate isolation backend, validates it, and prepares a
   versioned starting snapshot where useful. Devon watches the template's
   status go from `Building` to `Ready`.
4. Devon runs a few test claims against it, confirms the browser works,
   sets the template to `Published`. It now appears in the customer-facing
   catalog.
5. Devon creates a managed default `SandboxPool` with 10 ready copies in
   the initial location. A week later, traffic has grown; he raises that
   pool to 50 without changing the template or customer claims. A customer
   with a strict latency target can create a separate pool for the same
   published template.
6. A security advisory drops for Chromium. Devon publishes
   `headless-browser:1.1` as a new template version. He moves the default
   pool to it using a controlled rollout. New copies use the patched version;
   existing claimed sandboxes keep running until their sessions end. Customer
   pools stay pinned until their owners choose to move them.
7. Datum's billing and observability surfaces show per-template and per-pool
   usage: claim volume, session duration, ready capacity, pool exhaustion,
   and storage. Devon uses this to right-size the managed pool and report ROI.

**What Devon never has to think about:** writing a controller, managing
pods, hand-rolling a warm-pool scheduler, building a per-copy storage
system, or coordinating rollouts across regions.

### Journey C — The end customer of the agent (incidental)

**Persona.** Priya is using Maya's coding assistant. She doesn't know what
Datum is and never will.

What she experiences: she asks the agent to do something. The agent
responds in roughly the same time it would take any chatbot. Behind the
scenes, a sandbox was claimed, used, paused, and cleaned up — but to
Priya, it just felt like the assistant worked. Her data didn't leak into
anyone else's session, and the assistant didn't get slower as more people
used it.

That invisible reliability is the actual product.

---

## What success looks like

- **Time-to-first-sandbox** for a new agent platform: under one hour from
  signup, with no infrastructure code written.
- **Allocation latency** from an already ready pool: measure the 95th
  percentile separately from cold starts and resume. Set a numerical target
  after measuring the end-to-end path on Datum.
- **Time to first instruction**: measure when the caller can successfully
  use the sandbox, not only when a claim has an endpoint or a `Ready` status.
- **Pool transparency**: report warm allocation rate, exhausted claims,
  refill time, ready capacity, and idle cost per pool and location.
- **Idle cost** of a paused sandbox: an order of magnitude lower than
  a comparable always-on container.
- **Catalog breadth**: Datum ships at least the top 5 agent runtimes
  (Python, Node, code interpreter, headless browser, notebook) in the
  initial release, with a clear path for customer-published templates.
- **Operator ergonomics**: a new sandbox template can be added to the
  Datum catalog by one engineer in under a day.

## Open product questions

- Which templates ship in the launch catalog, and in what order?
- What is the pricing shape — per claim, per active sandbox-minute,
  per ready pool slot, or some combination? Who pays for a managed default
  pool versus a customer-owned pool?
- Do we expose customer-published templates in v1, or hold them for v2?
- How do we surface template versioning and deprecation to consumers
  who may have thousands of live claims at any moment?
- What are the default behaviors when a pool is empty: cold start, wait up
  to a deadline, or fail fast? Which claim overrides remain eligible for a
  warm copy?
- How much pool placement, scaling, and spend control do we expose in v1?
- Whether the upstream compatibility layer (see below) ships in phase 1
  or follows the MVP.

## Upstream compatibility

The [kubernetes-sigs agent-sandbox][upstream] SIG Apps project now exposes
`Sandbox` as a core custom resource, with `SandboxTemplate`, `SandboxWarmPool`,
and `SandboxClaim` as extensions. Its current `v1beta1` claim references a
warm pool, which references a template; a pool with zero ready replicas can
serve cold-start claims. The pool has its own desired and ready replica counts,
update strategy, and Kubernetes scale interface. [GKE Agent Sandbox][gke]
offers this model as a managed add-on. These are evolving project APIs, not
built-in Kubernetes resources.

The broader sandbox market reinforces the separation. [Daytona][daytona]
builds a repeatable starting snapshot and manages warm pools by snapshot and
region; [Azure Container Apps dynamic sessions][azure] also make the session
pool an explicit capacity resource. A prepared environment and the capacity
reserved for it have different owners, lifecycles, and costs.

Datum's compute and network interfaces are location-aware and do not map
one-to-one to a native Pod. We want to participate in this ecosystem while
preserving those capabilities in the native API.

Our approach has three parts:

- **Mirror the vocabulary.** Datum's native API uses the same resource
  names and lifecycle concepts as upstream wherever the semantics line up.
  In particular, a pool names a template and a claim may name a pool. Datum
  also allows a claim to name a template alone, resolving a managed default
  pool so that simple customers do not have to operate one.
- **Ship a compatibility layer.** A thin, optional component that accepts
  upstream `agents.x-k8s.io` and `extensions.agents.x-k8s.io` resources and
  translates supported fields into Datum-native ones. Publish a conformance
  matrix for supported fields and behavior; do not promise that every upstream
  manifest or SDK works unchanged before the layer exists and is tested.
- **Contribute upstream.** Bring the patterns Datum's runtime unlocks
  (snapshot-based warm pools, sub-second allocation, non-Pod-shaped
  isolation backends) back to the SIG. The upstream [roadmap][roadmap]
  already identifies decoupling its API from its Pod-backed runtime as work
  in progress, making this a useful area for collaboration.

We are intentionally **not** adopting the upstream schema as Datum's v1
native contract while it remains a pre-1.0 API. Doing so would tie Datum's
stability promises to upstream's churn and its current Pod-backed fields.
Revisit that decision as the upstream runtime abstraction and API mature.

**Phase 1 scope.** The compatibility layer is likely to land *after* the
MVP rather than alongside it. Phase 1 prioritizes shipping the Datum-native
API, the curated catalog, customer-controllable pools, and the warm-pool
fast path. Define the compatibility mapping during phase 1 so native API
choices do not make the later bridge unnecessarily difficult.

## What's *not* in scope

- Replacing `Workload` for long-running, multi-region production services.
- A general-purpose VM or container product. Agent sandboxes are
  opinionated on purpose: one image, one copy, one session.
- A development IDE or notebook UI. Datum provides the runtime; the
  agent platform or developer tool provides the experience on top.

[upstream]: https://github.com/kubernetes-sigs/agent-sandbox
[gke]: https://docs.cloud.google.com/kubernetes-engine/docs/concepts/machine-learning/agent-sandbox
[daytona]: https://www.daytona.io/docs/en/warm-pools/
[azure]: https://learn.microsoft.com/azure/container-apps/sessions
[roadmap]: https://github.com/kubernetes-sigs/agent-sandbox/blob/main/roadmap.md
