import { describe, expect, test } from 'bun:test';
import {
  instanceTypeShortName,
  instanceMatchesSearch,
  instanceStatusBadge,
  sortInstances,
} from './instance-presenters';
import { buildLocationIndex } from './locations';
import type { Instance, InstanceStatusValue } from '../schema';

function instance(
  name: string,
  status: InstanceStatusValue,
  extra: Partial<Instance> = {}
): Instance {
  return {
    uid: `uid-${name}`,
    name,
    createdAt: new Date('2026-01-01'),
    status,
    ports: [],
    containers: [],
    internalIPs: [],
    conditions: [],
    schedulingGates: [],
    suspended: false,
    ...extra,
  };
}

describe('sortInstances', () => {
  test('puts unhealthy instances first', () => {
    const result = sortInstances([
      instance('ok', 'Available'),
      instance('down', 'Failed'),
      instance('unknown', 'Unknown'),
      instance('starting', 'Pending'),
    ]);
    expect(result.map((i) => i.name)).toEqual(['down', 'starting', 'unknown', 'ok']);
  });

  test('groups by location, then deployment index, then name', () => {
    const result = sortInstances([
      instance('b-1', 'Available', { location: 'us-west', instanceIndex: 1 }),
      instance('zz-0', 'Available', { location: 'us-east', instanceIndex: 0 }),
      instance('a-2', 'Available', { location: 'us-east', instanceIndex: 2 }),
      instance('c', 'Available', { location: 'us-east' }),
      instance('b', 'Available', { location: 'us-east' }),
      instance('nowhere', 'Available'),
    ]);
    expect(result.map((i) => i.name)).toEqual(['zz-0', 'a-2', 'b', 'c', 'b-1', 'nowhere']);
  });

  test('does not mutate the input', () => {
    const input = [instance('ok', 'Available'), instance('down', 'Failed')];
    sortInstances(input);
    expect(input.map((i) => i.name)).toEqual(['ok', 'down']);
  });
});

describe('instanceStatusBadge', () => {
  test('maps every status to a badge type', () => {
    expect(instanceStatusBadge('Available')).toBe('success');
    expect(instanceStatusBadge('Pending')).toBe('warning');
    expect(instanceStatusBadge('Failed')).toBe('danger');
    expect(instanceStatusBadge('Unknown')).toBe('muted');
  });
});

describe('instanceMatchesSearch', () => {
  const index = buildLocationIndex([{ name: 'us-east-1-a', region: 'us-east-1' }]);
  const subject = instance('web-abc12-0', 'Pending', {
    location: 'us-east-1-a',
    workloadDeploymentName: 'web-us-east-1-abc12',
    placement: 'us',
    internalIP: '10.0.1.4',
    internalIPs: ['10.0.1.4', '2001:db8::1'],
    externalIP: '203.0.113.9',
    statusReason: 'SchedulingGatesPresent',
    schedulingGates: ['compute.datumapis.com/quota'],
  });

  test('empty search matches everything', () => {
    expect(instanceMatchesSearch(subject, '   ', index)).toBe(true);
  });

  test.each([
    ['name', 'ABC12'],
    ['raw location', 'us-east-1-a'],
    ['region label', 'us-east-1'],
    ['deployment', 'web-us-east-1-abc12'],
    ['placement', 'us'],
    ['internal IPv6', 'db8::1'],
    ['external IP', '203.0.113'],
    ['status', 'pending'],
    ['status reason', 'gatespresent'],
    ['gate name', 'quota'],
  ])('matches on %s', (_field, needle) => {
    expect(instanceMatchesSearch(subject, needle, index)).toBe(true);
  });

  test('rejects an unrelated term', () => {
    expect(instanceMatchesSearch(subject, 'eu-west', index)).toBe(false);
  });
});

describe('instanceTypeShortName', () => {
  test('strips the vendor prefix in both catalog spellings', () => {
    expect(instanceTypeShortName('datumcloud-d1-standard-2')).toBe('d1-standard-2');
    expect(instanceTypeShortName('datumcloud/d1-standard-2')).toBe('d1-standard-2');
  });

  test('leaves other names and undefined alone', () => {
    expect(instanceTypeShortName('d1-standard-2')).toBe('d1-standard-2');
    expect(instanceTypeShortName(undefined)).toBeUndefined();
  });
});
