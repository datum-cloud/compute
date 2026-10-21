import { describe, expect, test } from 'bun:test';
import {
  applyInstanceFilters,
  instanceProblem,
  locationFacetCounts,
  parseProblemsParam,
  placementFacetCounts,
  prunePicks,
  statusFacetCounts,
} from './instance-list-filters';
import { buildLocationIndex } from './locations';
import type { Instance, InstanceStatusValue } from '../schema';

function instance(
  name: string,
  status: InstanceStatusValue,
  extra: Partial<Instance> = {},
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

const index = buildLocationIndex([
  { name: 'us-east-1-a', region: 'us-east-1' },
  { name: 'eu-west-1-a', region: 'eu-west-1' },
]);

const fleet = [
  instance('ok-1', 'Available', { location: 'us-east-1-a', placement: 'us' }),
  instance('ok-2', 'Available', { location: 'eu-west-1-a', placement: 'eu' }),
  instance('ok-suspended', 'Available', {
    location: 'us-east-1-a',
    placement: 'us',
    suspended: true,
  }),
  instance('pending-gated', 'Pending', {
    location: 'eu-west-1-a',
    placement: 'eu',
    schedulingGates: ['compute.datumapis.com/quota'],
  }),
  instance('failed', 'Failed', { location: 'us-east-1-a', placement: 'us' }),
  instance('nowhere', 'Unknown'),
];

describe('instanceProblem', () => {
  test('flags anything not Available, or held back', () => {
    expect(fleet.filter(instanceProblem).map((i) => i.name)).toEqual([
      'ok-suspended',
      'pending-gated',
      'failed',
      'nowhere',
    ]);
  });
});

describe('facet counts', () => {
  test('status options in severity order with pseudo-statuses, zero counts omitted', () => {
    expect(statusFacetCounts(fleet)).toEqual([
      { value: 'Failed', label: 'Failed', count: 1 },
      { value: 'Pending', label: 'Pending', count: 1 },
      { value: 'Unknown', label: 'Unknown', count: 1 },
      { value: 'Available', label: 'Available', count: 3 },
      { value: 'Suspended', label: 'Suspended', count: 1 },
      { value: 'Gated', label: 'Gated', count: 1 },
    ]);
    expect(statusFacetCounts([instance('a', 'Available')]).map((o) => o.value)).toEqual([
      'Available',
    ]);
  });

  test('locations use the catalog label and skip unplaced instances', () => {
    expect(locationFacetCounts(fleet, index)).toEqual([
      { value: 'eu-west-1', label: 'eu-west-1', count: 2 },
      { value: 'us-east-1', label: 'us-east-1', count: 3 },
    ]);
  });

  test('placements', () => {
    expect(placementFacetCounts(fleet).map((o) => [o.value, o.count])).toEqual([
      ['eu', 2],
      ['us', 3],
    ]);
  });
});

describe('applyInstanceFilters', () => {
  const none = { statuses: [], locations: [], placements: [], problemsOnly: false };

  test('no picks keeps everything', () => {
    expect(applyInstanceFilters(fleet, none, index)).toHaveLength(6);
  });

  test('statuses OR together, pseudo-statuses match by flag', () => {
    const result = applyInstanceFilters(
      fleet,
      { ...none, statuses: ['Failed', 'Suspended'] },
      index,
    );
    expect(result.map((i) => i.name)).toEqual(['ok-suspended', 'failed']);
  });

  test('facets AND together', () => {
    const result = applyInstanceFilters(
      fleet,
      { ...none, statuses: ['Available'], locations: ['us-east-1'] },
      index,
    );
    expect(result.map((i) => i.name)).toEqual(['ok-1', 'ok-suspended']);
  });

  test('problems only', () => {
    const result = applyInstanceFilters(fleet, { ...none, problemsOnly: true }, index);
    expect(result.map((i) => i.name)).toEqual([
      'ok-suspended',
      'pending-gated',
      'failed',
      'nowhere',
    ]);
  });

  test('a location pick excludes instances without a location', () => {
    const result = applyInstanceFilters(
      fleet,
      { ...none, locations: ['us-east-1', 'eu-west-1'] },
      index,
    );
    expect(result.map((i) => i.name)).not.toContain('nowhere');
  });

  test('placement pick', () => {
    const result = applyInstanceFilters(fleet, { ...none, placements: ['eu'] }, index);
    expect(result.map((i) => i.name)).toEqual(['ok-2', 'pending-gated']);
  });
});

describe('prunePicks', () => {
  test('drops picks no instance matches', () => {
    const facets = {
      status: statusFacetCounts(fleet),
      location: locationFacetCounts(fleet, index),
      placement: placementFacetCounts(fleet),
    };
    expect(
      prunePicks(
        {
          statuses: ['Failed', 'Bogus'],
          locations: ['us-east-1', 'ap-south-1'],
          placements: ['nope'],
          problemsOnly: true,
        },
        facets,
      ),
    ).toEqual({
      statuses: ['Failed'],
      locations: ['us-east-1'],
      placements: [],
      problemsOnly: true,
    });
  });
});

describe('parseProblemsParam', () => {
  test('accepts 1 and true only', () => {
    expect(parseProblemsParam('1')).toBe(true);
    expect(parseProblemsParam('true')).toBe(true);
    expect(parseProblemsParam('0')).toBe(false);
    expect(parseProblemsParam(null)).toBe(false);
  });
});
