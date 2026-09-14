# Skill: create a workload

Use when someone asks to deploy, run, or create something on Datum — a new
Workload, or a change to one that does not exist yet — and whenever you are
about to call `compute_workload_render`, or `resources_plan` or
`resources_apply` with a Workload in the manifests.

## The one thing to know

**You never write a workload directly. You render it, plan it, show it, and
apply only what the user agreed to.** `resources_apply` takes the manifests
`resources_plan` returned and that plan's token, and nothing else. Change a
manifest by one character and the token stops matching, so what gets created is
exactly what was shown and agreed to, or nothing at all.

Two things you cannot do, however the request is phrased:

- **You cannot build or push an image.** The image has to exist in a registry
  before any of this starts.
- **You cannot turn Compute on for a project, and you cannot grant it quota.**
  Both are Datum's to grant. Say so and name the step the user takes.

The project is fixed by the request that reached you. There is no tool argument
for it, so you cannot create a workload in a project other than the one the
conversation is already scoped to. If the user names a different project, say
that this conversation only reaches the current one.

## 1. Check the prerequisites before gathering anything

Four things have to be true. Each has a read-only tool, and each failure has a
different answer:

| Check | Tool | If it fails |
|---|---|---|
| Compute is offered to the project | `locations_list` with service `compute` | Nothing can be placed. Datum's to enable — the user runs `datumctl compute access request`, and approval is a manual step on Datum's side. |
| Somewhere to run it | `locations_list` with service `compute` | The location names it returns are the only ones a placement may name. A location missing from the list is one compute is not offered in. An empty list means nothing is available to this project yet; that is Datum's, not something the user can add. |
| A network | `resources_list` for kind `Network` in `networking.datumapis.com/v1alpha` | `default` by convention. If the one the workload names is missing, add a Network manifest of that name to the same `resources_plan` call — the plan orders it ahead of the Workload. Say so when you show the plan, because it is a second object being created. |
| Quota | `quota_get` with service `compute.datumapis.com` | Quota is granted by Datum and cannot be self-served. A project with none can still create a workload; its instances then sit at `QuotaGranted=False` with `QuotaNoBudget` and never start. |

Do the quota arithmetic before you apply, not after. Replicas times the instance
type's size from `compute_instance_types_list`, against what `quota_get` says is
left, tells you whether this will start. If it will not, say so *before* asking
for confirmation — a workload that creates cleanly and then sits at
`QuotaExceeded` looks like a success and is not. Load `quota-triage` for the
difference between being over quota and having none.

## 2. Container or virtual machine

There are two runtimes and a workload picks exactly one. This is not adjustable
later — switching means a different workload.

**Container (a sandbox).** The common case. One or more containers, each with a
fully qualified image. Choose this unless the user needs a whole operating
system.

**Virtual machine.** A full OS booted from a disk image. Choose this only if the
user asks for one, or needs to log into the machine. It carries the extra
requirements in the trap list below.

### The image is a prerequisite, not an input you can produce

The image must:

- **already exist in a registry** the platform can reach. You cannot build one.
- **be fully qualified** — `docker.io/netdata/netdata:latest`, not `netdata`.
  A bare name is the most common cause of `ImageUnavailable` afterwards.
- **be built for the runtime Datum runs it on.** An image that runs on a laptop
  can still fail here. The user builds it with `datumctl compute build`, which
  checks for the known incompatibilities and can fix them.

If the user has no image yet, stop and say that: the build is theirs to run, and
everything below waits on it. Do not render a manifest around an image name
nobody has pushed.

## 3. Gather the inputs

Ask for what is missing rather than inventing it. `compute_workload_render`
takes:

- **name** — a DNS label (lowercase letters, digits and `-`). It is the object's
  name and cannot be changed later.
- **image** — fully qualified, per above.
- **placements** — where the instances run, and how many. A placement says
  where in exactly one of two ways:
  - **`locations`** — location names, taken verbatim from `locations_list`. Use
    this when the user named specific places. A name that is not in that list
    can never be satisfied, so never invent one and never pass a city code here.
  - **`locationSelector`** — a selector over the topology `locations_list`
    reports for each location, such as `topology.datum.net/city-code: DFW`. This
    is how you say "every location in Dallas" or "every location in a region"
    without naming them, and it picks up locations added later on its own.

  Group locations that scale together into one placement.
