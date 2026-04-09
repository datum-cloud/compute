# Agent Sandboxes

Status: Proposal
Audience: Product, platform operators, prospective customers
Date: 2026-04-08

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
ships and curates**, the way cloud providers ship machine images — except
allocation is measured in milliseconds and idle copies cost almost nothing.

## What becomes available

Three new resources, layered so that the simple case stays simple and the
advanced case stays possible.

### 1. `Sandbox` — one isolated environment

The core building block. A `Sandbox` represents a single, isolated, stateful
environment running one image. It has a stable name, stable network address,
and persistent storage that survives pause and resume.

This is the lowest-level resource. Most users never touch it directly.

### 2. `SandboxTemplate` — a reusable, curated environment definition

A **named, versioned blueprint** for a kind of sandbox: which image to run,
how much CPU/memory, which ports to expose, how long to keep it warm, what
isolation level to use. Datum ships and maintains a catalog of these out
of the box — `python-agent-runtime`, `node-agent-runtime`, `code-interpreter`,
`headless-browser`, `jupyter-datascience`, and so on. Customers and partners
can publish their own templates into their own namespaces using the same
mechanism.

Templates are the product surface. They are what users browse, pick, and
build against.

### 3. `SandboxClaim` — "give me a copy of that template"

The user-facing request. A `SandboxClaim` says "I want a fresh sandbox based
on template X, with these small overrides." The platform produces a per-claim
`Sandbox` that is a fully independent copy — its own identity, its own
storage, its own lifecycle.

A claim is typically 5–10 lines and can be created by an agent without any
documentation. It is the resource an agent platform creates per session,
per user, or per task.

### Behind the scenes: warm pools

Each `SandboxTemplate` can keep a pool of pre-initialized copies ready to
go. When a claim arrives, the platform hands out a warm copy and refills
the pool in the background. The user sees a sub-second allocation; the
operator sees a tunable knob on the template.

Warm pools are not a separate resource the user or operator has to manage —
they're a property of the template.

## How this fits the existing platform

Datum already has a `Workload` resource for declarative, multi-region,
horizontally scaled applications. `Workload` is and remains the right tool
for production services. Agent sandboxes are a *different* shape of compute:

| | **Workload** | **Agent Sandbox** |
|---|---|---|
| Cardinality | Many replicas across regions | One environment per session |
| Lifetime | Long-running | Minutes to hours, then gone |
| Scaling | Horizontal, automatic | None — each sandbox is its own unit |
| State | Usually external (DB, cache) | Local, persistent across pause |
| Allocation time | Seconds to minutes | Milliseconds (from warm pool) |
| Who creates it | A human, once | An agent, thousands of times |

The two live side-by-side. We are not replacing `Workload`; we are adding
the right primitive for the use case it was never designed for.

As part of this work, the underlying repository is being renamed from
`workload-operator` to **`compute`** to reflect that it now owns
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
3. Within tens of milliseconds, the claim reports `Ready` with an endpoint
   her agent can connect to. The environment is fully initialized — the
   Python interpreter is warm, libraries are loaded, ready for the first
   command.
4. Her agent uses the sandbox: writing files, executing code, generating
   plots. Everything stays inside that one copy.
5. When the user's session ends — or after 15 minutes of inactivity —
   the sandbox is deleted. Storage goes with it. No cleanup code on
   Maya's side.
6. If Maya wants something not in the catalog, she pushes her own image
   and Datum builds a custom template for her in her own namespace. The
   per-claim experience is identical.

**What Maya never has to think about:** regions, scaling, image building,
warm pools, cluster sizing, isolation backends, snapshot management,
garbage collection, or the difference between a "container" and a "VM."

### Journey B — The internal team (operator)

**Persona.** Devon is on the Datum platform team. He owns the catalog of
sandbox templates Datum ships to customers. A new team has asked for a
`headless-browser` sandbox for agents that need to scrape and screenshot
web pages.

**With agent sandboxes.**

1. Devon writes a Dockerfile for the headless-browser environment:
   Chromium, Playwright, a small HTTP wrapper. Standard stuff.
2. He creates a `SandboxTemplate` in the Datum catalog namespace pointing
   at that image. He sets resource sizing, the ports to expose, a default
   idle timeout of 10 minutes, and a warm pool size of 10.
3. Datum's build pipeline picks up the new template, builds the image
   for the appropriate isolation backend, validates it, and starts the
   warm pool. Devon watches the template's status go from `Building` to
   `Ready`.
4. Devon runs a few test claims against it, confirms the browser works,
   sets the template to `Published`. It now appears in the customer-facing
   catalog.
5. A week later, traffic has grown. Devon raises the warm pool from 10
   to 50 by editing one field on the template. No customer change needed.
6. A security advisory drops for Chromium. Devon publishes
   `headless-browser:1.1` as a new template version. New claims get the
   patched version automatically; existing live sandboxes keep running on
   the old version until their sessions end. No fleet-wide restart.
7. Datum's billing and observability surfaces show per-template usage:
   how many claims, how long they live, how often the warm pool runs dry,
   how much storage they consume. Devon uses this to right-size the pool
   and report ROI.

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
- **Claim-to-ready latency** against a catalog template: under 50 ms at
  the 95th percentile.
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
  per warm-pool slot, or some combination?
- Do we expose customer-published templates in v1, or hold them for v2?
- How do we surface template versioning and deprecation to consumers
  who may have thousands of live claims at any moment?

## What's *not* in scope

- Replacing `Workload` for long-running, multi-region production services.
- A general-purpose VM or container product. Agent sandboxes are
  opinionated on purpose: one image, one copy, one session.
- A development IDE or notebook UI. Datum provides the runtime; the
  agent platform or developer tool provides the experience on top.

[upstream]: https://github.com/kubernetes-sigs/agent-sandbox
