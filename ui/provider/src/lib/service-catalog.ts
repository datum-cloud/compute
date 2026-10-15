/**
 * The subset of the built-in Overview's `Service`/`ServiceConfiguration`
 * detail worth keeping on the Overview-override page — see
 * `../pages/service-overview.tsx`. Picked from a support point of view, not
 * for parity with the page it replaces:
 *
 * - Phase / gated badges + description — identity context that's otherwise
 *   gone once this page replaces staff-portal's built-in header block.
 * - Service-level conditions — tells support whether a problem is
 *   platform-wide (e.g. quota fan-out broken) before they go looking for a
 *   per-customer cause.
 * - Quota limits — the plain-language answer to "why is this workload
 *   stuck," since the workload preview's own reasons (e.g.
 *   `NoAvailablePlacements`) are often a quota ceiling.
 * - Pricing and meters — a customer asking "why was I billed X" or "what
 *   does this metric mean" is a routine support question; both are shown at
 *   the bottom, lower priority than anything actionable above.
 *
 * Deliberately dropped: monitored resource types and which
 * `ServiceConfiguration` is currently published — catalog metadata that
 * isn't itself a common support question the way pricing/quota are.
 *
 * Fetched via the same generic `/api/internal/...` proxy as everything else
 * in this plugin — no plugin-owned backend.
 */
import { proxyFetchAbsolute, PLUGIN_ID } from './api';
import { useQuery, type UseQueryResult } from '@tanstack/react-query';

const SERVICE_CATALOG_QUERY_KEY = [PLUGIN_ID, 'service-catalog-details'];
const REFETCH_INTERVAL_MS = 60_000;

export interface ServiceCondition {
  type: string;
  status: 'True' | 'False' | 'Unknown';
  reason?: string;
  message?: string;
}

export interface QuotaLimit {
  name: string;
  displayName: string;
  defaultLimit: number;
  unit: string;
  /** e.g. "Project" — the resource kind this limit is scoped per. */
  consumerKind?: string;
}

export interface PricingCharge {
  name: string;
  displayName: string;
  /** e.g. "$0.000014 / vcpu-second" — a plain-language summary, not the raw rate structure. */
  summary: string;
}

export interface MeterMetric {
  name: string;
  displayName: string;
  /** e.g. "Cumulative", "Gauge". */
  kind: string;
  unit: string;
}

export interface ServiceCatalogDetails {
  phase: string;
  gated: boolean;
  description?: string;
  /** Other services this one depends on (resource names). */
  dependencies: string[];
  conditions: ServiceCondition[];
  quotaLimits: QuotaLimit[];
  charges: PricingCharge[];
  meters: MeterMetric[];
}

interface RawService {
  spec?: {
    phase?: string;
    description?: string;
    enablementPolicy?: { mode?: string };
    dependencies?: { serviceRef?: { name?: string } }[];
  };
}

async function fetchService(serviceResourceName: string): Promise<RawService> {
  // `proxyFetchAbsolute` (not a bare `fetch`) so a 403 throws `ApiError` —
  // required for `ErrorOrRestrictedState` to render the restricted-access
  // state rather than a generic failure card.
  return proxyFetchAbsolute<RawService>(
    `/apis/services.miloapis.com/v1alpha1/services/${encodeURIComponent(serviceResourceName)}`
  );
}

interface RawServiceCharge {
  name?: string;
  displayName?: string;
  chargeType?: string;
  usage?: {
    pricingUnit?: string;
    metricRef?: string;
    rates?: {
      match?: { dimension?: string; value?: string };
      flat?: string;
      tiered?: unknown[];
    }[];
  };
  oneTime?: { amount?: string; trigger?: string };
  recurring?: { amount?: string; interval?: string };
}

/**
 * Same rate-summarizing logic as staff-portal's `ChargesCard`
 * (`app/features/service-catalog/components/charges-card.tsx`), duplicated
 * rather than imported since that's host-only code.
 */