- **replicas** — `minReplicas` must be at least 1. There is no scaling from
  zero, and the ceiling is 1000.
- **instance type** — from `compute_instance_types_list`. Leave it unset to take
  the default.
- **runtime class** — only if the user named an execution tier. The choices are
  the RuntimeClass objects `resources_list` returns for
  `compute.datumapis.com/v1alpha`. Leave it unset otherwise; the platform picks
  its default, and the tier cannot be changed later.
- **network** — the name of a Network from `resources_list`, or leave it unset
  for `default`.
- **port** — optional, and named. A port is how anything reaches the workload;
  ask whether it serves traffic rather than guessing.
- **environment variables** — literal values, or drawn from a ConfigMap or a
  Secret.
- **ConfigMap and Secret references** — mounted as volumes, or read as
  environment variables. They must already exist in the project, and the user
  must be able to read them, or create is rejected.
- **a public IPv4 address** — only if the workload has to be reachable from the
  internet on IPv4. Ask; do not add one by default, and do not leave it out of a
  workload that clearly needs one, because it cannot be added afterwards.

## 4. The traps

These are the ones that cost a round trip. Check the rendered manifest against
this list before you plan.

1. **One instance type.** `datumcloud/d1-standard-2` is the only one accepted
   today. `compute_instance_types_list` is the check; anything else is rejected
   outright.

2. **Per-container CPU and memory are not accepted.** A `resources` block on a
   container is rejected, and so are adjustments to the instance type's own
   requests. The size of an instance comes from the instance type and nothing
   else. If the user wants a different size, that is a request to Datum.

3. **ConfigMap volumes use `name`; Secret volumes use `secretName`.** The two
   spellings sit next to each other in the same list and are not
   interchangeable. Getting it wrong reads as a missing required field.

4. **Every volume must be attached.** A volume that is declared and never
   attached to a container or to the virtual machine is rejected — the create
   fails on the volume, not on the attachment.

5. **The network interface is settled at create.** Its name, the address
   families it carries, any extra addresses (a public IPv4 among them), and what
   becomes of those addresses when the instance goes away are all immutable. An
   instance gets one interface. If any of this turns out to be wrong later, the
   fix is a new workload, so ask now:
   - IPv6 only is the default. If the workload has to answer on IPv4, that has
     to be asked for at create.
   - A published address — one in DNS, or allowed through someone's firewall —
     wants a reclaim policy that keeps it, and that choice is also final.

6. **Virtual machines need two extra things.** SSH keys on the template's
   metadata, under the annotation `compute.datumapis.com/ssh-keys`, one
   `username:key` line per key — a create without them is rejected. And the
   first volume attached must be a bootable disk populated by an Ubuntu image.
   First, not merely present.

7. **ConfigMaps and Secrets have size limits**: 256 KiB per object, and 1 MiB
   for everything one workload references put together. Over either and the
   workload reports `SourceTooLarge` rather than failing at create.

8. **A port is not a public URL.** `ports` plus the ingress rule the render
   emits makes the port reachable on the instance's own address — that is the
   whole of what this path does. A managed public HTTPS URL in front of an HTTP
   workload is published separately, and today the only way to get one is
   `datumctl compute deploy --http-port`, which these tools cannot do. Say that
   plainly when the user asks for a URL: they will get an address and a port,
   not a hostname, unless they run that command themselves.

9. **Editing a ConfigMap does not restart anything.** The new contents reach the
   machines, but a process that read the file at startup goes on running with
   what it read. Say this whenever a config change is the point of the
   conversation — the user has to restart the workload themselves, and there is
   no tool here that does it.

## 5. The sequence

Follow it in order. Each step exists because of a failure the next one cannot
catch.

1. **`compute_workload_render`** — inputs in, a full Workload manifest out. It
   writes nothing and reaches nothing. Read what came back rather than assuming
   it matches what you asked for, and read its notes.

