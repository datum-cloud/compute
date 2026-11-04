import { afterEach, beforeEach, describe, expect, test } from 'vitest';
import { focusManager, MutationObserver, QueryClient, QueryObserver } from '@tanstack/react-query';
import {
  albRoutes,
  computeEntitlementQueryOptions,
  createDemoWorkloadMutationOptions,
  deleteWorkloadMutationOptions,
  PLUGIN_ID,
  workloadsQueryOptions,
  type ComputeEntitlement,
  type DeleteWorkloadInput,
  type RawHttpProxy,
  type RawNetworkService,
} from './api';

const PROJECT_ID = 'p-1';
const ENTITLEMENT_KEY = [PLUGIN_ID, 'compute-entitlement', PROJECT_ID];
const HOST_ACTIVE_ENTITLEMENTS_KEY = ['service-entitlements', 'active', PROJECT_ID];
const HOST_KEYS = [['http-proxies'], ['network-services'], ['compute-workloads'], ['domains']];
const POLL_INTERVAL_MS = 10_000;
const ENABLING_POLL_INTERVAL_MS = 2_000;

const realFetch = globalThis.fetch;
let fetchCalls: string[] = [];

function stubFetch(respond: (url: string, init?: RequestInit) => Response): void {
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    fetchCalls.push(`${init?.method ?? 'GET'} ${url}`);
    return respond(url, init);
  }) as typeof fetch;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

/** A client configured like cloud-portal's: 5-minute staleTime, focus refetch off. */
function hostQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { staleTime: 5 * 60 * 1000, refetchOnWindowFocus: false, retry: false },
    },
  });
}

/** Seeds a host key and returns a reader for whether it has been invalidated. */
function seedHostKey(client: QueryClient, queryKey: readonly unknown[]): () => boolean {
  client.setQueryData(queryKey, []);
  return () => client.getQueryState(queryKey)?.isInvalidated ?? false;
}

beforeEach(() => {
  fetchCalls = [];
});

afterEach(() => {
  globalThis.fetch = realFetch;
  focusManager.setFocused(undefined);
});

describe('workload polling', () => {
  test('workload queries refetch on window focus', async () => {
    stubFetch(() => json({ items: [] }));
    const client = hostQueryClient();
    const options = workloadsQueryOptions(PROJECT_ID);
    // The host's QueryClientProvider mounts the client, wiring it to focus events.
    client.mount();
    const unsubscribe = new QueryObserver(client, options).subscribe(() => {});
    try {
      await client.fetchQuery(options);
      // Data older than one poll interval: the tab was hidden, so polling paused.
      client.setQueryData(options.queryKey, [], { updatedAt: Date.now() - POLL_INTERVAL_MS - 1 });
      fetchCalls = [];

      focusManager.setFocused(false);
      focusManager.setFocused(true);
      await new Promise((resolve) => setTimeout(resolve, 0));

      expect(fetchCalls).toHaveLength(1);
    } finally {
      unsubscribe();
      client.unmount();
    }
  });
});

describe('compute entitlement', () => {
  test('the entitlement query polls fast while activating, slowly otherwise, and stops when Active', () => {
    const client = hostQueryClient();
    const options = computeEntitlementQueryOptions(PROJECT_ID);
    const query = client.getQueryCache().build(client, options);
    const interval = options.refetchInterval;
    if (typeof interval !== 'function') throw new Error('refetchInterval must depend on the phase');

    query.setData({ phase: 'PendingApproval' });
    expect(interval(query)).toBe(ENABLING_POLL_INTERVAL_MS);
    const phases: ComputeEntitlement['phase'][] = [null, 'Rejected'];
    for (const phase of phases) {
      query.setData({ phase });
      expect(interval(query)).toBe(POLL_INTERVAL_MS);
    }
    query.setData({ phase: 'Active' });
    expect(interval(query)).toBe(false);
  });

  test('becoming Active invalidates the host active-entitlements key', async () => {
    stubFetch(() => json({ status: { phase: 'Active' } }));
    const client = hostQueryClient();
    const options = computeEntitlementQueryOptions(PROJECT_ID);
    const hostInvalidated = seedHostKey(client, HOST_ACTIVE_ENTITLEMENTS_KEY);
    client.setQueryData<ComputeEntitlement>(ENTITLEMENT_KEY, { phase: 'PendingApproval' });

    await client.fetchQuery({ ...options, staleTime: 0 });

    expect(client.getQueryData<ComputeEntitlement>(ENTITLEMENT_KEY)?.phase).toBe('Active');
    expect(hostInvalidated()).toBe(true);
  });

  test('staying Active leaves the host active-entitlements key alone', async () => {
    stubFetch(() => json({ status: { phase: 'Active' } }));
    const client = hostQueryClient();
    const options = computeEntitlementQueryOptions(PROJECT_ID);
    const hostInvalidated = seedHostKey(client, HOST_ACTIVE_ENTITLEMENTS_KEY);
    client.setQueryData<ComputeEntitlement>(ENTITLEMENT_KEY, { phase: 'Active' });

    await client.fetchQuery({ ...options, staleTime: 0 });

    expect(hostInvalidated()).toBe(false);
  });
});

