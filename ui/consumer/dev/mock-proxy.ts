/**
 * Dev-server stand-in for the portal's authenticated proxy and host endpoints.
 *
 * The standalone harness has no cloud-portal behind it, so every data call the
 * plugin makes (`/api/proxy/.../control-plane/...`, `/api/prometheus`,
 * `/api/plugins`) is answered here from the JSON fixtures in `dev/fixtures/`.
 * Fixtures keep the exact shape the real apiserver returns, so the adapters
 * run unchanged. Unknown paths get the Kubernetes 404 Status the proxy would
 * relay, which keeps the plugin's "optional resource" branches honest.
 *
 * Set HARNESS_UPSTREAM to a `datumctl api proxy` URL to forward every
 * `/api/proxy/...` call to a real Datum API instead of the fixtures; the
 * host-only endpoints (`/api/plugins`, `/api/prometheus`) stay mocked because
 * datumctl has no equivalent for them.
 *
 * Registered only for `vite` (serve); `vite build` never sees this module.
 */
import { readFileSync } from 'node:fs';
import type { IncomingMessage, ServerResponse } from 'node:http';
import { resolve } from 'node:path';
import type { Plugin } from 'vite';

const FIXTURES_DIR = resolve(__dirname, 'fixtures');
const UPSTREAM = process.env.HARNESS_UPSTREAM?.replace(/\/$/, '');

const PROXY_BASE =
  /^\/api\/proxy\/apis\/resourcemanager\.miloapis\.com\/v1alpha1\/projects\/([^/]+)\/control-plane(\/.*)$/;
const COMPUTE_NS = '/apis/compute.datumapis.com/v1alpha/namespaces/default';
const NETWORKING_NS = '/apis/networking.datumapis.com/v1alpha/namespaces/default';

interface KubeObject {
  metadata?: { name?: string; labels?: Record<string, string> };
}
interface KubeList {
  metadata?: { continue?: string; resourceVersion?: string };
  items?: KubeObject[];
}

function fixture<T = unknown>(file: string): T {
  // Read per request so fixture edits show up without restarting the server.
  return JSON.parse(readFileSync(resolve(FIXTURES_DIR, file), 'utf8')) as T;
}

function send(res: ServerResponse, status: number, body: unknown) {
  res.statusCode = status;
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify(body));
}

/** The Status object the apiserver returns for a missing resource. */
function notFound(res: ServerResponse, resource: string, name?: string) {
  send(res, 404, {
    kind: 'Status',
    apiVersion: 'v1',
    metadata: {},
    status: 'Failure',
    message: name
      ? `${resource} "${name}" not found`
      : `the server could not find the requested resource (${resource})`,
    reason: 'NotFound',
    details: name ? { name, kind: resource } : undefined,
    code: 404,
  });
}

/**
 * Minimal Kubernetes label selector: comma-separated `k=v`, `k==v`, `k!=v`,
 * `k in (a,b)`, `k notin (a,b)`, `k`, `!k`. Enough to honour the
 * `workload-name=<wl>` filter the plugin sends for a workload's instances.
 */
function matchesSelector(labels: Record<string, string>, selector: string): boolean {
  const terms = selector
    .split(',')
    .map((t) => t.trim())
    .filter(Boolean);
  return terms.every((term) => {
    let m: RegExpMatchArray | null;
    if ((m = term.match(/^([^!=\s]+)\s*(==|=|!=)\s*(.*)$/))) {
      const [, key, op, value] = m;
      return op === '!=' ? labels[key] !== value : labels[key] === value;
    }
    if ((m = term.match(/^([^\s]+)\s+(in|notin)\s+\((.*)\)$/))) {
      const [, key, op, list] = m;
      const values = list.split(',').map((v) => v.trim());
      const present = key in labels && values.includes(labels[key]);
      return op === 'in' ? present : !present;
    }
    if (term.startsWith('!')) return !(term.slice(1) in labels);
    return term in labels;
  });
}