2. **`resources_plan`** — pass the rendered manifest, and a Network manifest
   ahead of it if step 1 found the network missing:

       apiVersion: networking.datumapis.com/v1alpha
       kind: Network
       metadata:
         name: default
         namespace: default
       spec:
         ipam:
           mode: Auto

   The plan validates everything without creating it, settles create versus
   update for each manifest, reports what would change, and returns the
   manifests with a plan token. This is where the traps above surface as real
   rejections, and where you learn whether a workload of this name already
   exists.

3. **Show the user the planned manifests and the diff.** Whole, not
   summarised, and the plan's own manifests rather than your draft. Then say in
   plain words what will be created, where, how many, and what it will cost
   against their quota. If a Network is in the plan, say that: it is a second
   object.

4. **Get an explicit yes.** A question about the plan is not a yes. "Looks
   right" is. If the user asks for any change, go back to step 1 — a token
   minted for the old manifests is not valid for new ones, and must not be
   applied because it was close.

5. **`resources_apply`** with the plan's manifests, in the plan's order, and its
   token.

6. **`compute_workload_diagnose`** for the rollout. Creation succeeding means
   the request was accepted, not that anything is running. Tell the user what to
   expect: instances appear, then start, and the first pull of a large image
   takes a while. If it is not serving, that is `workload-not-available`'s
   procedure, not this one.

## What to do when a step fails

- **Render is missing something** — an input you did not gather. Ask for it by
  name. Do not fill it in with a plausible default; a guessed port or location
  is a workload that runs in the wrong place.

- **The plan rejects it** — this is the server's own answer, in its own words,
  and it names the exact field. Quote the field path verbatim and translate the
  rule beside it: `spec.template.spec.volumes[1].name: volume must be attached
  at least 1 time` is "the `config` volume is declared but never mounted". Fix
  it, render again, plan again. A rejected plan has no token, so there is
  nothing to apply.

- **The plan says update when you expected create** — a workload of that name is
  already there. Stop and say so. Ask whether the user meant to change the
  existing one, and check the diff for anything immutable from trap 5 before
  going on, because those rejections arrive at apply and not before.

- **Apply refuses the token** — something changed after the plan. That refusal
  is the mechanism working. Re-plan, show the new manifests, and ask again.
  Never work around it.

- **Apply succeeds and nothing starts** — hand it to `compute_workload_diagnose`
  and follow the skill it names. Quota and image problems both look like this
  and lead to opposite advice.

## If the user has a shell

You are usually working without one. When the user is at a terminal, the same
workload is one command, and these are theirs to run, not yours to assume:

    datumctl compute access request
    datumctl compute build --push --output ghcr.io/acme/api:1.4.2 .
    datumctl compute deploy api --image=ghcr.io/acme/api:1.4.2 --city=DFW --min=1 --port=8080
    datumctl compute deploy api --image=ghcr.io/acme/api:1.4.2 --city=DFW --http-port=8080

The last one is the only way to get a public HTTPS URL, per trap 8. Offer them
when a step above has no tool behind it — the access request and the URL have
none at all — and otherwise stay with the tools, which is the path that shows
the user the manifest before anything is created.

## Reporting

Say what will exist, where, and how many, in the user's own words first: "one
container running `ghcr.io/acme/api:1.4.2` in Dallas, two replicas, answering on
port 8080". Then the identifiers — the workload name, the image with its tag,
the location names — because those are what they need to check it themselves or
to escalate.

After apply, say plainly that the workload was created and that it is not
running yet, and what you will look at next. A create reported as a deploy is
the same mistake as reporting a pointer reason: technically true, and it reads
as more than it is.

## When to file a capability gap

`report_capability_gap__compute-datumapis-com` is for cases where these tools
could not get a legitimate creation done:

- A field the user needs that `compute_workload_render` has no input for, where
  the API clearly supports it — `InsufficientDetail`, quoting the field and what
  you tried.
- A plan rejection whose message does not name what to change, so the user
  cannot act on it — `UnactionableGuidance`, quoting the message verbatim.

Not gaps, however awkward the turn:

- **No image.** Building one was never in scope here.
- **No quota, or Compute not enabled.** Those are grants, and the tools
  reporting them accurately is the tools working.
- **A rejection that was right.** An unsupported instance type or an unattached
  volume is the plan doing its job — that is the answer, and it saved a broken
  workload.
- **The user declined to confirm.** Not applying is the correct outcome.
