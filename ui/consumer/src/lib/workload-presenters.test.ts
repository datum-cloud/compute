import { describe, expect, test } from 'vitest';
import {
  compactDetailRows,
  crashDetail,
  crashDetailText,
  firstCrash,
  formatAgo,
  instanceNameParts,
  instanceProblem,
  instancesStarting,
  DEPLOY_STALLED_AFTER_MS,
  HEALTH_DOT_COLOR,
  STATUS_TONE_COLOR,
  instanceReadiness,
  instancesSectionState,
  privateNameState,
  deployStatus,
  formatElapsed,
  homeColumnWorkloads,
  newestFirst,
  overviewHealth,
  overviewStatusLine,
  instanceFailureSummary,
  instanceStatusLabel,
  matchesWorkloadSearch,
  statusLabel,
  workloadStatusSortWeight,
  workloadStatusTone,
} from './workload-presenters';
import type { Instance, Workload, WorkloadHealth } from '../schema';
import { SETTLING_WINDOW_MS, workloadSettling } from './workload-settling';

function workload(name: string, health: WorkloadHealth, createdAt: string): Workload {
  return {
    uid: `uid-${name}`,
    name,
    createdAt: new Date(createdAt),
    health,
    currentReplicas: 1,
    readyReplicas: 1,
    desiredReplicas: 1,
    placements: [],
    placementRegions: [],
    conditions: [],
    tags: [],
    ports: [],
    locations: [],
    networks: [],
    deleting: false,
  };
}

describe('newestFirst', () => {
  test('orders by creation time, newest first, regardless of health', () => {
    const input = [
      workload('old', 'Unavailable', '2026-01-01'),
      workload('newest', 'Available', '2026-03-01'),
      workload('middle', 'Degraded', '2026-02-01'),
    ];
    expect(newestFirst(input).map((w) => w.name)).toEqual(['newest', 'middle', 'old']);
    expect(input.map((w) => w.name)).toEqual(['old', 'newest', 'middle']);
  });
});

describe('homeColumnWorkloads', () => {
  test('puts unhealthy workloads first', () => {
    const result = homeColumnWorkloads([
      workload('ok', 'Available', '2026-03-01'),
      workload('down', 'Unavailable', '2026-01-01'),
      workload('meh', 'Degraded', '2026-02-01'),
      workload('unknown', 'Unknown', '2026-01-15'),
      workload('new', 'Deploying', '2026-01-10'),
    ]);
    expect(result.map((w) => w.name)).toEqual(['down', 'meh', 'new', 'unknown', 'ok']);
  });

  test('orders workloads with the same health newest first', () => {
    const result = homeColumnWorkloads([
      workload('old', 'Available', '2026-01-01'),
      workload('new', 'Available', '2026-03-01'),
    ]);
    expect(result.map((w) => w.name)).toEqual(['new', 'old']);
  });

  test('caps the list and leaves the input untouched', () => {
    const input = Array.from({ length: 8 }, (_, i) =>
      workload(`w${i}`, 'Available', `2026-01-0${i + 1}`)
    );
    expect(homeColumnWorkloads(input)).toHaveLength(5);
    expect(homeColumnWorkloads(input, 2).map((w) => w.name)).toEqual(['w7', 'w6']);
    expect(input[0].name).toBe('w0');
  });
});

const STARTED = '2026-10-05T12:00:00Z';
const startedAt = new Date(STARTED).getTime();

function deploying(reason?: string, message?: string): Workload {
  return {
    ...workload('web', 'Deploying', '2026-10-05T11:59:50Z'),
    readyReplicas: 0,
    conditions: reason
      ? [{ type: 'Available', status: 'False', reason, message, lastTransitionTime: STARTED }]
      : [],
  };
}

