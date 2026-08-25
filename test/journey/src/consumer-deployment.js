// Datum consumer deployment journey.
//
// Exercises the path a customer actually walks: get a project, subscribe to
// compute, create a network, deploy a workload to a location, watch its
// instances become ready, then tear it all down again.
//
// The suite talks to the Datum API directly over HTTPS with a bearer token.
// It has no dependency on datumctl or a kubeconfig, so the same file runs
// locally under `k6 run` and in-cluster under k6-operator.
//
// Everything it creates is named and labelled with a per-run id so parallel
// runs never collide and leftovers are always attributable.

import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';

// ─── Configuration ──────────────────────────────────────────────────────────

const ENDPOINT = (__ENV.DATUM_API_ENDPOINT || 'https://api.staging.env.datum.net').replace(/\/+$/, '');
const TOKEN = __ENV.DATUM_TOKEN || '';
const ORG = __ENV.DATUM_ORG || 'datum-technology';

// Reuse mode (default) deploys into an existing project that already holds an
// active compute entitlement. Create mode provisions a throwaway project per
// run — see DATUM_REQUIRE_COMPUTE for why that cannot reach a running workload
// on staging today.
const CREATE_PROJECT = truthy(__ENV.DATUM_CREATE_PROJECT, false);
const PROJECT = __ENV.DATUM_PROJECT || 'datum-cloud';
const PROJECT_PREFIX = __ENV.DATUM_PROJECT_PREFIX || 'k6j';
const NAMESPACE = __ENV.DATUM_NAMESPACE || 'default';

// compute.datumapis.com is GatedByProvider: a freshly requested entitlement
// parks in PendingApproval until a human approves it. When this is false the
// run records the gate as a finding and stops before the deploy phase instead
// of failing.
const REQUIRE_COMPUTE = truthy(__ENV.DATUM_REQUIRE_COMPUTE, !CREATE_PROJECT);

// Service catalog object names (metadata.name, not the reverse-DNS identity —
// admission rejects the canonical name in spec.serviceRef).
const COMPUTE_SERVICE = __ENV.DATUM_COMPUTE_SERVICE || 'compute';
const NETWORKING_SERVICE = __ENV.DATUM_NETWORKING_SERVICE || 'networking-datumapis-com';

const CITY_CODE = __ENV.DATUM_CITY_CODE || 'DFW';
const PLACEMENT = __ENV.DATUM_PLACEMENT_NAME || 'us-central';
const REPLICAS = intEnv('DATUM_REPLICAS', 1);
const IP_FAMILY = __ENV.DATUM_IP_FAMILY || 'IPv6';
const IMAGE = __ENV.DATUM_IMAGE || 'docker.io/scotwells/datum-compute-hello-world:latest';
const INSTANCE_TYPE = __ENV.DATUM_INSTANCE_TYPE || 'datumcloud/d1-standard-2';
// The sandbox runtime cannot pull anonymously; the named secret must already
// exist in the target namespace. Empty string omits imagePullSecrets entirely.
const PULL_SECRET = __ENV.DATUM_IMAGE_PULL_SECRET === undefined ? 'dockerhub-pull' : __ENV.DATUM_IMAGE_PULL_SECRET;

const POLL_MS = intEnv('DATUM_POLL_INTERVAL_SECONDS', 5) * 1000;
const T = {
  projectReady: secs('DATUM_TIMEOUT_PROJECT_READY', 180),
  controlPlane: secs('DATUM_TIMEOUT_CONTROL_PLANE', 180),
  entitlement: secs('DATUM_TIMEOUT_ENTITLEMENT', 180),
  networkReady: secs('DATUM_TIMEOUT_NETWORK_READY', 300),
  workload: secs('DATUM_TIMEOUT_WORKLOAD_AVAILABLE', 900),
  instance: secs('DATUM_TIMEOUT_INSTANCE_READY', 900),
  networkContext: secs('DATUM_TIMEOUT_NETWORK_CONTEXT', 600),
  teardown: secs('DATUM_TIMEOUT_TEARDOWN', 600),
};

const SKIP_TEARDOWN = truthy(__ENV.DATUM_SKIP_TEARDOWN, false);
// Delete anything tagged with DATUM_RUN_ID and exit. Recovers leftovers from a
// hard-aborted run without deploying anything new.
const SWEEP_ONLY = truthy(__ENV.DATUM_SWEEP_ONLY, false);

const RUN_LABEL = 'test.datumapis.com/k6-run-id';
const OWNER_LABEL = 'app.kubernetes.io/managed-by';
const OWNER_VALUE = 'k6-consumer-journey';

// ─── Metrics ────────────────────────────────────────────────────────────────
//
// Every state transition the platform is supposed to make gets its own trend,
// so a run answers "how long does Datum take to do X" and not just "did the
// API return 200".