function listFrom(file: string, search: URLSearchParams): KubeList {
  const list = fixture<KubeList>(file);
  const selector = search.get('labelSelector');
  const items = selector
    ? (list.items ?? []).filter((item) => matchesSelector(item.metadata?.labels ?? {}, selector))
    : (list.items ?? []);
  // Pagination tokens are accepted but a single page always holds everything.
  return { ...list, metadata: { ...list.metadata, continue: '' }, items };
}

function itemFrom(file: string, name: string): KubeObject | undefined {
  return fixture<KubeList>(file).items?.find((item) => item.metadata?.name === name);
}

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((done) => {
    let data = '';
    req.on('data', (chunk: Buffer) => (data += chunk.toString()));
    req.on('end', () => done(data));
  });
}

/** Routes a control-plane path (after `/control-plane`) to a fixture response. */
function handleControlPlane(
  method: string,
  path: string,
  search: URLSearchParams,
  res: ServerResponse,
): boolean {
  const collection = (prefix: string, file: string, resource: string): boolean => {
    if (path === prefix) {
      send(res, 200, listFrom(file, search));
      return true;
    }
    const named = path.startsWith(`${prefix}/`) ? path.slice(prefix.length + 1) : undefined;
    if (named && !named.includes('/')) {
      const item = itemFrom(file, named);
      if (item) send(res, 200, item);
      else notFound(res, resource, named);
      return true;
    }
    return false;
  };

  if (method === 'GET') {
    if (collection(`${COMPUTE_NS}/workloads`, 'workloads.json', 'workloads.compute.datumapis.com'))
      return true;
    if (collection(`${COMPUTE_NS}/instances`, 'instances.json', 'instances.compute.datumapis.com'))
      return true;
    if (
      collection(
        '/apis/compute.datumapis.com/v1alpha/runtimeclasses',
        'runtimeclasses.json',
        'runtimeclasses.compute.datumapis.com',
      )
    )
      return true;
    if (
      collection(`${NETWORKING_NS}/networks`, 'networks.json', 'networks.networking.datumapis.com')
    )
      return true;
    if (
      collection(
        `${NETWORKING_NS}/httpproxies`,
        'httpproxies.json',
        'httpproxies.networking.datumapis.com',
      )
    )
      return true;
    if (
      collection(
        `${NETWORKING_NS}/networkservices`,
        'networkservices.json',
        'networkservices.networking.datumapis.com',
      )
    )
      return true;
    if (
      collection(
        '/apis/locations.miloapis.com/v1alpha1/locations',
        'locations.json',
        'locations.locations.miloapis.com',
      )
    )
      return true;
    if (
      collection(
        '/apis/quota.miloapis.com/v1alpha1/namespaces/milo-system/allowancebuckets',
        'allowancebuckets.json',
        'allowancebuckets.quota.miloapis.com',
      )
    )
      return true;

    if (path === '/apis/services.miloapis.com/v1alpha1/serviceentitlements/compute') {
      send(res, 200, fixture('serviceentitlement-compute.json'));
      return true;
    }
    if (path === '/apis/services.miloapis.com/v1alpha1/serviceentitlements') {
      send(res, 200, {
        apiVersion: 'services.miloapis.com/v1alpha1',
        kind: 'ServiceEntitlementList',
        metadata: {},
        items: [fixture('serviceentitlement-compute.json')],
      });
      return true;
    }
    if (path === '/apis/o11y.miloapis.com/v1alpha1/logs/loki/api/v1/query_range') {
      // Empty-but-valid Loki answer: the log tables render their empty state.
      send(res, 200, {
        status: 'success',
        data: { resultType: 'streams', result: [] },
      });
      return true;
    }
    if (path.startsWith('/apis/events.k8s.io/v1/namespaces/default/events')) {
      send(res, 200, {
        apiVersion: 'events.k8s.io/v1',
        kind: 'EventList',
        metadata: {},
        items: [],
      });
      return true;
    }
    if (path.startsWith(`${COMPUTE_NS}/instanceconsolesessions`)) {
      send(res, 200, {
        apiVersion: 'compute.datumapis.com/v1alpha',
        kind: 'InstanceConsoleSessionList',
        metadata: {},
        items: [],
      });
      return true;
    }
  }

  if (method === 'POST' && path === '/apis/authorization.k8s.io/v1/selfsubjectaccessreviews') {
    // The demo user may do everything, so delete/shell affordances show.
    send(res, 201, {
      apiVersion: 'authorization.k8s.io/v1',
      kind: 'SelfSubjectAccessReview',
      metadata: {},
      status: { allowed: true },
    });
    return true;
  }

  return false;
}