describe('deployStatus', () => {
  test('is undefined unless deploying', () => {
    expect(deployStatus(workload('ok', 'Available', STARTED))).toBeUndefined();
    expect(deployStatus(workload('down', 'Unavailable', STARTED))).toBeUndefined();
    expect(deployStatus({ ...deploying('InstancesProvisioning'), deleting: true })).toBeUndefined();
  });

  test('names the current step while progressing', () => {
    const status = deployStatus(deploying('NetworkProvisioning'), startedAt + 30_000);
    expect(status).toMatchObject({
      label: 'Deploying',
      title: 'Deploying',
      step: 'Provisioning network',
      tone: 'info',
      inProgress: true,
    });
    expect(status?.since).toEqual(new Date(STARTED));
  });

  test('warns once it has been deploying too long', () => {
    const status = deployStatus(
      deploying('InstancesProvisioning', 'Instances are being provisioned'),
      startedAt + DEPLOY_STALLED_AFTER_MS + 1,
    );
    expect(status).toMatchObject({
      title: 'Taking longer than expected',
      step: 'Starting instances',
      message: 'Instances are being provisioned',
      tone: 'warning',
      inProgress: true,
    });
  });

  test('warns straight away when waiting on quota', () => {
    const status = deployStatus(deploying('QuotaNotGranted', '1 of 1 desired instances pending quota'), startedAt);
    expect(status).toMatchObject({
      label: 'Waiting for quota',
      message: '1 of 1 desired instances pending quota',
      tone: 'warning',
      inProgress: false,
    });
  });

  test('times an unreconciled workload from creation', () => {
    const status = deployStatus(deploying(), startedAt);
    expect(status?.since).toEqual(new Date('2026-10-05T11:59:50Z'));
    expect(status?.step).toBe('Waiting for the controller');
  });
});

describe('statusLabel', () => {
  test('uses the deploy wording', () => {
    expect(statusLabel(deploying('InstancesProvisioning'))).toBe('Deploying');
    expect(statusLabel(deploying('QuotaNotGranted'))).toBe('Waiting for quota');
  });
});

describe('formatElapsed', () => {
  test('seconds, minutes, hours', () => {
    const since = new Date(STARTED);
    expect(formatElapsed(since, startedAt + 45_000)).toBe('45s');
    expect(formatElapsed(since, startedAt + 3 * 60_000)).toBe('3m');
    expect(formatElapsed(since, startedAt + 65 * 60_000)).toBe('1h 5m');
    expect(formatElapsed(since, startedAt - 5_000)).toBe('0s');
  });
});

function instance(name: string, conditions: Instance['conditions'], status: Instance['status'] = 'Pending'): Instance {
  return {
    uid: `uid-${name}`,
    name,
    createdAt: new Date(STARTED),
    ports: [],
    containers: [],
    internalIPs: [],
    addresses: [],
    externalIPs: [],
    hostnames: [],
    status,
    conditions,
  };
}

const crashing = (name: string) =>
  instance(
    name,
    [{ type: 'Ready', status: 'False', reason: 'InstanceCrashing', message: 'Back-off restarting failed container' }],
    'Failed'
  );

describe('instanceFailureSummary', () => {
  test('is undefined when nothing is failing', () => {
    expect(instanceFailureSummary([])).toBeUndefined();
    expect(
      instanceFailureSummary([instance('a', [{ type: 'Ready', status: 'False', reason: 'Provisioning' }])])
    ).toBeUndefined();
  });

  test('counts failing instances and quotes the first message', () => {
    expect(instanceFailureSummary([crashing('a')])).toBe(
      '1 instance failing: Back-off restarting failed container'
    );
    expect(instanceFailureSummary([crashing('a'), crashing('b')])).toBe(
      '2 instances failing: Back-off restarting failed container'
    );
  });

  test('falls back to the reason without a message', () => {
    expect(
      instanceFailureSummary([
        instance('a', [{ type: 'Programmed', status: 'False', reason: 'ImageUnavailable' }], 'Failed'),
      ])
    ).toBe('1 instance failing: Image unavailable');
  });
});

describe('instanceStatusLabel', () => {
  test('shows the step a pending instance is on', () => {
    expect(
      instanceStatusLabel(
        instance('a', [
          { type: 'Available', status: 'False', reason: 'Starting' },
          { type: 'Ready', status: 'False', reason: 'Provisioning' },
        ])
      )
    ).toBe('Provisioning');
  });

  test('shows why a failed instance is failing', () => {
    expect(instanceStatusLabel(crashing('a'))).toBe('Crashing');
  });

  test('leaves available instances alone', () => {
    expect(
      instanceStatusLabel(instance('a', [{ type: 'Available', status: 'True' }], 'Available'))
    ).toBe('Available');
  });
});