const tProjectReady = new Trend('datum_project_ready', true);
const tControlPlane = new Trend('datum_project_control_plane_ready', true);
const tEntitlement = new Trend('datum_entitlement_active', true);
const tNetworkReady = new Trend('datum_network_ready', true);
const tNetworkContext = new Trend('datum_network_context_ready', true);
const tWorkloadAvailable = new Trend('datum_workload_available', true);
const tDeploymentAvailable = new Trend('datum_workload_deployment_available', true);
const tFirstInstance = new Trend('datum_first_instance_observed', true);
const tInstanceReady = new Trend('datum_instance_ready', true);
const tInstanceAddress = new Trend('datum_instance_address_assigned', true);
const tWorkloadDeleted = new Trend('datum_workload_teardown', true);
const tNetworkDeleted = new Trend('datum_network_teardown', true);
const tProjectDeleted = new Trend('datum_project_teardown', true);
const tJourney = new Trend('datum_journey_total', true);

const rStage = new Rate('datum_stage_success');
const cGatedEntitlement = new Counter('datum_entitlement_gated');
// Non-zero means nso#401 reproduced: a NetworkContext survived the deletion of
// everything that referenced it.
const cOrphanContexts = new Counter('datum_orphaned_network_contexts');
const cOrphanBindings = new Counter('datum_orphaned_network_bindings');
const cLeaked = new Counter('datum_leaked_resources');
// An instance the platform gave up on. Tracked separately from a plain timeout
// because it is a definite platform answer, not a slow one.
const cInstanceFailed = new Counter('datum_instance_failed');

// ─── k6 options ─────────────────────────────────────────────────────────────

export const options = {
  scenarios: {
    journey: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      maxDuration: __ENV.DATUM_MAX_DURATION || '45m',
    },
  },
  thresholds: {
    checks: ['rate==1'],
    datum_stage_success: ['rate==1'],
    datum_leaked_resources: ['count==0'],
    datum_orphaned_network_contexts: ['count==0'],
  },
  insecureSkipTLSVerify: truthy(__ENV.K6_INSECURE_SKIP_TLS_VERIFY, false),
};

// Expected statuses cover the polling 404s and the idempotent-create 409s so
// they do not pollute http_req_failed.
http.setResponseCallback(http.expectedStatuses(200, 201, 202, 204, 404, 409));

// ─── Small helpers ──────────────────────────────────────────────────────────

function truthy(v, dflt) {
  if (v === undefined || v === '') return dflt;
  return ['1', 'true', 'yes', 'on'].indexOf(String(v).toLowerCase()) !== -1;
}

function intEnv(name, dflt) {
  const v = parseInt(__ENV[name], 10);
  return isNaN(v) ? dflt : v;
}

function secs(name, dflt) {
  return intEnv(name, dflt) * 1000;
}

function log(msg) {
  console.log(`[${new Date().toISOString()}] ${msg}`);
}