/** Relays one request to the datumctl proxy, which adds the session credentials. */
async function forward(req: IncomingMessage, res: ServerResponse, target: string) {
  const method = req.method ?? 'GET';
  const body = method === 'GET' || method === 'HEAD' ? undefined : await readBody(req);
  try {
    const upstream = await fetch(target, {
      method,
      headers: {
        'Content-Type': req.headers['content-type'] ?? 'application/json',
      },
      body,
    });
    res.statusCode = upstream.status;
    res.setHeader('Content-Type', upstream.headers.get('content-type') ?? 'application/json');
    res.end(Buffer.from(await upstream.arrayBuffer()));
  } catch (error) {
    send(res, 502, {
      error: 'upstream unreachable',
      target,
      detail: String(error),
    });
  }
}

export function mockProxy(): Plugin {
  return {
    name: 'compute-harness-mock-proxy',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use(async (req, res, next) => {
        const url = new URL(req.url ?? '/', 'http://localhost');
        const method = req.method ?? 'GET';

        if (url.pathname === '/api/plugins') {
          send(res, 200, fixture('plugins.json'));
          return;
        }

        if (url.pathname === '/api/prometheus' && method === 'POST') {
          const body = JSON.parse((await readBody(req)) || '{}') as {
            type?: string;
            timeRange?: { start?: number; end?: number };
          };
          const nowSec = Math.floor(Date.now() / 1000);
          const start = body.timeRange?.start ?? nowSec - 3600;
          const end = body.timeRange?.end ?? nowSec;
          // Empty-but-valid results in the portal's /api/prometheus envelope, so
          // charts and KPI tiles show "no data" rather than an error.
          if (body.type === 'chart') {
            send(res, 200, {
              success: true,
              data: { series: [], timeRange: { start, end } },
            });
          } else if (body.type === 'card') {
            send(res, 200, {
              success: true,
              data: { value: 0, formattedValue: '—', timestamp: end },
            });
          } else if (body.type === 'labels') {
            send(res, 200, { success: true, data: [] });
          } else {
            send(res, 200, { success: true, data: null });
          }
          return;
        }

        if (UPSTREAM && url.pathname.startsWith('/api/proxy/')) {
          await forward(
            req,
            res,
            `${UPSTREAM}${url.pathname.slice('/api/proxy'.length)}${url.search}`,
          );
          return;
        }

        const proxied = url.pathname.match(PROXY_BASE);
        if (proxied) {
          const [, projectId, path] = proxied;
          if (projectId !== 'demo-project') {
            send(res, 403, {
              kind: 'Status',
              apiVersion: 'v1',
              metadata: {},
              status: 'Failure',
              message: `projects.resourcemanager.miloapis.com "${projectId}" is forbidden`,
              reason: 'Forbidden',
              code: 403,
            });
            return;
          }
          if (handleControlPlane(method, path, url.searchParams, res)) return;
          notFound(res, path);
          return;
        }

        if (url.pathname.startsWith('/api/')) {
          send(res, 404, { error: 'Not found', path: url.pathname });
          return;
        }

        next();
      });
    },
  };
}
