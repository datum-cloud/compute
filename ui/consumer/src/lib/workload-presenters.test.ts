import { describe, expect, test } from 'vitest';
import {
  DEPLOY_STALLED_AFTER_MS,
  HEALTH_DOT_COLOR,
  STATUS_TONE_COLOR,
  deployStatus,
  formatElapsed,
  homeColumnWorkloads,
  newestFirst,
  instanceFailureSummary,
  instanceStatusLabel,
  matchesWorkloadSearch,
  statusLabel,
  workloadStatusSortWeight,
  workloadStatusTone,
} from './workload-presenters';
import type { Instance, Workload, WorkloadHealth } from '../schema';

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