function summarizeCharge(charge: RawServiceCharge): string {
  if (charge.chargeType === 'Usage' && charge.usage) {
    const unit = charge.usage.pricingUnit;
    const parts = (charge.usage.rates ?? []).map((rate) => {
      const match = rate.match ? `${rate.match.dimension}=${rate.match.value}` : 'default';
      if (rate.flat) return `${match}: $${rate.flat}/${unit}`;
      if (rate.tiered?.length) return `${match}: ${rate.tiered.length} tiers`;
      return match;
    });
    if (parts.length === 0) return charge.usage.metricRef ?? '—';
    if (parts.length <= 3) return parts.join(' · ');
    return `${parts.slice(0, 2).join(' · ')} · +${parts.length - 2} more`;
  }
  if (charge.chargeType === 'OneTime' && charge.oneTime) {
    return `$${charge.oneTime.amount} · ${charge.oneTime.trigger}`;
  }
  if (charge.chargeType === 'Recurring' && charge.recurring) {
    return `$${charge.recurring.amount} / ${charge.recurring.interval}`;
  }
  return '—';
}

interface RawMetric {
  name?: string;
  displayName?: string;
  kind?: string;
  unit?: string;
}

interface RawServiceConfiguration {
  metadata?: { name?: string; creationTimestamp?: string };
  spec?: {
    serviceRef?: { name?: string };
    phase?: string;
    quota?: {
      limits?: {
        name?: string;
        displayName?: string;
        defaultLimit?: number;
        unit?: string;
        consumerType?: { kind?: string };
      }[];
    };
    charges?: RawServiceCharge[];
    metrics?: RawMetric[];
  };
  status?: {
    publishedAt?: string;
    conditions?: {
      type?: string;
      status?: 'True' | 'False' | 'Unknown';
      reason?: string;
      message?: string;
    }[];
  };
}

/**
 * Lists every `ServiceConfiguration` (no server-side filter — same
 * unfiltered list staff-portal's own `listServiceConfigurationsForService`
 * makes, filtering client-side) and picks the one Published for this
 * service, most-recently-published first.
 */
async function fetchActiveServiceConfiguration(
  serviceResourceName: string
): Promise<RawServiceConfiguration | undefined> {
  const body = await proxyFetchAbsolute<{ items?: RawServiceConfiguration[] }>(
    '/apis/services.miloapis.com/v1alpha1/serviceconfigurations'
  );
  const published = (body.items ?? []).filter(
    (c) => c.spec?.serviceRef?.name === serviceResourceName && c.spec?.phase === 'Published'
  );
  return published.sort((a, b) => {
    const at = a.status?.publishedAt ?? a.metadata?.creationTimestamp ?? '';
    const bt = b.status?.publishedAt ?? b.metadata?.creationTimestamp ?? '';
    return bt.localeCompare(at);
  })[0];
}

async function fetchServiceCatalogDetails(
  serviceResourceName: string
): Promise<ServiceCatalogDetails> {
  const [service, config] = await Promise.all([
    fetchService(serviceResourceName),
    fetchActiveServiceConfiguration(serviceResourceName),
  ]);

  return {
    phase: service.spec?.phase ?? 'Unknown',
    gated: service.spec?.enablementPolicy?.mode === 'GatedByProvider',
    description: service.spec?.description,
    dependencies: (service.spec?.dependencies ?? [])
      .map((d) => d.serviceRef?.name)
      .filter((name): name is string => !!name),
    conditions: (config?.status?.conditions ?? []).map((c) => ({
      type: c.type ?? '',
      status: c.status ?? 'Unknown',
      reason: c.reason,
      message: c.message,
    })),
    quotaLimits: (config?.spec?.quota?.limits ?? []).map((l) => ({
      name: l.name ?? '',
      displayName: l.displayName ?? l.name ?? '',
      defaultLimit: l.defaultLimit ?? 0,
      unit: l.unit ?? '',
      consumerKind: l.consumerType?.kind,
    })),
    charges: (config?.spec?.charges ?? []).map((c) => ({
      name: c.name ?? '',
      displayName: c.displayName ?? c.name ?? '',
      summary: summarizeCharge(c),
    })),
    meters: (config?.spec?.metrics ?? []).map((m) => ({
      name: m.name ?? '',
      displayName: m.displayName ?? m.name ?? '',
      kind: m.kind ?? '',
      unit: m.unit ?? '',
    })),
  };
}

export function useServiceCatalogDetails(
  serviceResourceName: string | undefined
): UseQueryResult<ServiceCatalogDetails, Error> {
  return useQuery({
    queryKey: [SERVICE_CATALOG_QUERY_KEY, serviceResourceName],
    enabled: !!serviceResourceName,
    queryFn: () => fetchServiceCatalogDetails(serviceResourceName as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}
