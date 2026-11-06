import { describe, expect, test } from 'vitest';
import {
  DEPLOYED_FOR_MS,
  deployHeadline,
  deployState,
  deployTimeline,
  stallCause,
} from './deploy-presenters';
import type { DeployStatus } from './workload-presenters';
import type { Instance, Workload } from '../schema';

const T0 = new Date('2026-11-06T12:06:20Z');
const at = (seconds: number) => new Date(T0.getTime() + seconds * 1000);

function workload(extra: Partial<Workload> = {}): Workload {
  return {
    uid: 'w',
    name: 'web',
    createdAt: T0,
    health: 'Available',
    currentReplicas: 1,
    readyReplicas: 1,
    desiredReplicas: 1,
    placements: [],
    placementRegions: [],
    conditions: [],
    tags: [],
    ports: [],
    locations: ['us-east-1'],
    networks: [],
    deleting: false,
    ...extra,
  };
}

function instance(conditions: Instance['conditions'], extra: Partial<Instance> = {}): Instance {
  return {
    uid: 'i',
    name: 'web-default-us-east-1-0',
    createdAt: at(5),
    location: 'us-east-1',
    ports: [],
    containers: [],
    internalIPs: [],
    addresses: [],
    externalIPs: [],
    hostnames: [],
    networkInterfaces: [],
    status: 'Pending',
    conditions,
    ...extra,
  };
}

const ready = instance(
  [
    { type: 'Programmed', status: 'True', lastTransitionTime: at(7).toISOString() },
    { type: 'Ready', status: 'True', lastTransitionTime: at(13).toISOString() },
  ],
  { status: 'Available' }
);
const booting = instance([
  { type: 'Programmed', status: 'True', lastTransitionTime: at(7).toISOString() },
  { type: 'Ready', status: 'False', reason: 'Provisioning' },
]);
const badImage = instance(
  [{ type: 'Ready', status: 'False', reason: 'ImageUnavailable', message: 'could not be pulled', lastTransitionTime: at(9).toISOString() }],
  { status: 'Failed', image: 'ghcr.io/acme/app:nope' }
);
const deploying: DeployStatus = { label: 'Deploying', title: 'Deploying', step: 'Starting instances', tone: 'info', inProgress: true, since: T0 };

describe('deployTimeline', () => {
  test('rebuilds Created → Placed → Started → Ready from timestamps', () => {
    expect(deployTimeline(workload(), [ready]).map((s) => [s.label, s.at.toISOString()])).toEqual([
      ['Created', T0.toISOString()],
      ['Placed in us-east-1', at(5).toISOString()],
      ['Started', at(7).toISOString()],
      ['Ready', at(13).toISOString()],
    ]);
  });
  test('stops where instances have got to and adds the step it is stuck on', () => {
    const steps = deployTimeline(workload(), [booting], { label: 'Waiting for quota', at: at(5), state: 'waiting' });
    expect(steps.map((s) => s.label)).toEqual(['Created', 'Placed in us-east-1', 'Started', 'Waiting for quota']);
    expect(steps.at(-1)?.state).toBe('waiting');
  });
});

describe('stallCause', () => {
  test('names a missing Secret from the deployments', () => {
    const cause = stallCause(
      [{ name: 'd', conditions: [{ type: 'ReferencedDataReady', status: 'False', reason: 'SourceNotFound', message: 'Secret "x" not found' }] }],
      []
    );
    expect(cause).toMatchObject({ title: "A Secret or ConfigMap it uses doesn't exist", message: 'Secret "x" not found' });
  });
  test('names a quota wait from the instances', () => {
    const cause = stallCause([], [instance([{ type: 'QuotaGranted', status: 'False', reason: 'QuotaExceeded', message: 'over limit' }])]);
    expect(cause).toMatchObject({ title: 'Waiting for quota', message: 'over limit' });
  });
  test('is undefined when nothing names a cause', () => {
    expect(stallCause([], [booting], deploying)).toBeUndefined();
  });
});

describe('deployState', () => {
  const base = { deployments: [], healthyCount: 0 };
  test('deploying while instances boot', () => {
    const state = deployState({ ...base, workload: workload(), instances: [booting], now: at(10).getTime() });
    expect(state?.phase).toBe('deploying');
  });
  test('stalled once it has taken over two minutes', () => {
    const state = deployState({ ...base, workload: workload(), instances: [booting], deploy: deploying, now: at(150).getTime() });
    expect(state?.phase).toBe('stalled');
    expect(state?.problem?.title).toBe('Starting instances is taking longer than usual');
    expect(state?.stuckOn).toBe('Starting instances');
  });
  test('stalled straight away when a cause is named', () => {
    const state = deployState({
      ...base,
      workload: workload({ health: 'Deploying' }),
      instances: [booting],
      deploy: deploying,
      deployments: [{ name: 'd', conditions: [{ type: 'ReferencedDataReady', status: 'False', reason: 'SourceNotFound' }] }],
      now: at(10).getTime(),
    });
    expect(state?.phase).toBe('stalled');
  });
  test('failed when nothing serves and an instance cannot recover', () => {
    const state = deployState({ ...base, workload: workload(), instances: [badImage], now: at(20).getTime() });
    expect(state).toMatchObject({ phase: 'failed', failedInstance: badImage });
  });
  test('deployed for a minute after the last instance is ready, then nothing', () => {
    const input = { ...base, workload: workload(), instances: [ready], healthyCount: 1 };
    expect(deployState({ ...input, now: at(20).getTime() })?.phase).toBe('deployed');
    expect(deployState({ ...input, now: at(13).getTime() + DEPLOYED_FOR_MS + 1 })).toBeUndefined();
  });
  test('no deployed moment for an old workload', () => {
    const old = workload({ createdAt: new Date(T0.getTime() - 3_600_000) });
    expect(deployState({ ...base, workload: old, instances: [ready], healthyCount: 1, now: at(20).getTime() })).toBeUndefined();
  });
});

describe('deployHeadline', () => {
  const counts = { healthy: 1, total: 1, locations: 1 };
  test('deployed reads the time from creation to ready', () => {
    expect(deployHeadline({ phase: 'deployed', since: T0, readyAt: at(13) }, counts, at(20).getTime())).toEqual({
      tone: 'ok',
      title: 'Deployed in 13s',
      detail: '1/1 instance ready · 1 location',
    });
  });
  test('failed names the problem', () => {
    const headline = deployHeadline(
      { phase: 'failed', since: T0, problem: { tone: 'error', title: "The image can't be pulled", hints: [], showLogs: false } },
      { ...counts, healthy: 0 },
      at(20).getTime()
    );
    expect(headline.detail).toBe("0/1 instance ready · the image can't be pulled");
  });
});