describe('matchesWorkloadSearch', () => {
  const api: Workload = {
    ...workload('api-gateway', 'Available', '2026-01-01'),
    image: 'ghcr.io/acme/api:1.4.2',
    runtimeType: 'sandbox',
    tags: ['production', 'team-core'],
    locations: ['us-central1-a', 'eu-west1-b'],
    networks: ['default', 'payments-vpc'],
  };

  test('an empty or whitespace query matches everything', () => {
    expect(matchesWorkloadSearch(api, '')).toBe(true);
    expect(matchesWorkloadSearch(api, '   ')).toBe(true);
  });

  test('matches on name, case-insensitively and on a partial', () => {
    expect(matchesWorkloadSearch(api, 'API-GATE')).toBe(true);
    expect(matchesWorkloadSearch(api, 'gateway')).toBe(true);
  });

  test('matches on image, runtime type, tags, locations and networks', () => {
    expect(matchesWorkloadSearch(api, 'ghcr.io/acme')).toBe(true);
    expect(matchesWorkloadSearch(api, 'sandbox')).toBe(true);
    expect(matchesWorkloadSearch(api, 'team-core')).toBe(true);
    expect(matchesWorkloadSearch(api, 'eu-west1')).toBe(true);
    // The table's own search covered networks before it moved up to the page.
    expect(matchesWorkloadSearch(api, 'payments-vpc')).toBe(true);
  });

  test('matches on the published hostname when one is supplied', () => {
    expect(matchesWorkloadSearch(api, 'shop.example.com')).toBe(false);
    expect(matchesWorkloadSearch(api, 'shop.example.com', 'shop.example.com')).toBe(true);
  });

  test('rejects a query that matches no field', () => {
    expect(matchesWorkloadSearch(api, 'database')).toBe(false);
  });

  test('tolerates a workload with no optional fields set', () => {
    expect(matchesWorkloadSearch(workload('bare', 'Unknown', '2026-01-01'), 'bare')).toBe(true);
    expect(matchesWorkloadSearch(workload('bare', 'Unknown', '2026-01-01'), 'nope')).toBe(false);
  });
});

describe('workloadStatusTone', () => {
  const now = Date.parse('2026-03-01T00:10:00Z');

  test('maps Available→ok, Degraded→warn, Unavailable→error, Unknown and deleting→muted', () => {
    expect(workloadStatusTone(workload('a', 'Available', '2026-01-01'), now).tone).toBe('ok');
    expect(workloadStatusTone(workload('b', 'Degraded', '2026-01-01'), now).tone).toBe('warn');
    expect(workloadStatusTone(workload('c', 'Unavailable', '2026-01-01'), now).tone).toBe('error');
    expect(workloadStatusTone(workload('u', 'Unknown', '2026-01-01'), now)).toEqual({ tone: 'muted', label: 'Unknown' });
    expect(workloadStatusTone({ ...workload('d', 'Available', '2026-01-01'), deleting: true }, now)).toEqual({
      tone: 'muted',
      label: 'Deleting',
    });
  });

  test('label is never all-caps', () => {
    const healths: WorkloadHealth[] = ['Available', 'Degraded', 'Deploying', 'Unavailable', 'Unknown'];
    for (const health of healths) {
      const { label } = workloadStatusTone(workload('w', health, '2026-03-01T00:09:30Z'), now);
      expect(label).not.toBe(label.toUpperCase());
    }
  });

  test('reports a stalled deploy as warn', () => {
    const createdAt = new Date(now - DEPLOY_STALLED_AFTER_MS - 1).toISOString();
    expect(workloadStatusTone(workload('slow', 'Deploying', createdAt), now)).toEqual({
      tone: 'warn',
      label: 'Deploying',
      reason: 'Taking longer than expected',
    });
    const fresh = new Date(now - 1_000).toISOString();
    expect(workloadStatusTone(workload('fresh', 'Deploying', fresh), now)).toEqual({
      tone: 'muted',
      label: 'Deploying',
    });
  });

  test('reports a quota wait as warn, with the label alone naming it', () => {
    const waiting: Workload = {
      ...workload('quota', 'Deploying', new Date(now - 1_000).toISOString()),
      conditions: [{ type: 'Available', status: 'False', reason: 'QuotaNotGranted' }],
    };
    expect(workloadStatusTone(waiting, now)).toEqual({ tone: 'warn', label: 'Waiting for quota' });
  });
});

