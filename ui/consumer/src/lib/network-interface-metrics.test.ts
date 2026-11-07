import { describe, expect, test } from 'vitest';
import { resolveNetworkInterfaceMetrics, workloadInterfaceSeriesLabels } from './network-interface-metrics';
import type { Instance } from '../schema';

const ifaces = [{ name: 'eth0', network: 'default', networkInterfaceRef: 'web-0-eth0' }];

describe('resolveNetworkInterfaceMetrics', () => {
  test('is unavailable with no queries when the instance has no refs', () => {
    const s = resolveNetworkInterfaceMetrics({
      projectId: 'p',
      namespace: 'default',
      interfaces: [{ name: 'eth0' }],
      probedNetworks: [],
      probeDenied: false,
      probeError: undefined,
      probeLoading: false,
    });
    expect(s.unavailable).toBe(true);
    expect(s.receiveQuery).toBeUndefined();
    expect(s.transmitQuery).toBeUndefined();
    expect(s.isLoading).toBe(false);
  });

  test('is loading while the probe runs', () => {
    const s = resolveNetworkInterfaceMetrics({
      projectId: 'p',
      namespace: 'default',
      interfaces: ifaces,
      probedNetworks: undefined,
      probeDenied: false,
      probeError: undefined,
      probeLoading: true,
    });
    expect(s.isLoading).toBe(true);
    expect(s.unavailable).toBe(false);
  });

  test('is unavailable when the probe finds no series', () => {
    const s = resolveNetworkInterfaceMetrics({
      projectId: 'p',
      namespace: 'default',
      interfaces: ifaces,
      probedNetworks: [],
      probeDenied: false,
      probeError: undefined,
      probeLoading: false,
    });
    expect(s.unavailable).toBe(true);
  });

  test('builds both queries and labels when the probe finds the series', () => {
    const s = resolveNetworkInterfaceMetrics({
      projectId: 'p',
      namespace: 'default',
      interfaces: ifaces,
      probedNetworks: ['web-0-eth0'],
      probeDenied: false,
      probeError: undefined,
      probeLoading: false,
    });
    expect(s.unavailable).toBe(false);
    expect(s.isLoading).toBe(false);
    expect(s.isDenied).toBe(false);
    expect(s.receiveQuery).toContain('datum_vpc_interface_receive_bytes_total');
    expect(s.transmitQuery).toContain('datum_vpc_interface_transmit_bytes_total');
    expect(s.seriesLabels).toEqual({ 'web-0-eth0': 'eth0 · default' });
  });

  test('passes denial through', () => {
    const s = resolveNetworkInterfaceMetrics({
      projectId: 'p',
      namespace: 'default',
      interfaces: ifaces,
      probedNetworks: undefined,
      probeDenied: true,
      probeError: new Error('forbidden'),
      probeLoading: false,
    });
    expect(s.isDenied).toBe(true);
    expect(s.unavailable).toBe(false);
  });

  test('has no queries without a project', () => {
    const s = resolveNetworkInterfaceMetrics({
      projectId: undefined,
      namespace: 'default',
      interfaces: ifaces,
      probedNetworks: ['web-0-eth0'],
      probeDenied: false,
      probeError: undefined,
      probeLoading: false,
    });
    expect(s.receiveQuery).toBeUndefined();
    expect(s.transmitQuery).toBeUndefined();
  });

  test('surfaces a probe failure that is not a denial as an error, not an empty state', () => {
    const err = new Error('upstream unavailable');
    const s = resolveNetworkInterfaceMetrics({
      projectId: 'p',
      namespace: 'default',
      interfaces: ifaces,
      probedNetworks: undefined,
      probeDenied: false,
      probeError: err,
      probeLoading: false,
    });
    expect(s.error).toBe(err);
    expect(s.unavailable).toBe(false);
    expect(s.isDenied).toBe(false);
  });

  test('does not report an error when the probe was denied', () => {
    const s = resolveNetworkInterfaceMetrics({
      projectId: 'p',
      namespace: 'default',
      interfaces: ifaces,
      probedNetworks: undefined,
      probeDenied: true,
      probeError: new Error('forbidden'),
      probeLoading: false,
    });
    expect(s.error).toBeUndefined();
    expect(s.isDenied).toBe(true);
  });

  test('is unavailable with no queries when the instance has no namespace', () => {
    const s = resolveNetworkInterfaceMetrics({
      projectId: 'p',
      namespace: undefined,
      interfaces: ifaces,
      probedNetworks: undefined,
      probeDenied: false,
      probeError: undefined,
      probeLoading: false,
    });
    expect(s.unavailable).toBe(true);
    expect(s.receiveQuery).toBeUndefined();
    expect(s.transmitQuery).toBeUndefined();
  });
});

describe('workloadInterfaceSeriesLabels', () => {
  const inst = (name: string, refs: string[]) =>
    ({
      name,
      networkInterfaces: refs.map((ref, i) => ({ name: `eth${i}`, networkInterfaceRef: ref })),
    }) as unknown as Instance;
  test('labels each interface with its instance, and the interface when there are several', () => {
    expect(
      workloadInterfaceSeriesLabels([
        inst('web-default-us-east-1-0', ['a-eth0']),
        inst('web-default-eu-west-1-0', ['b-eth0', 'b-eth1']),
      ])
    ).toEqual({ 'a-eth0': 'us-east-1-0', 'b-eth0': 'eu-west-1-0 · eth0', 'b-eth1': 'eu-west-1-0 · eth1' });
  });
});
