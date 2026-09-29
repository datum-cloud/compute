import { describe, expect, test } from 'bun:test';
import { homeColumnWorkloads } from './workload-presenters';
import type { Workload, WorkloadHealth } from '../schema';

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

describe('homeColumnWorkloads', () => {
  test('puts unhealthy workloads first', () => {
    const result = homeColumnWorkloads([
      workload('ok', 'Available', '2026-03-01'),
      workload('down', 'Unavailable', '2026-01-01'),
      workload('meh', 'Degraded', '2026-02-01'),
      workload('unknown', 'Unknown', '2026-01-15'),
    ]);
    expect(result.map((w) => w.name)).toEqual(['down', 'meh', 'unknown', 'ok']);
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