describe('workloadStatusSortWeight', () => {
  const now = Date.parse('2026-03-01T00:10:00Z');

  test('sorts by the state the row shows, problems first', () => {
    const stalled = workload('stalled', 'Deploying', new Date(now - DEPLOY_STALLED_AFTER_MS - 1).toISOString());
    const deleting = { ...workload('deleting', 'Unavailable', '2026-01-01'), deleting: true };
    const ordered = [
      workload('healthy', 'Available', '2026-01-01'),
      deleting,
      stalled,
      workload('down', 'Unavailable', '2026-01-01'),
    ].sort((a, b) => workloadStatusSortWeight(a, now) - workloadStatusSortWeight(b, now));
    expect(ordered.map((w) => w.name)).toEqual(['down', 'stalled', 'deleting', 'healthy']);
  });
});

describe('dot colors', () => {
  test('every status tone uses a datum-ui badge token', () => {
    expect(STATUS_TONE_COLOR).toEqual({
      ok: 'var(--color-badge-success)',
      warn: 'var(--color-badge-warning)',
      error: 'var(--color-badge-danger)',
      muted: 'var(--color-badge-muted)',
    });
  });

  test('health dots share the status tone colors', () => {
    expect(HEALTH_DOT_COLOR.Available).toBe(STATUS_TONE_COLOR.ok);
    expect(HEALTH_DOT_COLOR.Degraded).toBe(STATUS_TONE_COLOR.warn);
    expect(HEALTH_DOT_COLOR.Unavailable).toBe(STATUS_TONE_COLOR.error);
    expect(HEALTH_DOT_COLOR.Unknown).toBe(STATUS_TONE_COLOR.muted);
    expect(HEALTH_DOT_COLOR.Deploying).toBe('var(--color-badge-info)');
  });
});

describe('instance reason labels', () => {
  test('a reason named like an Object prototype member is not treated as a label', () => {
    const pending = instance('a', [{ type: 'Ready', status: 'False', reason: 'constructor' }]);
    expect(instanceStatusLabel(pending)).toBe('Pending');
  });
});

describe('instanceReadiness', () => {
  test('Available=True → ok "Running"', () => {
    const result = instanceReadiness(
      instance('a', [
        { type: 'Available', status: 'True' },
        { type: 'Programmed', status: 'True' },
        { type: 'Ready', status: 'True' },
      ], 'Available')
    );
    expect(result).toEqual({ tone: 'ok', label: 'Running' });
  });

  test('Programmed=True, Ready=False reason InstanceCrashing → warn with reason "Crashing on start"', () => {
    const result = instanceReadiness(
      instance('a', [
        { type: 'Programmed', status: 'True' },
        { type: 'Ready', status: 'False', reason: 'InstanceCrashing', message: 'Back-off restarting' },
      ])
    );
    expect(result.tone).toBe('warn');
    expect(result.reason).toBe('Crashing on start');
  });

  test('Programmed=True, Ready=False unknown reason → warn with reason = Ready.message', () => {
    const result = instanceReadiness(
      instance('a', [
        { type: 'Programmed', status: 'True' },
        { type: 'Ready', status: 'False', reason: 'SomethingNew', message: 'Probe failed on port 8080' },
      ])
    );
    expect(result.tone).toBe('warn');
    expect(result.reason).toBe('Probe failed on port 8080');
  });

  test('a crashing instance the adapter marks Failed is still amber once programmed', () => {
    const result = instanceReadiness(
      instance('a', [
        { type: 'Programmed', status: 'True' },
        { type: 'Ready', status: 'False', reason: 'InstanceCrashing' },
      ], 'Failed')
    );
    expect(result.tone).toBe('warn');
  });

  test('ImageUnavailable → error with reason', () => {
    const result = instanceReadiness(
      instance('a', [
        { type: 'Programmed', status: 'True' },
        { type: 'Ready', status: 'False', reason: 'ImageUnavailable' },
      ], 'Failed')
    );
    expect(result).toEqual({ tone: 'error', label: 'Failed', reason: "Image can't be pulled" });
  });

  test('ConfigurationError → error with reason', () => {
    const result = instanceReadiness(
      instance('a', [
        { type: 'Programmed', status: 'True' },
        { type: 'Ready', status: 'False', reason: 'ConfigurationError' },
      ])
    );
    expect(result).toEqual({ tone: 'error', label: 'Failed', reason: 'Configuration rejected' });
  });

  test('InstanceCrashing → warn', () => {
    const result = instanceReadiness(
      instance('a', [
        { type: 'Programmed', status: 'True' },
        { type: 'Ready', status: 'False', reason: 'InstanceCrashing' },
      ], 'Failed')
    );
    expect(result).toEqual({ tone: 'warn', label: 'Not ready', reason: 'Crashing on start' });
  });

  test('Failed status → error', () => {
    const result = instanceReadiness(
      instance('a', [{ type: 'Programmed', status: 'False', reason: 'ConfigurationError' }], 'Failed')
    );
    expect(result.tone).toBe('error');
    expect(result.reason).toBe('Configuration rejected');
  });

  test('no conditions → muted "Starting", no reason', () => {
    expect(instanceReadiness(instance('a', [], 'Unknown'))).toEqual({ tone: 'muted', label: 'Starting' });
  });
});

