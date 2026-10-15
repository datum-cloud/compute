/**
 * Project-scoped Location catalog (`locations.miloapis.com`).
 *
 * Compute stamps Location resource names (and sometimes city codes) onto
 * workloads and instances. The catalog's topology labels turn those into
 * region codes like "us-east-1", matching cloud-portal ALB pages.
 *
 * List failures (missing IAM, API not offered) degrade to an empty catalog so
 * callers keep showing the raw name.
 */
import { ApiError, PLUGIN_ID, getProjectScopedBase } from './api';
import { useQuery } from '@tanstack/react-query';
import { useMemo } from 'react';

export const TOPOLOGY_CITY = 'topology.datum.net/city';
export const TOPOLOGY_CITY_CODE = 'topology.datum.net/city-code';
export const TOPOLOGY_COUNTRY = 'topology.datum.net/country';
export const TOPOLOGY_COUNTRY_CODE = 'topology.datum.net/country-code';
export const TOPOLOGY_REGION = 'topology.datum.net/region';
export const LABEL_LOCATION = 'networking.datumapis.com/location';

const LOCATIONS_PATH = '/apis/locations.miloapis.com/v1alpha1/locations';

export interface Location {
  name: string;
  region?: string;
  city?: string;
  cityCode?: string;
  country?: string;
  countryCode?: string;
  locationLabel?: string;
}

export type LocationIndex = Map<string, Location>;

interface RawLocation {
  metadata?: { name?: string; labels?: Record<string, string> };
  spec?: { topology?: Record<string, string> };
}

interface RawLocationList {
  items?: RawLocation[];
}

function normalizeKey(value: string): string {
  return value.toLowerCase().replace(/-[a-z]$/, '');
}

export function toLocation(raw: RawLocation): Location {
  const topology = raw.spec?.topology ?? {};
  const labels = raw.metadata?.labels ?? {};
  return {
    name: raw.metadata?.name ?? '',
    region: topology[TOPOLOGY_REGION],
    city: topology[TOPOLOGY_CITY],
    cityCode: topology[TOPOLOGY_CITY_CODE],
    country: topology[TOPOLOGY_COUNTRY],
    countryCode: topology[TOPOLOGY_COUNTRY_CODE],
    locationLabel: labels[LABEL_LOCATION],
  };
}

export function locationMatchKeys(location: Location): string[] {
  return [location.name, location.region, location.locationLabel, location.cityCode].filter(
    (key): key is string => !!key?.trim()
  );
}

export function buildLocationIndex(locations: readonly Location[]): LocationIndex {
  const index: LocationIndex = new Map();
  for (const location of locations) {
    for (const key of locationMatchKeys(location)) {
      const normalized = normalizeKey(key);
      if (!index.has(normalized)) index.set(normalized, location);
      if (!index.has(key.toLowerCase())) index.set(key.toLowerCase(), location);
    }
  }
  return index;
}

export function lookupLocation(name: string, index: LocationIndex): Location | undefined {
  return index.get(normalizeKey(name)) ?? index.get(name.toLowerCase());
}

/** Region label shown in the portal: "us-east-1". Falls back to the resource name. */
export function locationPlace(location: Location): string {
  return location.region || location.locationLabel || location.name;
}

/** Country name for secondary text, when it adds something beyond the region code. */
export function locationCountry(location: Location): string | undefined {
  const country = location.country?.trim();
  if (!country || country === locationPlace(location)) return undefined;
  return country;
}

export function formatLocationName(name: string | undefined, index: LocationIndex): string {
  if (!name) return '—';
  const location = lookupLocation(name, index);
  return location ? locationPlace(location) : name;
}

export function formatLocationCountry(
  name: string | undefined,
  index: LocationIndex
): string | undefined {
  if (!name) return undefined;
  const location = lookupLocation(name, index);
  return location ? locationCountry(location) : undefined;
}

export function formatLocationTooltip(
  name: string | undefined,
  index: LocationIndex
): string | undefined {
  if (!name) return undefined;
  const label = formatLocationName(name, index);
  const country = formatLocationCountry(name, index);
  return country ? `${label} · ${country}` : label;
}

export function formatLocationNames(
  names: readonly string[],
  index: LocationIndex,
  empty = '—'
): string {
  if (names.length === 0) return empty;
  return names.map((name) => formatLocationName(name, index)).join(', ');
}

export function formatLocationSelector(
  selector: string | undefined,
  index: LocationIndex
): string | undefined {
  if (!selector) return undefined;
  return selector.replace(/\b[A-Za-z0-9][A-Za-z0-9._-]*\b/g, (token) => {
    if (token.includes('.')) return token;
    const location = lookupLocation(token, index);
    if (!location) return token;
    const place = locationPlace(location);
    return place === token ? token : place;
  });
}

async function fetchLocations(projectId: string): Promise<Location[]> {
  const url = `${getProjectScopedBase(projectId)}${LOCATIONS_PATH}?limit=200`;
  const res = await fetch(url, { headers: { Accept: 'application/json' } });
  if (res.status === 403 || res.status === 404) return [];
  if (!res.ok) {
    throw new ApiError(res.status, `Request failed (${res.status}): ${LOCATIONS_PATH}`);
  }
  const body = (await res.json()) as RawLocationList;
  return (body.items ?? []).map(toLocation);
}

export function useLocations(projectId: string | undefined) {
  return useQuery({
    queryKey: [PLUGIN_ID, 'locations', projectId],
    enabled: !!projectId,
    queryFn: () => fetchLocations(projectId as string),
    staleTime: 15_000,
    refetchInterval: (query) => ((query.state.data?.length ?? 0) > 0 ? 5 * 60_000 : 15_000),
    retry: false,
  });
}

export function useLocationIndex(projectId: string | undefined): LocationIndex {
  const { data = [] } = useLocations(projectId);
  return useMemo(() => buildLocationIndex(data), [data]);
}