// stage records a pass/fail for a named journey step against both the check
// output and the datum_stage_success rate, so a threshold can gate on it.
function stage(name, ok, detail) {
  rStage.add(ok ? 1 : 0, { stage: name });
  check(null, { [name]: () => ok });
  log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`);
  return ok;
}

function condition(obj, type) {
  const conds = (obj && obj.status && obj.status.conditions) || [];
  for (const c of conds) if (c.type === type) return c;
  return null;
}

function conditionTrue(obj, type) {
  const c = condition(obj, type);
  return !!c && c.status === 'True';
}

function conditionSummary(obj) {
  const conds = (obj && obj.status && obj.status.conditions) || [];
  return conds.map((c) => `${c.type}=${c.status}(${c.reason})`).join(' ');
}

// ─── API client ─────────────────────────────────────────────────────────────
//
// Datum exposes a control plane per scope, addressed by a path prefix on the
// single public endpoint. Everything below is plain Kubernetes-style REST
// underneath that prefix.

function orgBase() {
  return `${ENDPOINT}/apis/resourcemanager.miloapis.com/v1alpha1/organizations/${ORG}/control-plane`;
}

function projectBase(project) {
  return `${ENDPOINT}/apis/resourcemanager.miloapis.com/v1alpha1/projects/${project}/control-plane`;
}

function headers() {
  return {
    Authorization: `Bearer ${TOKEN}`,
    'Content-Type': 'application/json',
    Accept: 'application/json',
  };
}

function apiGet(url, tag) {
  return http.get(url, { headers: headers(), tags: { datum_op: tag || 'get' } });
}

function apiPost(url, body, tag) {
  return http.post(url, JSON.stringify(body), { headers: headers(), tags: { datum_op: tag || 'create' } });
}

function apiDelete(url, tag) {
  return http.del(url, null, { headers: headers(), tags: { datum_op: tag || 'delete' } });
}

function body(res) {
  try {
    return res.json();
  } catch (e) {
    return null;
  }
}

// resourceURL builds a collection or single-object URL for a namespaced or
// cluster-scoped resource inside a control plane.
function resourceURL(base, group, version, plural, opts) {
  const o = opts || {};
  const prefix = group ? `${base}/apis/${group}/${version}` : `${base}/api/${version}`;
  let url = o.namespace ? `${prefix}/namespaces/${o.namespace}/${plural}` : `${prefix}/${plural}`;
  if (o.name) url += `/${o.name}`;
  const q = [];
  if (o.labelSelector) q.push(`labelSelector=${encodeURIComponent(o.labelSelector)}`);
  if (q.length) url += `?${q.join('&')}`;
  return url;
}

// ─── Bounded waits ──────────────────────────────────────────────────────────
//
// Every readiness wait is a predicate polled to a deadline. Nothing in this
// suite sleeps for a fixed duration and hopes.

function waitFor(name, timeoutMs, probe) {
  const start = Date.now();
  const deadline = start + timeoutMs;
  let last = '';
  for (;;) {
    const r = probe();
    if (r && r.done) {
      return { ok: true, elapsed: Date.now() - start, detail: r.detail, value: r.value };
    }
    last = (r && r.detail) || last;
    if (Date.now() >= deadline) {
      return { ok: false, elapsed: Date.now() - start, detail: `timed out after ${Math.round(timeoutMs / 1000)}s; last: ${last}` };
    }
    sleep(POLL_MS / 1000);
  }
}

// ─── Naming ─────────────────────────────────────────────────────────────────

function newRunID() {
  if (__ENV.DATUM_RUN_ID) return __ENV.DATUM_RUN_ID.toLowerCase();
  const t = Date.now().toString(36);
  const r = Math.random().toString(36).slice(2, 6);
  return `${t}${r}`.toLowerCase();
}

function names(runID) {
  return {
    runID,
    project: CREATE_PROJECT ? `${PROJECT_PREFIX}-${runID}` : PROJECT,
    network: `k6-${runID}-net`,
    workload: `k6-${runID}-wl`,
  };
}

function runLabels(runID) {
  const l = {};
  l[OWNER_LABEL] = OWNER_VALUE;
  l[RUN_LABEL] = runID;
  return l;
}

// ─── Safety ─────────────────────────────────────────────────────────────────
//
// This suite runs against shared staging. It must never delete anything it did
// not create. Every delete goes through ownedByRun, which re-reads the object
// and refuses unless it carries this run's label.

function ownedByRun(obj, runID) {
  const l = (obj && obj.metadata && obj.metadata.labels) || {};
  return l[OWNER_LABEL] === OWNER_VALUE && l[RUN_LABEL] === runID;
}

function safeDelete(url, runID, what) {
  const cur = apiGet(url, 'preflight');
  if (cur.status === 404) return { deleted: false, reason: 'absent' };
  const obj = body(cur);
  if (!ownedByRun(obj, runID)) {
    log(`REFUSING to delete ${what}: not labelled for run ${runID}`);
    return { deleted: false, reason: 'not-owned' };
  }
  const res = apiDelete(url, 'delete');
  return { deleted: res.status === 200 || res.status === 202 || res.status === 404, reason: `http ${res.status}` };
}

// ─── Object builders ────────────────────────────────────────────────────────

function networkManifest(n) {
  return {
    apiVersion: 'networking.datumapis.com/v1alpha',
    kind: 'Network',
    metadata: { name: n.network, namespace: NAMESPACE, labels: runLabels(n.runID) },
    spec: {
      ipFamilies: [IP_FAMILY],
      ipam: { mode: 'Auto' },
      mtu: intEnv('DATUM_MTU', 1460),
    },
  };
}

function workloadManifest(n) {
  const sandbox = {
    containers: [{ name: 'app', image: IMAGE }],
  };
  if (PULL_SECRET) sandbox.imagePullSecrets = [{ name: PULL_SECRET }];

  return {
    apiVersion: 'compute.datumapis.com/v1alpha',
    kind: 'Workload',
    metadata: { name: n.workload, namespace: NAMESPACE, labels: runLabels(n.runID) },
    spec: {
      placements: [
        {
          name: PLACEMENT,
          cityCodes: [CITY_CODE],
          scaleSettings: { instanceManagementPolicy: 'OrderedReady', minReplicas: REPLICAS },
        },
      ],
      // Template labels land on the instance and its downstream pod, so keep
      // them minimal; the run id lives on the Workload itself.
      template: {
        metadata: { labels: { app: n.workload } },
        spec: {
          networkInterfaces: [
            {
              name: 'eth0',
              network: { name: n.network },
              ipFamilies: [IP_FAMILY],
              reclaimPolicy: 'Delete',
            },
          ],
          runtime: {
            resources: { instanceType: INSTANCE_TYPE },
            sandbox: sandbox,
          },
        },
      },
    },
  };
}

// ─── Journey stages ─────────────────────────────────────────────────────────

// ensureProject either creates a throwaway project or confirms the reused one
// is Ready, and in both cases waits for its control plane to start serving.
function ensureProject(n) {
  const url = resourceURL(orgBase(), 'resourcemanager.miloapis.com', 'v1alpha1', 'projects', { name: n.project });

  if (CREATE_PROJECT) {
    const res = apiPost(
      resourceURL(orgBase(), 'resourcemanager.miloapis.com', 'v1alpha1', 'projects'),
      {
        apiVersion: 'resourcemanager.miloapis.com/v1alpha1',
        kind: 'Project',
        metadata: {
          name: n.project,
          labels: runLabels(n.runID),
          annotations: { 'kubernetes.io/description': `k6 consumer journey run ${n.runID}` },
        },
        spec: {},
      },
      'create-project',
    );
    if (!stage('project created', res.status === 201 || res.status === 409, `http ${res.status}`)) {
      fail(`could not create project ${n.project}: ${res.body}`);
    }
  }

  const ready = waitFor('project Ready', T.projectReady, () => {
    const res = apiGet(url, 'get-project');
    if (res.status !== 200) return { done: false, detail: `http ${res.status}` };
    const p = body(res);
    return { done: conditionTrue(p, 'Ready'), detail: conditionSummary(p), value: p };
  });
  tProjectReady.add(ready.elapsed);
  if (!stage('project reports Ready', ready.ok, `${ready.elapsed}ms ${ready.detail}`)) {
    fail(`project ${n.project} never became Ready`);
  }

  // A Ready project is not necessarily a serving project — the per-project
  // control plane is provisioned separately.
  const cp = waitFor('project control plane serving', T.controlPlane, () => {
    const res = apiGet(
      resourceURL(projectBase(n.project), 'services.miloapis.com', 'v1alpha1', 'serviceentitlements'),
      'get-entitlements',
    );
    return { done: res.status === 200, detail: `http ${res.status}` };
  });
  tControlPlane.add(cp.elapsed);
  if (!stage('project control plane serving', cp.ok, `${cp.elapsed}ms ${cp.detail}`)) {
    fail(`control plane for ${n.project} never served`);
  }
}

// subscribe requests a service entitlement and waits for it to resolve. Returns
// the terminal state so the caller can decide whether the journey can continue.
function subscribe(project, service, timeoutMs, trend) {
  const collection = resourceURL(projectBase(project), 'services.miloapis.com', 'v1alpha1', 'serviceentitlements');
  const single = resourceURL(projectBase(project), 'services.miloapis.com', 'v1alpha1', 'serviceentitlements', { name: service });

  const existing = apiGet(single, 'get-entitlement');
  if (existing.status === 404) {
    const res = apiPost(
      collection,
      {
        apiVersion: 'services.miloapis.com/v1alpha1',
        kind: 'ServiceEntitlement',
        metadata: { name: service },
        spec: { serviceRef: { name: service }, requestMessage: 'k6 consumer deployment journey' },
      },
      'create-entitlement',
    );
    stage(`entitlement requested (${service})`, res.status === 201 || res.status === 409, `http ${res.status}`);
  } else {
    stage(`entitlement requested (${service})`, existing.status === 200, 'pre-existing');
  }

  // PendingApproval is terminal for an automated run: it needs a human.
  const w = waitFor(`entitlement ${service}`, timeoutMs, () => {
    const res = apiGet(single, 'get-entitlement');
    if (res.status !== 200) return { done: false, detail: `http ${res.status}` };
    const e = body(res);
    const phase = (e.status && e.status.phase) || '';
    const done = phase === 'Active' || phase === 'PendingApproval' || phase === 'Denied' || phase === 'Rejected';
    return { done, detail: `phase=${phase} ${conditionSummary(e)}`, value: e };
  });
  if (trend) trend.add(w.elapsed);

  const phase = (w.value && w.value.status && w.value.status.phase) || 'Unknown';
  const active = phase === 'Active' && conditionTrue(w.value, 'Ready');
  log(`entitlement ${service}: phase=${phase} after ${w.elapsed}ms`);
  return { phase, active, elapsed: w.elapsed, detail: w.detail };
}

function createNetwork(n) {
  const collection = resourceURL(projectBase(n.project), 'networking.datumapis.com', 'v1alpha', 'networks', { namespace: NAMESPACE });
  const single = resourceURL(projectBase(n.project), 'networking.datumapis.com', 'v1alpha', 'networks', { namespace: NAMESPACE, name: n.network });

  const res = apiPost(collection, networkManifest(n), 'create-network');
  if (!stage('network created', res.status === 201 || res.status === 409, `http ${res.status} ${res.status >= 400 ? res.body : ''}`)) {
    fail(`could not create network ${n.network}`);
  }

  const w = waitFor('network Ready', T.networkReady, () => {
    const g = apiGet(single, 'get-network');
    if (g.status !== 200) return { done: false, detail: `http ${g.status}` };
    const net = body(g);
    return { done: conditionTrue(net, 'Ready'), detail: conditionSummary(net), value: net };
  });
  tNetworkReady.add(w.elapsed);
  stage('network reports Ready', w.ok, `${w.elapsed}ms ${w.detail}`);

  if (IP_FAMILY === 'IPv6' || IP_FAMILY === 'IPv4') {
    const allocated = conditionTrue(w.value, 'IPAMAllocated');
    const prefix = ((w.value && w.value.status && w.value.status.ipam) || {})[IP_FAMILY === 'IPv6' ? 'ipv6Prefix' : 'ipv4Prefix'];
    stage('network allocated address space', allocated && !!prefix, prefix || conditionSummary(w.value));
  }
  return w.value;
}

function createWorkload(n) {
  const collection = resourceURL(projectBase(n.project), 'compute.datumapis.com', 'v1alpha', 'workloads', { namespace: NAMESPACE });
  const single = resourceURL(projectBase(n.project), 'compute.datumapis.com', 'v1alpha', 'workloads', { namespace: NAMESPACE, name: n.workload });

  const res = apiPost(collection, workloadManifest(n), 'create-workload');
  if (!stage('workload created', res.status === 201 || res.status === 409, `http ${res.status} ${res.status >= 400 ? res.body : ''}`)) {
    fail(`could not create workload ${n.workload}`);
  }

  const w = waitFor('workload Available', T.workload, () => {
    const g = apiGet(single, 'get-workload');
    if (g.status !== 200) return { done: false, detail: `http ${g.status}` };
    const wl = body(g);
    return { done: conditionTrue(wl, 'Available'), detail: conditionSummary(wl), value: wl };
  });
  tWorkloadAvailable.add(w.elapsed);
  stage('workload becomes Available', w.ok, `${w.elapsed}ms ${w.detail}`);

  const st = (w.value && w.value.status) || {};
  stage(
    'workload reports the requested replica count ready',
    st.readyReplicas === REPLICAS && st.desiredReplicas === REPLICAS,
    `desired=${st.desiredReplicas} ready=${st.readyReplicas} current=${st.currentReplicas}`,
  );
  return w.value;
}

// awaitDeployment finds the WorkloadDeployment the placement produced and
// confirms the control plane actually landed it in a location.
function awaitDeployment(n, workload) {
  const uid = workload && workload.metadata && workload.metadata.uid;
  const selector = `compute.datumapis.com/workload-uid=${uid}`;
  const list = resourceURL(projectBase(n.project), 'compute.datumapis.com', 'v1alpha', 'workloaddeployments', {
    namespace: NAMESPACE,
    labelSelector: selector,
  });

  const w = waitFor('workload deployment Available', T.workload, () => {
    const g = apiGet(list, 'list-deployments');
    if (g.status !== 200) return { done: false, detail: `http ${g.status}` };
    const items = (body(g) || {}).items || [];
    if (!items.length) return { done: false, detail: 'no deployments yet' };
    const d = items[0];
    return { done: conditionTrue(d, 'Available'), detail: `${d.metadata.name} ${conditionSummary(d)}`, value: d };
  });
  tDeploymentAvailable.add(w.elapsed);
  stage('placement produces an Available WorkloadDeployment', w.ok, `${w.elapsed}ms ${w.detail}`);

  const d = w.value || {};
  const location = ((d.status || {}).location || {}).name || '';
  stage('deployment is scheduled to a location', !!location, `cityCode=${CITY_CODE} location=${location || '<none>'}`);
  stage('deployment reports replicas ready', ((d.status || {}).readyReplicas || 0) === REPLICAS, `ready=${(d.status || {}).readyReplicas}`);
  return { deployment: d, location };
}

// awaitNetworkContext proves the network actually reached the location the
// workload landed in — the "network reaches its location" assertion.
function awaitNetworkContext(n, location) {
  if (!location) return null;
  const list = resourceURL(projectBase(n.project), 'networking.datumapis.com', 'v1alpha', 'networkcontexts', { namespace: NAMESPACE });

  const w = waitFor('network context Ready', T.networkContext, () => {
    const g = apiGet(list, 'list-network-contexts');
    if (g.status !== 200) return { done: false, detail: `http ${g.status}` };
    const items = ((body(g) || {}).items || []).filter(
      (c) => ((c.spec || {}).network || {}).name === n.network && ((c.spec || {}).location || {}).name === location,
    );
    if (!items.length) return { done: false, detail: `no context for ${n.network}@${location}` };
    const c = items[0];
    return { done: conditionTrue(c, 'Ready'), detail: `${c.metadata.name} ${conditionSummary(c)}`, value: c };
  });
  tNetworkContext.add(w.elapsed);
  stage('network reaches the deployment location', w.ok, `${w.elapsed}ms ${w.detail}`);
  return w.value;
}

function awaitInstances(n) {
  const selector = `compute.datumapis.com/workload-name=${n.workload}`;
  const list = resourceURL(projectBase(n.project), 'compute.datumapis.com', 'v1alpha', 'instances', {
    namespace: NAMESPACE,
    labelSelector: selector,
  });

  const first = waitFor('first instance observed', T.instance, () => {
    const g = apiGet(list, 'list-instances');
    if (g.status !== 200) return { done: false, detail: `http ${g.status}` };
    const items = (body(g) || {}).items || [];
    return { done: items.length > 0, detail: `${items.length} instance(s)`, value: items };
  });
  tFirstInstance.add(first.elapsed);
  stage('workload materialises instances', first.ok, `${first.elapsed}ms ${first.detail}`);

  // An instance can report Ready=False/reason=Failed and then recover, so the
  // wait keeps polling to its deadline and only records the failure.
  let sawFailure = false;
  const ready = waitFor('instances Ready', T.instance, () => {
    const g = apiGet(list, 'list-instances');
    if (g.status !== 200) return { done: false, detail: `http ${g.status}` };
    const items = (body(g) || {}).items || [];
    const readyCount = items.filter((i) => conditionTrue(i, 'Ready')).length;
    for (const i of items) {
      const c = condition(i, 'Ready');
      if (c && c.status === 'False' && c.reason === 'Failed' && !sawFailure) {
        sawFailure = true;
        cInstanceFailed.add(1, { workload: n.workload });
        log(`instance ${i.metadata.name} entered Failed: "${c.message}"`);
      }
    }
    return {
      done: items.length >= REPLICAS && readyCount >= REPLICAS,
      detail: `${readyCount}/${items.length} ready; ${items.map((i) => conditionSummary(i)).join(' | ')}`,
      value: items,
    };
  });
  tInstanceReady.add(ready.elapsed);
  stage('every instance reports Ready', ready.ok, `${ready.elapsed}ms ${ready.detail}`);

  const items = ready.value || first.value || [];
  for (const i of items) {
    const nm = i.metadata.name;
    stage(`instance ${nm} is Available`, conditionTrue(i, 'Available'), conditionSummary(i));
    stage(`instance ${nm} is Programmed`, conditionTrue(i, 'Programmed'), conditionSummary(i));
    stage(`instance ${nm} was granted quota`, conditionTrue(i, 'QuotaGranted'), conditionSummary(i));
  }

  // An instance is only genuinely usable once the platform has handed it an
  // address on the requested family.
  const addr = waitFor('instance address assigned', T.instance, () => {
    const g = apiGet(list, 'list-instances');
    if (g.status !== 200) return { done: false, detail: `http ${g.status}` };
    const got = [];
    for (const i of (body(g) || {}).items || []) {
      for (const ni of i.status && i.status.networkInterfaces ? i.status.networkInterfaces : []) {
        for (const a of ni.addresses || []) if (a.family === IP_FAMILY && a.address) got.push(`${i.metadata.name}:${a.address}`);
      }
    }
    return { done: got.length >= REPLICAS, detail: got.join(', ') || 'no addresses yet', value: got };
  });
  tInstanceAddress.add(addr.elapsed);
  stage(`instances receive an ${IP_FAMILY} address`, addr.ok, `${addr.elapsed}ms ${addr.detail}`);

  return items;
}

// ─── Teardown ───────────────────────────────────────────────────────────────
//
// Deleting a Workload garbage-collects the NetworkContext for its
// (network, location) pair, and a NetworkBinding that already resolved to that
// context is never reconciled again — datum-cloud/network-services-operator#401.
// Each run therefore owns its own network, and teardown deletes the workload
// first, drains its children, then removes the network, then checks that no
// context or binding survived. A survivor is reported, not swallowed.

function teardown_(n, location) {
  if (SKIP_TEARDOWN) {
    log(`DATUM_SKIP_TEARDOWN set — leaving run ${n.runID} in place`);
    return;
  }
  const wlURL = resourceURL(projectBase(n.project), 'compute.datumapis.com', 'v1alpha', 'workloads', { namespace: NAMESPACE, name: n.workload });
  const netURL = resourceURL(projectBase(n.project), 'networking.datumapis.com', 'v1alpha', 'networks', { namespace: NAMESPACE, name: n.network });

  // 1. Workload first, so its deployments and instances drain before the
  //    network they attach to disappears.
  safeDelete(wlURL, n.runID, `workload ${n.workload}`);
  const wlGone = waitFor('workload deleted', T.teardown, () => {
    const g = apiGet(wlURL, 'get-workload');
    return { done: g.status === 404, detail: `http ${g.status}` };
  });
  tWorkloadDeleted.add(wlGone.elapsed);
  stage('workload deletes cleanly', wlGone.ok, `${wlGone.elapsed}ms ${wlGone.detail}`);
  if (!wlGone.ok) cLeaked.add(1, { kind: 'Workload' });

  const childrenGone = waitFor('workload children deleted', T.teardown, () => {
    const inst = apiGet(
      resourceURL(projectBase(n.project), 'compute.datumapis.com', 'v1alpha', 'instances', {
        namespace: NAMESPACE,
        labelSelector: `compute.datumapis.com/workload-name=${n.workload}`,
      }),
      'list-instances',
    );
    const items = inst.status === 200 ? ((body(inst) || {}).items || []) : [];
    return { done: items.length === 0, detail: `${items.length} instance(s) remain` };
  });
  stage('instances are garbage collected', childrenGone.ok, `${childrenGone.elapsed}ms ${childrenGone.detail}`);
  if (!childrenGone.ok) cLeaked.add(1, { kind: 'Instance' });

  // 2. Then the network this run created. Nothing shared is ever touched.
  safeDelete(netURL, n.runID, `network ${n.network}`);
  const netGone = waitFor('network deleted', T.teardown, () => {
    const g = apiGet(netURL, 'get-network');
    return { done: g.status === 404, detail: `http ${g.status}` };
  });
  tNetworkDeleted.add(netGone.elapsed);
  stage('network deletes cleanly', netGone.ok, `${netGone.elapsed}ms ${netGone.detail}`);
  if (!netGone.ok) cLeaked.add(1, { kind: 'Network' });

  // 3. Verify the derived networking objects actually went away. This is the
  //    nso#401 detector.
  const ctxRes = apiGet(
    resourceURL(projectBase(n.project), 'networking.datumapis.com', 'v1alpha', 'networkcontexts', { namespace: NAMESPACE }),
    'list-network-contexts',
  );
  const strandedCtx = ctxRes.status === 200
    ? ((body(ctxRes) || {}).items || []).filter((c) => ((c.spec || {}).network || {}).name === n.network)
    : [];
  cOrphanContexts.add(strandedCtx.length);
  stage(
    'no NetworkContext survives the run (nso#401)',
    strandedCtx.length === 0,
    strandedCtx.map((c) => c.metadata.name).join(', ') || `network=${n.network} location=${location || '?'}`,
  );

  const bindRes = apiGet(
    resourceURL(projectBase(n.project), 'networking.datumapis.com', 'v1alpha', 'networkbindings', { namespace: NAMESPACE }),
    'list-network-bindings',
  );
  const strandedBind = bindRes.status === 200
    ? ((body(bindRes) || {}).items || []).filter((b) => ((b.spec || {}).network || {}).name === n.network)
    : [];
  cOrphanBindings.add(strandedBind.length);
  stage('no NetworkBinding survives the run', strandedBind.length === 0, strandedBind.map((b) => b.metadata.name).join(', ') || 'none');

  // 4. Throwaway projects go last, and only ones this run created.
  if (CREATE_PROJECT) {
    const projURL = resourceURL(orgBase(), 'resourcemanager.miloapis.com', 'v1alpha1', 'projects', { name: n.project });
    safeDelete(projURL, n.runID, `project ${n.project}`);
    const gone = waitFor('project deleted', T.teardown, () => {
      const g = apiGet(projURL, 'get-project');
      return { done: g.status === 404, detail: `http ${g.status}` };
    });
    tProjectDeleted.add(gone.elapsed);
    stage('project deletes cleanly', gone.ok, `${gone.elapsed}ms ${gone.detail}`);
    if (!gone.ok) cLeaked.add(1, { kind: 'Project' });
  }
}

// sweep is the belt-and-braces pass: delete anything still carrying this run's
// label, wherever it is. Used by teardown() and by DATUM_SWEEP_ONLY.
function sweep(n) {
  const selector = `${RUN_LABEL}=${n.runID}`;
  const targets = [
    ['compute.datumapis.com', 'v1alpha', 'workloads', true],
    ['networking.datumapis.com', 'v1alpha', 'networks', true],
  ];
  let removed = 0;
  const projectExists = apiGet(
    resourceURL(orgBase(), 'resourcemanager.miloapis.com', 'v1alpha1', 'projects', { name: n.project }),
    'get-project',
  ).status === 200;
  if (!projectExists) return removed;

  for (const [g, v, plural] of targets) {
    const res = apiGet(
      resourceURL(projectBase(n.project), g, v, plural, { namespace: NAMESPACE, labelSelector: selector }),
      'sweep-list',
    );
    if (res.status !== 200) continue;
    for (const item of (body(res) || {}).items || []) {
      const url = resourceURL(projectBase(n.project), g, v, plural, { namespace: NAMESPACE, name: item.metadata.name });
      const r = safeDelete(url, n.runID, `${plural}/${item.metadata.name}`);
      if (r.deleted) {
        removed++;
        log(`swept leftover ${plural}/${item.metadata.name}`);
      }
    }
  }
  if (CREATE_PROJECT && projectExists) {
    const projURL = resourceURL(orgBase(), 'resourcemanager.miloapis.com', 'v1alpha1', 'projects', { name: n.project });
    if (safeDelete(projURL, n.runID, `project ${n.project}`).deleted) removed++;
  }
  return removed;
}

// ─── Lifecycle ──────────────────────────────────────────────────────────────

export function setup() {
  if (!TOKEN) fail('DATUM_TOKEN is required (locally: DATUM_TOKEN=$(datumctl auth get-token))');
  const runID = newRunID();
  const n = names(runID);
  log(`run id ${runID} — endpoint=${ENDPOINT} org=${ORG} project=${n.project} createProject=${CREATE_PROJECT}`);
  log(`resources: network=${n.network} workload=${n.workload} namespace=${NAMESPACE} cityCode=${CITY_CODE}`);
  return n;
}

export default function (n) {
  if (SWEEP_ONLY) {
    log(`sweep-only for run ${n.runID}: removed ${sweep(n)} resource(s)`);
    return;
  }

  const started = Date.now();
  let location = '';
  try {
    ensureProject(n);

    // Networking is self-service, so a fresh project can subscribe to it.
    const net = subscribe(n.project, NETWORKING_SERVICE, T.entitlement);
    stage('networking subscription is Active', net.active, `phase=${net.phase}`);

    // Compute is GatedByProvider. In a pre-approved project this returns
    // Active immediately; in a fresh one it parks in PendingApproval forever.
    const cmp = subscribe(n.project, COMPUTE_SERVICE, T.entitlement, tEntitlement);
    if (!cmp.active) {
      cGatedEntitlement.add(1, { phase: cmp.phase });
      if (REQUIRE_COMPUTE) {
        stage('compute subscription is Active', false, `phase=${cmp.phase} — ${cmp.detail}`);
        return;
      }
      log(`compute entitlement is ${cmp.phase}; provider approval is a manual step, stopping before deploy`);
      stage('compute subscription resolves to a known state', cmp.phase !== 'Unknown', `phase=${cmp.phase}`);
      return;
    }
    stage('compute subscription is Active', true, `phase=${cmp.phase}`);

    createNetwork(n);
    const workload = createWorkload(n);
    const d = awaitDeployment(n, workload);
    location = d.location;
    awaitNetworkContext(n, location);
    awaitInstances(n);
    tJourney.add(Date.now() - started);
  } finally {
    // Runs even when a stage failed or fail() unwound the iteration.
    teardown_(n, location);
  }
}

export function teardown(n) {
  if (SKIP_TEARDOWN || SWEEP_ONLY) return;
  const removed = sweep(n);
  if (removed > 0) log(`final sweep removed ${removed} leftover resource(s) for run ${n.runID}`);
}

export function handleSummary(data) {
  return {
    stdout: summarise(data),
    [__ENV.DATUM_SUMMARY_FILE || 'summary.json']: JSON.stringify(data, null, 2),
  };
}

function summarise(data) {
  const lines = ['', 'Datum consumer deployment journey', '─'.repeat(64)];
  const order = [
    'datum_project_ready',
    'datum_project_control_plane_ready',
    'datum_entitlement_active',
    'datum_network_ready',
    'datum_workload_available',
    'datum_workload_deployment_available',
    'datum_network_context_ready',
    'datum_first_instance_observed',
    'datum_instance_ready',
    'datum_instance_address_assigned',
    'datum_workload_teardown',
    'datum_network_teardown',
    'datum_project_teardown',
    'datum_journey_total',
  ];
  for (const k of order) {
    const m = data.metrics[k];
    if (m && m.values && m.values.avg !== undefined && m.values.count !== 0) {
      lines.push(`${k.padEnd(40)} ${(m.values.avg / 1000).toFixed(1)}s`);
    }
  }
  const checks = data.metrics.checks;
  if (checks) {
    lines.push('─'.repeat(64));
    lines.push(`checks passed: ${checks.values.passes}/${checks.values.passes + checks.values.fails}`);
  }
  lines.push('');
  return lines.join('\n');
}