describe('privateNameState', () => {
  test('hostnames present → names', () => {
    const withNames = { ...instance('a', [], 'Available'), hostnames: ['a.internal', 'a.v6.internal'] };
    expect(privateNameState(withNames)).toEqual({ kind: 'names', names: ['a.internal', 'a.v6.internal'] });
  });

  test('empty and not Available → "Assigned once the instance is running"', () => {
    expect(privateNameState(instance('a', [], 'Pending'))).toEqual({
      kind: 'missing',
      reason: 'Assigned once the instance is running',
    });
  });

  test('empty and Available → "No private name published on this network"', () => {
    expect(privateNameState(instance('a', [], 'Available'))).toEqual({
      kind: 'missing',
      reason: 'No private name published on this network',
    });
  });
});

describe('compactDetailRows', () => {
  test('compactDetailRows drops empty values', () => {
    const rows = [
      { label: 'undefined', value: undefined },
      { label: 'null', value: null },
      { label: 'empty string', value: '' },
      { label: 'empty array', value: [] },
      { label: 'zero', value: 0 },
      { label: 'false', value: false },
      { label: 'text', value: 'nginx' },
    ];
    const result = compactDetailRows(rows);
    expect(result.map((r) => r.label)).toEqual(['zero', 'false', 'text']);
    expect(rows).toHaveLength(7);
  });
});

describe('overviewStatusLine', () => {
  const base = { healthyCount: 2, totalCount: 2, locationCount: 1, deleting: false, now: Date.parse(STARTED) };

  test('a healthy workload is a plain ok line with counts', () => {
    expect(overviewStatusLine({ ...base, health: 'Available' })).toEqual({
      tone: 'ok',
      title: 'Serving normally',
      detail: '2/2 instances · 1 location',
    });
  });

  test('instance failures are red and lead with the reason', () => {
    const line = overviewStatusLine({ ...base, health: 'Unavailable', healthyCount: 0, failure: '1 instance failing: Back-off' });
    expect(line.tone).toBe('error');
    expect(line.detail).toBe('1 instance failing: Back-off · 0/2 instances available');
  });

  test('a fresh deploy is muted, a stalled one amber', () => {
    const since = new Date(base.now - 30_000);
    const deploy = { label: 'Deploying', title: 'Deploying', step: 'Starting instances', tone: 'info' as const, inProgress: true, since };
    expect(overviewStatusLine({ ...base, health: 'Deploying', deploy }).tone).toBe('muted');
    expect(overviewStatusLine({ ...base, health: 'Deploying', deploy }).detail).toBe(
      'Starting instances · 2/2 instances ready · started 30s ago'
    );
    expect(overviewStatusLine({ ...base, health: 'Deploying', deploy: { ...deploy, tone: 'warning' } }).tone).toBe('warn');
  });

  test('a load balancer error turns a healthy line amber', () => {
    const line = overviewStatusLine({ ...base, health: 'Available', albError: 'Certificate failed' });
    expect(line.tone).toBe('warn');
    expect(line.detail).toBe('Load balancer: Certificate failed');
  });

  test('a deleting workload is muted', () => {
    expect(overviewStatusLine({ ...base, health: 'Available', deleting: true })).toEqual({
      tone: 'muted',
      title: 'Deleting',
      detail: 'Instances are shutting down',
    });
  });

  test('unknown health waits for status', () => {
    expect(overviewStatusLine({ ...base, health: 'Unknown' }).title).toBe('Waiting for status');
  });
});

