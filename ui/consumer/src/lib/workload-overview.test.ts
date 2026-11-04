import { describe, expect, test } from 'bun:test';
import { toInstance, toWorkload, type RawInstance } from '../adapter';
import {
  ingressText,
  instanceNotReady,
  instancePrivateName,
  portText,
  privateReach,
  scaleText,
} from './workload-overview';

const HOST = 'cache-default-us-central-1-0.instances.datum.internal';

function instance(dns: NonNullable<RawInstance['status']>['dns']) {
  return toInstance({ metadata: { name: 'cache-default-us-central-1-0', uid: 'i-0' }, status: { dns } });
}

const published = instance([
  { network: { name: 'app-net' }, hostnames: [HOST], conditions: [{ type: 'Ready', status: 'True' }] },
]);
const unavailable = instance([
  {
    network: { name: 'app-net' },
    conditions: [
      { type: 'Ready', status: 'False', message: 'Private DNS is not available in this location' },
    ],
  },
]);

const workload = toWorkload({
  metadata: { name: 'cache', uid: 'w-1' },
  spec: {
    template: {
      spec: {
        runtime: { sandbox: { containers: [{ ports: [{ name: 'redis', port: 6379, protocol: 'TCP' }] }] } },
        networkInterfaces: [{ network: { name: 'app-net' } }],
      },
    },
  },
});

describe('instancePrivateName', () => {
  test('reads the published name', () => {
    expect(instancePrivateName(published)).toEqual({ ready: true, hostname: HOST });
  });

  test('says why there is none', () => {
    expect(instancePrivateName(unavailable)).toEqual({
      ready: false,
      message: 'Private DNS is not available in this location',
    });
  });

  test('is undefined before the platform reports anything', () => {
    expect(instancePrivateName(instance(undefined))).toBeUndefined();
  });
});

describe('privateReach', () => {
  test('gives the one instance’s address when its name is published', () => {
    expect(privateReach(workload, [published])).toEqual({ kind: 'address', address: `${HOST}:6379` });
  });

  test('names the network and port with several instances', () => {
    expect(privateReach(workload, [published, published])).toEqual({
      kind: 'network',
      network: 'app-net',
      port: 6379,
    });
  });

  test('names the network and port when the name is not published', () => {
    expect(privateReach(workload, [unavailable])).toEqual({ kind: 'network', network: 'app-net', port: 6379 });
  });
});

describe('ingressText', () => {
  test('reads an open rule as anywhere on the network', () => {
    expect(ingressText({ network: 'app-net', ports: ['6379/TCP'], from: ['0.0.0.0/0'] })).toBe(
      '6379/TCP from anywhere on app-net'
    );
    expect(ingressText({ ports: ['8080/TCP'], from: [] })).toBe('8080/TCP from anywhere');
  });

  test('lists the sources of a narrower rule', () => {
    expect(ingressText({ network: 'app-net', ports: ['5432/TCP'], from: ['fd00:0:1::/48'] })).toBe(
      '5432/TCP from fd00:0:1::/48 on app-net'
    );
  });

  test('says every port when the rule names none', () => {
    expect(ingressText({ ports: [], from: ['::/0'] })).toBe('Every port from anywhere');
  });
});

describe('scaleText', () => {
  test.each([
    [{ min: 1 }, 'min 1, no max'],
    [{ min: 1, max: 3 }, '1 to 3'],
    [{ min: 2, max: 2 }, '2'],
  ])('%o reads as %p', (scale, text) => {
    expect(scaleText(scale)).toBe(text);
  });

  test('is undefined with no minimum', () => {
    expect(scaleText(undefined)).toBeUndefined();
    expect(scaleText({ max: 3 })).toBeUndefined();
  });
});

describe('portText', () => {
  test('adds the port’s name when it has one', () => {
    expect(portText({ port: 6379, protocol: 'TCP', name: 'redis' })).toBe('6379/TCP (redis)');
    expect(portText({ port: 8080, protocol: 'TCP' })).toBe('8080/TCP');
  });
});

describe('instanceNotReady', () => {
  const NOT_PROGRAMMED = 'Interface "eth0" is not programmed: the attachment has no network advertisement';
  const withConditions = (conditions: NonNullable<RawInstance['status']>['conditions']) =>
    toInstance({ metadata: { name: 'guestbook-default-us-central-1-0', uid: 'i-1' }, status: { conditions } });

  test('says why an available instance is not ready', () => {
    const instance = withConditions([
      { type: 'Available', status: 'True', reason: 'Available' },
      { type: 'Ready', status: 'False', reason: 'NetworkInterfaceNotProgrammed', message: NOT_PROGRAMMED },
    ]);
    expect(instanceNotReady(instance)).toBe(NOT_PROGRAMMED);
  });

  test('is undefined for a ready instance', () => {
    const instance = withConditions([
      { type: 'Available', status: 'True' },
      { type: 'Ready', status: 'True' },
    ]);
    expect(instanceNotReady(instance)).toBeUndefined();
  });

  test('leaves an instance still coming up to its status', () => {
    const instance = withConditions([
      { type: 'Available', status: 'False', reason: 'Starting' },
      { type: 'Ready', status: 'False', reason: 'Provisioning' },
    ]);
    expect(instanceNotReady(instance)).toBeUndefined();
  });
});