describe('host caches after writes', () => {
  test('demo create invalidates host roots and domains', async () => {
    stubFetch(() => json({}, 201));
    const client = hostQueryClient();
    const readers = HOST_KEYS.map((key) => seedHostKey(client, key));

    await new MutationObserver(client, createDemoWorkloadMutationOptions(PROJECT_ID)).mutate();

    expect(readers.map((read) => read())).toEqual(HOST_KEYS.map(() => true));
  });

  test('demo create joins the project network when there is exactly one', async () => {
    stubFetch((url, init) =>
      (init?.method ?? 'GET') === 'GET' && url.includes('/networks?')
        ? json({ items: [{ metadata: { name: 'default' } }] })
        : json({}, 201)
    );

    await new MutationObserver(hostQueryClient(), createDemoWorkloadMutationOptions(PROJECT_ID)).mutate();

    const networkPosts = fetchCalls.filter((call) => call.startsWith('POST') && call.endsWith('/networks'));
    expect(networkPosts).toEqual([]);
  });

  test('demo create makes its own network when the project has none or several', async () => {
    for (const items of [[], [{ metadata: { name: 'a' } }, { metadata: { name: 'b' } }]]) {
      fetchCalls = [];
      stubFetch((url, init) =>
        (init?.method ?? 'GET') === 'GET' && url.includes('/networks?')
          ? json({ items })
          : json({}, 201)
      );

      await new MutationObserver(hostQueryClient(), createDemoWorkloadMutationOptions(PROJECT_ID)).mutate();

      const networkPosts = fetchCalls.filter((call) => call.startsWith('POST') && call.endsWith('/networks'));
      expect(networkPosts).toHaveLength(1);
    }
  });

  test('delete invalidates domains', async () => {
    stubFetch(() => new Response(null, { status: 200 }));
    const client = hostQueryClient();
    const domainsInvalidated = seedHostKey(client, ['domains']);
    const input: DeleteWorkloadInput = {
      workloadName: 'web',
      albs: [],
      networks: [],
      related: { albs: [], networks: [], serviceProxies: {} },
      canDeleteNetworkService: false,
    };

    await new MutationObserver(client, deleteWorkloadMutationOptions(PROJECT_ID)).mutate(input);

    expect(domainsInvalidated()).toBe(true);
  });
});

describe('albRoutes', () => {
  const service: RawNetworkService = {
    metadata: { name: 'web' },
    spec: { ports: [{ name: 'http', port: 8080, protocol: 'TCP' }] },
  };
  const proxy = (rules: NonNullable<RawHttpProxy['spec']>['rules']): RawHttpProxy => ({
    metadata: { name: 'web-alb' },
    spec: { rules },
  });
  const toWorkload = { networkService: { name: 'web', port: 'http' } };

  test('a rule with no matches routes / to the named port', () => {
    expect(albRoutes(proxy([{ backends: [toWorkload] }]), [service])).toEqual([
      { paths: ['/'], narrowed: false, port: { name: 'http', number: 8080, appProtocol: undefined }, percent: undefined, sharedWith: 0 },
    ]);
  });

  test('keeps only the rules that send traffic to this workload', () => {
    const routes = albRoutes(
      proxy([
        { matches: [{ path: { type: 'PathPrefix', value: '/api' } }], backends: [toWorkload] },
        { matches: [{ path: { type: 'PathPrefix', value: '/other' } }], backends: [{ endpoint: 'https://example.com' }] },
      ]),
      [service]
    );
    expect(routes.map((r) => r.paths)).toEqual([['/api']]);
  });

  test("a split rule gives this workload's share and how many backends share it", () => {
    const [route] = albRoutes(
      proxy([{ backends: [{ ...toWorkload, weight: 1 }, { endpoint: 'https://a.example', weight: 3 }] }]),
      [service]
    );
    expect(route.percent).toBe(25);
    expect(route.sharedWith).toBe(1);
  });

  test('unset weights count as 1', () => {
    const [route] = albRoutes(proxy([{ backends: [toWorkload, { endpoint: 'https://a.example' }] }]), [service]);
    expect(route.percent).toBe(50);
  });

  test('marks rules narrowed by headers, query or method, and regular expressions', () => {
    const [route] = albRoutes(
      proxy([
        {
          matches: [{ path: { type: 'RegularExpression', value: '/v[0-9]+' }, method: 'GET' }],
          backends: [toWorkload],
        },
      ]),
      [service]
    );
    expect(route.paths).toEqual(['~ /v[0-9]+']);
    expect(route.narrowed).toBe(true);
  });

  test('a port the service does not declare keeps its name without a number', () => {
    const [route] = albRoutes(
      proxy([{ backends: [{ networkService: { name: 'web', port: 'grpc' } }] }]),
      [service]
    );
    expect(route.port).toEqual({ name: 'grpc', number: undefined, appProtocol: undefined });
  });

  test('a proxy with no backend on this workload has no routes', () => {
    expect(albRoutes(proxy([{ backends: [{ endpoint: 'https://a.example' }] }]), [service])).toEqual([]);
    expect(albRoutes(proxy(undefined), [])).toEqual([]);
  });
});