describe('instancesSectionState', () => {
  const base = { count: 0, loading: false, deploying: false, deleting: false };

  test('still loading holds placeholder rows, not an empty notice', () => {
    expect(instancesSectionState({ ...base, loading: true })).toEqual({ kind: 'loading' });
  });

  test('a failed load says so instead of claiming there are no instances', () => {
    expect(instancesSectionState({ ...base, error: { status: 500 } })).toEqual({
      kind: 'notice',
      title: "Couldn't load instances",
      description: 'They will appear here once the next refresh succeeds.',
    });
  });

  test('a 403 reads as restricted', () => {
    expect(instancesSectionState({ ...base, error: { status: 403 } })).toEqual({
      kind: 'notice',
      title: 'Access restricted',
      description: "You don't have permission to view this workload's instances.",
    });
  });

  test('rows already loaded stay up through a failed refresh', () => {
    expect(instancesSectionState({ ...base, count: 2, error: { status: 500 } })).toEqual({ kind: 'rows' });
  });

  test('zero instances explains why for deploying, deleting and idle workloads', () => {
    expect(instancesSectionState({ ...base, deploying: true })).toEqual({
      kind: 'notice',
      title: 'Instances are being created',
    });
    expect(instancesSectionState({ ...base, deploying: true, deleting: true })).toEqual({
      kind: 'notice',
      title: 'Instances are shutting down',
    });
    expect(instancesSectionState(base)).toEqual({
      kind: 'notice',
      title: 'No running instances',
      description: 'This workload has no instances right now, for example after scaling to zero.',
    });
  });
});

describe('instanceReadiness reason lookup', () => {
  test('a Ready reason that matches an Object.prototype key falls back to the message', () => {
    const result = instanceReadiness(
      instance('a', [
        { type: 'Programmed', status: 'True' },
        { type: 'Ready', status: 'False', reason: 'constructor', message: 'Probe failed' },
      ])
    );
    expect(result.reason).toBe('Probe failed');
  });
});

describe('overviewHealth', () => {
  const unavailable = (message: string): Workload => ({
    ...workload('w', 'Unavailable', STARTED),
    conditions: [{ type: 'Available', status: 'False', message }],
  });

  test('a crashing instance overrides a provisioning workload', () => {
    expect(overviewHealth(workload('w', 'Deploying', STARTED), [crashing('a')])).toEqual({
      health: 'Unavailable',
      failure: '1 instance failing: Back-off restarting failed container',
      instancesFailing: true,
    });
  });

  test('an available workload keeps its health whatever its instances say', () => {
    expect(overviewHealth(workload('w', 'Available', STARTED), [crashing('a')])).toEqual({
      health: 'Available',
      instancesFailing: false,
    });
  });

  test('a deleting workload ignores instance failures', () => {
    const deleting = { ...workload('w', 'Deploying', STARTED), deleting: true };
    expect(overviewHealth(deleting, [crashing('a')])).toEqual({ health: 'Deploying', instancesFailing: false });
  });

  test('an unavailable workload explains itself through its Available condition', () => {
    expect(overviewHealth(unavailable('No replicas'), [])).toEqual({
      health: 'Unavailable',
      failure: 'No replicas',
      instancesFailing: false,
    });
  });
});

