import { describe, expect, test } from 'vitest';
import {
  namesMatcher,
  networkInterfaceSeriesLabels,
  networkTrafficQuery,
  networkTrafficSeriesMatch,
  networkInterfaceRefs,
} from './metrics-queries';

const ifaces = [
  { name: 'eth0', network: 'default', networkInterfaceRef: 'web-0-eth0' },
  { name: 'eth1', network: 'backend', networkInterfaceRef: 'web-0-eth1' },
];

describe('namesMatcher', () => {
  test('quotes a single name and escapes quotes and backslashes', () => {
    expect(namesMatcher('network', ['a"b'])).toBe('network="a\\"b"');
    expect(namesMatcher('network', ['a\\b'])).toBe('network="a\\\\b"');
  });
  test('regex-escapes dotted names and escapes the backslash for the string literal', () => {
    // VictoriaMetrics rejects `"x\.y"`; the wire form needs `"x\\.y"`.
    expect(namesMatcher('network', ['x.y', 'z'])).toBe('network=~"x\\\\.y|z"');
  });
  test('is undefined with no names', () => {
    expect(namesMatcher('network', [])).toBeUndefined();
  });
});

describe('networkInterfaceRefs', () => {
  test('keeps only interfaces with a ref', () => {
    expect(networkInterfaceRefs([...ifaces, { name: 'eth2' }])).toEqual(['web-0-eth0', 'web-0-eth1']);
  });
});

describe('networkTrafficQuery', () => {
  test('builds the receive query for one interface', () => {
    expect(networkTrafficQuery('proj', 'default', [ifaces[0]], 'receive')).toBe(
      'sum by (network) (rate(datum_vpc_interface_receive_bytes_total{resourcemanager_datumapis_com_project_name="proj",resource_namespace="default",network="web-0-eth0"}[5m])) * 8'
    );
  });
  test('builds the transmit query for several interfaces with a regex matcher', () => {
    expect(networkTrafficQuery('proj', 'default', ifaces, 'transmit')).toBe(
      'sum by (network) (rate(datum_vpc_interface_transmit_bytes_total{resourcemanager_datumapis_com_project_name="proj",resource_namespace="default",network=~"web-0-eth0|web-0-eth1"}[5m])) * 8'
    );
  });
  test('returns undefined when no refs', () => {
    expect(networkTrafficQuery('proj', 'default', [{ name: 'eth0' }], 'receive')).toBeUndefined();
    expect(networkTrafficQuery('proj', 'default', [], 'receive')).toBeUndefined();
  });
  test('escapes regex metacharacters in refs for the string literal on the wire', () => {
    const q = networkTrafficQuery('proj', 'default', [
      { name: 'a', networkInterfaceRef: 'x.y' }, { name: 'b', networkInterfaceRef: 'z' },
    ], 'receive');
    expect(q).toContain('network=~"x\\\\.y|z"');
  });
  test('uses the instance namespace and has no query without one', () => {
    expect(networkTrafficQuery('proj', 'team-a', [ifaces[0]], 'receive')).toContain('resource_namespace="team-a"');
    expect(networkTrafficQuery('proj', undefined, [ifaces[0]], 'receive')).toBeUndefined();
  });
});

describe('networkTrafficSeriesMatch', () => {
  test('selects the receive metric for the interfaces', () => {
    expect(networkTrafficSeriesMatch('proj', 'default', ifaces)).toBe(
      '{__name__="datum_vpc_interface_receive_bytes_total",resourcemanager_datumapis_com_project_name="proj",resource_namespace="default",network=~"web-0-eth0|web-0-eth1"}'
    );
  });
  test('returns undefined when no refs or no namespace', () => {
    expect(networkTrafficSeriesMatch('proj', 'default', [{ name: 'eth0' }])).toBeUndefined();
    expect(networkTrafficSeriesMatch('proj', undefined, ifaces)).toBeUndefined();
  });
});

describe('networkInterfaceSeriesLabels', () => {
  test('maps the ref to "name · network"', () => {
    expect(networkInterfaceSeriesLabels(ifaces)).toEqual({
      'web-0-eth0': 'eth0 · default',
      'web-0-eth1': 'eth1 · backend',
    });
  });
  test('uses the name alone when the network is unknown and skips entries without a ref', () => {
    expect(networkInterfaceSeriesLabels([
      { name: 'eth0', networkInterfaceRef: 'r0' }, { name: 'eth1' },
    ])).toEqual({ r0: 'eth0' });
  });
});
