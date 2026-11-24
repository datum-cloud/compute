import { describe, expect, test } from 'vitest';
import {
  seriesByWorkload,
  namesMatcher,
  networkInterfaceSeriesLabels,
  networkTrafficQuery,
  networkTrafficTotalQuery,
  networkThroughputQuery,
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

describe('seriesByWorkload', () => {
  const point = (timestamp: number, value: number) => ({ timestamp, value, formattedTime: String(timestamp) });
  const data = {
    timeRange: { start: 0, end: 2 },
    series: [
      { name: 'a', labels: { resource_name: 'web-1' }, data: [point(1, 1), point(2, 2)] },
      { name: 'b', labels: { resource_name: 'web-2' }, data: [point(1, 3), point(2, 4)] },
      { name: 'c', labels: { resource_name: 'api-1' }, data: [point(2, 5)] },
    ],
  };

  test('sums each workload\'s instances by timestamp', () => {
    const result = seriesByWorkload(data, 'resource_name', {
      web: ['web-1', 'web-2'],
      api: ['api-1'],
      idle: ['idle-1'],
    });
    expect(result.web.map((p) => p.value)).toEqual([4, 6]);
    expect(result.api.map((p) => [p.timestamp, p.value])).toEqual([[2, 5]]);
    expect(result.idle).toBeUndefined();
  });

  test('no data yields no series', () => {
    expect(seriesByWorkload(undefined, 'resource_name', { web: ['web-1'] })).toEqual({});
  });
});

describe('networkTrafficTotalQuery', () => {
  test('sums every interface into one series', () => {
    expect(networkTrafficTotalQuery('proj', 'default', ifaces, 'receive')).toBe(
      'sum(rate(datum_vpc_interface_receive_bytes_total{resourcemanager_datumapis_com_project_name="proj",resource_namespace="default",network=~"web-0-eth0|web-0-eth1"}[5m])) * 8'
    );
  });
  test('has no query without refs or a namespace', () => {
    expect(networkTrafficTotalQuery('proj', 'default', [], 'transmit')).toBeUndefined();
    expect(networkTrafficTotalQuery('proj', undefined, ifaces, 'transmit')).toBeUndefined();
  });
});

describe('networkThroughputQuery', () => {
  test('adds receive and transmit per interface', () => {
    expect(networkThroughputQuery('proj', 'default', [ifaces[0]])).toBe(
      'sum by (network) (rate({__name__=~"datum_vpc_interface_receive_bytes_total|datum_vpc_interface_transmit_bytes_total",resourcemanager_datumapis_com_project_name="proj",resource_namespace="default",network="web-0-eth0"}[5m])) * 8'
    );
  });
});