describe('overviewStatusLine, remaining branches', () => {
  const base = { healthyCount: 1, totalCount: 2, locationCount: 1, deleting: false };

  test('degraded health is amber with the available count', () => {
    expect(overviewStatusLine({ ...base, health: 'Degraded' })).toEqual({
      tone: 'warn',
      title: 'Degraded',
      detail: '1/2 instances available',
    });
  });

  test('unavailable with no instances says so', () => {
    expect(overviewStatusLine({ ...base, health: 'Unavailable', totalCount: 0 }).detail).toBe('No running instances');
  });

  test('a load balancer error keeps a degraded title', () => {
    expect(overviewStatusLine({ ...base, health: 'Degraded', albError: 'Certificate failed' }).title).toBe('Degraded');
  });
});

describe('instanceReadiness before programming', () => {
  test('a network that failed to create shows red with a plain reason', () => {
    const result = instanceReadiness(
      instance('a', [
        { type: 'Programmed', status: 'Unknown', reason: 'Pending', message: 'Waiting for controller' },
        {
          type: 'Ready',
          status: 'False',
          reason: 'NetworkFailedToCreate',
          message: 'Interface "eth0": AllocationFailed: Allocating an IPv6 address failed',
        },
      ])
    );
    expect(result).toEqual({ tone: 'error', label: 'Failed', reason: "Network couldn't be set up" });
  });

  test('a recoverable reason before programming stays muted', () => {
    const result = instanceReadiness(
      instance('a', [
        { type: 'Programmed', status: 'Unknown', reason: 'Pending' },
        { type: 'Ready', status: 'False', reason: 'SchedulingGatesPresent' },
      ])
    );
    expect(result.tone).toBe('muted');
  });
});

describe('formatAgo', () => {
  const now = new Date('2026-11-06T12:00:00Z').getTime();
  test.each([
    [2_000, 'just now'],
    [45_000, '45s ago'],
    [12 * 60_000, '12m ago'],
    [3 * 3_600_000, '3h ago'],
    [47 * 3_600_000, '47h ago'],
    [4 * 86_400_000, '4d ago'],
  ])('%i ms ago reads %s', (ago, text) => {
    expect(formatAgo(new Date(now - ago), now)).toBe(text);
  });
});

describe('instanceNameParts', () => {
  test('splits off the workload name', () => {
    expect(instanceNameParts('web-default-us-east-1-0', 'web')).toEqual({ prefix: 'web-', rest: 'default-us-east-1-0' });
  });
  test('keeps names that do not start with the workload name whole', () => {
    expect(instanceNameParts('other-0', 'web')).toEqual({ prefix: '', rest: 'other-0' });
    expect(instanceNameParts('web-', 'web')).toEqual({ prefix: '', rest: 'web-' });
  });
});

describe('crashDetail', () => {
  test('reads exit code and restarts from the runtime message', () => {
    expect(crashDetail('Container "app" exited with code 137 and has restarted 3 times')).toEqual({
      exitCode: 137,
      restarts: 3,
    });
  });
  test('reads restarts alone', () => {
    expect(crashDetail('Container "app" keeps exiting and has restarted 1 time')).toEqual({ restarts: 1 });
  });
  test('is undefined for other messages', () => {
    expect(crashDetail('Back-off restarting failed container')).toBeUndefined();
    expect(crashDetail(undefined)).toBeUndefined();
  });
  test('describes exit 137 as out of memory', () => {
    expect(crashDetailText({ exitCode: 137, restarts: 3 })).toBe('Killed, usually out of memory (exit 137) · 3 restarts');
    expect(crashDetailText({ exitCode: 1, restarts: 1 })).toBe('Exited with code 1 · 1 restart');
  });
});

describe('firstCrash', () => {
  test('finds the first instance whose runtime is crashing', () => {
    const healthy = instance('a', [{ type: 'Ready', status: 'True' }], 'Available');
    const crash = instance('b', [
      { type: 'RuntimeReady', status: 'False', reason: 'InstanceCrashing', message: 'Container "app" exited with code 2 and has restarted 5 times' },
    ]);
    expect(firstCrash([healthy, crash])).toEqual({ instance: crash, detail: { exitCode: 2, restarts: 5 } });
    expect(firstCrash([healthy])).toBeUndefined();
  });
});


describe('overviewStatusLine, partial failure', () => {
  test('is degraded while some instances still serve', () => {
    const line = overviewStatusLine({
      health: 'Unavailable',
      deleting: false,
      healthyCount: 2,
      totalCount: 3,
      locationCount: 3,
      failure: '1 instance failing: Back-off',
    });
    expect(line).toEqual({ tone: 'warn', title: 'Degraded', detail: '2/3 instances available · 1 instance failing: Back-off' });
  });
});

describe('starting instances', () => {
  const booting = () =>
    instance('b', [
      { type: 'Programmed', status: 'True' },
      { type: 'Ready', status: 'False', reason: 'Provisioning' },
    ]);
  const running = () => instance('a', [{ type: 'Ready', status: 'True' }], 'Available');

  test('a placed instance that is still provisioning reads as starting, not amber', () => {
    expect(instanceReadiness(booting())).toEqual({ tone: 'muted', label: 'Starting', reason: 'Booting' });
  });
  test('instancesStarting is true while any instance boots and none has failed', () => {
    expect(instancesStarting([running(), booting()])).toBe(true);
    expect(instancesStarting([running()])).toBe(false);
    expect(instancesStarting([])).toBe(false);
    expect(instancesStarting([booting(), crashing('c')])).toBe(false);
  });
});

describe('workloadSettling', () => {
  const now = new Date('2026-11-06T12:00:00Z').getTime();
  const at = (ago: number) => new Date(now - ago);
  test('is true while deploying', () => {
    expect(workloadSettling({ ...workload('w', 'Deploying', '2026-11-06T11:00:00Z') }, now)).toBe(true);
  });
  test('stays true after Available until replicas are ready, within the window', () => {
    const w = { ...workload('w', 'Available', '2026-11-06T11:00:00Z'), readyReplicas: 0, updatedAt: at(5_000) };
    expect(workloadSettling(w, now)).toBe(true);
    expect(workloadSettling({ ...w, updatedAt: at(SETTLING_WINDOW_MS + 1) }, now)).toBe(false);
    expect(workloadSettling({ ...w, readyReplicas: 1 }, now)).toBe(false);
  });
  test('is false while deleting', () => {
    expect(workloadSettling({ ...workload('w', 'Deploying', '2026-11-06T11:00:00Z'), deleting: true }, now)).toBe(false);
  });
});

describe('instanceProblem', () => {
  const withImage = (i: Instance, image: string): Instance => ({ ...i, image });
  test('is undefined for a healthy or starting instance', () => {
    expect(instanceProblem(instance('a', [{ type: 'Ready', status: 'True' }], 'Available'))).toBeUndefined();
    expect(instanceProblem(instance('b', [{ type: 'Ready', status: 'False', reason: 'Provisioning' }]))).toBeUndefined();
  });
  test('explains an image that cannot be pulled anonymously from Docker Hub', () => {
    const p = instanceProblem(
      withImage(
        instance('c', [
          { type: 'Programmed', status: 'False', reason: 'ImageUnavailable', message: 'Image "docker.io/library/busybox:1.37" for container "app" could not be pulled anonymously' },
        ]),
        'docker.io/library/busybox:1.37'
      )
    );
    expect(p?.title).toBe("The image can't be pulled");
    expect(p?.tone).toBe('error');
    expect(p?.showLogs).toBe(false);
    expect(p?.hints).toHaveLength(3);
    expect(p?.hints[0]).toContain('docker.io/library/busybox:1.37');
  });
  test('leaves out the Docker Hub hint for other registries', () => {
    const p = instanceProblem(
      withImage(
        instance('d', [{ type: 'Ready', status: 'False', reason: 'ImageUnavailable', message: 'could not be pulled anonymously' }]),
        'ghcr.io/acme/app:1'
      )
    );
    expect(p?.hints).toHaveLength(2);
  });
  test('turns a crash message into exit code, restarts and a memory hint', () => {
    const p = instanceProblem(
      instance('e', [
        { type: 'RuntimeReady', status: 'False', reason: 'InstanceCrashing', message: 'Container "app" exited with code 137 and has restarted 3 times' },
      ])
    );
    expect(p).toMatchObject({ tone: 'warn', title: 'The app keeps crashing', message: 'Killed, usually out of memory (exit 137) · 3 restarts', showLogs: true });
    expect(p?.hints[0]).toContain('out of memory');
  });
});
