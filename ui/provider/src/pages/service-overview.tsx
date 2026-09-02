/**
 * `portal.page/service` extension at `""`, exposed as `ServiceOverview` —
 * replaces staff-portal's built-in Overview content for compute (see
 * `path: ""` on `PageServiceExtension` in staff-portal's
 * `app/modules/plugins/types.ts`). staff-portal's own `DetailShell` still
 * renders the outer identity header (name/badges) and the tab strip above
 * this — this page only owns what's below the tabs.
 *
 * Shows the fleet-wide "at a glance" numbers
 * (`FleetStats`/`FailedProjectsNotice`, shared with the Workloads tab — see
 * `../components/fleet-summary.tsx`) plus a short, non-interactive preview
 * of the worst unhealthy workloads, and a handful of service-catalog facts
 * kept from the built-in Overview this replaces — see
 * `../lib/service-catalog.ts` for exactly what was kept and why (picked for
 * support triage value, not parity with the page it replaces). The full
 * sortable/paginated list of *every* workload (healthy included) stays on
 * the Workloads tab (`../pages/fleet-workloads.tsx`); this page is
 * deliberately light — Overview is every service's default landing content.
 * (It does still pull in a `GroupedTable` for the conditions summary below,
 * which shares its underlying `data-table` chunk with Workloads — but skips
 * Workloads' own sort/paginate/search UI.)
 *
 * `serviceName` comes from `useParams()` resolving the ancestor route param
 * from staff-portal's service detail route — see `../lib/api.ts`'s header
 * comment for why this works with no extra prop/context plumbing.
 */
import { StatusBadge } from '../components/detail-list';
import { FailedProjectsNotice, FleetStats } from '../components/fleet-summary';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useFleetHealth, type FleetWorkload } from '../lib/fleet-health';
import {
  useServiceCatalogDetails,
  type MeterMetric,
  type PricingCharge,
  type QuotaLimit,
  type ServiceCondition,
} from '../lib/service-catalog';
import { healthToBadgeType } from '../schema';
import { createColumnHelper } from '../lib/table';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { GroupedTable, type GroupedTableGroup } from '@datum-cloud/datum-ui/grouped-table';
import { Text } from '@datum-cloud/datum-ui/typography';
import { formatDistanceToNowStrict } from 'date-fns';
import { ChevronRightIcon } from 'lucide-react';
import { useMemo } from 'react';
import { Link, useParams } from 'react-router';

/** How many of the worst unhealthy workloads to preview — full list is the Workloads tab. */
const PREVIEW_COUNT = 5;

function PreviewRow({ entry }: { entry: FleetWorkload }) {
  const { project, workload, reason, statusSince } = entry;
  return (
    <div className="flex flex-wrap items-center gap-3 border-b px-3 py-2 text-sm last:border-b-0">
      <StatusBadge type={healthToBadgeType(workload.health)}>{workload.health}</StatusBadge>
      <Link
        to={`/customers/projects/${project.name}/plugins/workloads/${workload.name}`}
        className="font-mono hover:underline">
        {workload.name}
      </Link>
      <Text size="sm" textColor="muted" className="truncate">
        {reason}
      </Text>
      <div className="ml-auto flex items-center gap-1 text-right">
        {project.organization && (
          <>
            <span className="text-muted-foreground truncate text-xs">
              {project.organization.displayName}
            </span>
            <ChevronRightIcon className="text-muted-foreground size-3 shrink-0" />
          </>
        )}
        <span className="truncate text-xs">{project.displayName}</span>
        <span className="text-muted-foreground w-24 shrink-0 text-xs">
          {formatDistanceToNowStrict(statusSince, { addSuffix: true })}
        </span>
      </div>
    </div>
  );
}

function phaseBadgeType(phase: string): 'success' | 'warning' | 'muted' {
  if (phase === 'Published') return 'success';
  if (phase === 'Deprecated') return 'warning';
  return 'muted';
}

/** Phase + gated badges, description, and dependencies — identity context otherwise lost once this page replaces the built-in header block. */
function ServiceIdentity({
  phase,
  gated,
  description,
  dependencies,
}: {
  phase: string;
  gated: boolean;
  description?: string;
  dependencies: string[];
}) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2">
        <StatusBadge type={phaseBadgeType(phase)}>{phase}</StatusBadge>
        {gated && <StatusBadge type="warning">Gated by Provider</StatusBadge>}
      </div>
      {description && (
        <Text size="sm" textColor="muted">
          {description}
        </Text>
      )}
      {dependencies.length > 0 && (
        <Text size="xs" textColor="muted">
          Depends on: {dependencies.join(', ')}
        </Text>
      )}
    </div>
  );
}

const conditionColumnHelper = createColumnHelper<ServiceCondition>();

/**
 * Whether the compute service itself is healthy — checked *before* digging
 * into a single customer's workload, since a platform-wide problem here
 * (e.g. quota fan-out broken) can look identical to a customer-specific one.
 * Summary line always shows a real count either way; expanding it lists
 * every condition (not just the unhealthy ones — a healthy condition's
 * `reason` is still useful context, e.g. confirming *when* it last passed).
 * Starts expanded when something's actually wrong, collapsed otherwise —
 * modeled as a single-group `GroupedTable` so expand state, the chevron, and
 * the collapsible header band come from the shared component instead of a
 * hand-rolled `useState`/`ChevronDownIcon` toggle.
 */
function ServiceConditionsSummary({ conditions }: { conditions: ServiceCondition[] }) {
  const unhealthy = useMemo(() => conditions.filter((c) => c.status !== 'True'), [conditions]);

  const columns = useMemo(
    () => [
      conditionColumnHelper.display({
        id: 'message',
        header: 'Message',
        cell: ({ row }) => (
          <Text size="sm" textColor="muted">
            {row.original.message ?? row.original.reason ?? '—'}
          </Text>
        ),
      }),
    ],
    []
  );

  const groups = useMemo<GroupedTableGroup<ServiceCondition>[]>(
    () => [
      {
        id: 'conditions',
        title:
          unhealthy.length === 0
            ? `All ${conditions.length} service conditions are healthy`
            : `${unhealthy.length} of ${conditions.length} service conditions unhealthy`,
        meta: (
          <StatusBadge type={unhealthy.length === 0 ? 'success' : 'danger'}>
            {unhealthy.length === 0 ? 'Healthy' : `${unhealthy.length} unhealthy`}
          </StatusBadge>
        ),
        rows: conditions,
        defaultOpen: unhealthy.length > 0,
      },
    ],
    [conditions, unhealthy]
  );

  if (conditions.length === 0) return null;

  return (
    <GroupedTable<ServiceCondition> columns={columns} groups={groups} getRowId={(row) => row.type} />
  );
}

/** Explains the most common "why is this workload stuck" question — a quota ceiling. */
function QuotaLimitsList({ limits }: { limits: QuotaLimit[] }) {
  if (limits.length === 0) return null;

  return (
    <div className="flex flex-col gap-2">
      <Text size="sm" weight="semibold">
        Quota limits
      </Text>
      <div className="border-border overflow-hidden rounded-lg border">
        {limits.map((l) => (
          <div key={l.name} className="flex items-center justify-between gap-3 border-b px-3 py-2 text-sm last:border-b-0">
            <span>{l.displayName}</span>
            <span className="text-muted-foreground">
              {l.defaultLimit} {l.unit}
              {l.consumerKind ? ` per ${l.consumerKind}` : ''}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

/** What a customer's being billed for — the plain-language answer to "why was I charged X." */
function PricingList({ charges }: { charges: PricingCharge[] }) {
  if (charges.length === 0) return null;

  return (
    <div className="flex flex-col gap-2">
      <Text size="sm" weight="semibold">
        Pricing
      </Text>
      <div className="border-border overflow-hidden rounded-lg border">
        {charges.map((c) => (
          <div key={c.name} className="flex flex-col gap-0.5 border-b px-3 py-2 text-sm last:border-b-0">
            <span>{c.displayName}</span>
            <span className="text-muted-foreground text-xs">{c.summary}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

/** What each billing metric actually measures — useful when a customer asks what a line item means. */
function MetersList({ meters }: { meters: MeterMetric[] }) {
  if (meters.length === 0) return null;

  return (
    <div className="flex flex-col gap-2">
      <Text size="sm" weight="semibold">
        Meters
      </Text>
      <div className="border-border overflow-hidden rounded-lg border">
        {meters.map((m) => (
          <div key={m.name} className="flex items-center justify-between gap-3 border-b px-3 py-2 text-sm last:border-b-0">
            <span>{m.displayName}</span>
            <span className="text-muted-foreground text-xs">
              {m.kind}
              {m.unit ? ` · ${m.unit}` : ''}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

export default function ServiceOverview() {
  const { name: serviceName } = useParams<{ name: string }>();
  const { data, isLoading, error, refetch } = useFleetHealth(serviceName);
  const { data: catalog, error: catalogError } = useServiceCatalogDetails(serviceName);
  const unhealthy = data?.workloads.filter((w) => w.workload.health !== 'Available') ?? [];

  return (
    <div className="flex min-w-0 flex-col gap-6 p-4 sm:p-6" data-testid="provider-plugin-overview">
      {catalog && (
        <ServiceIdentity
          phase={catalog.phase}
          gated={catalog.gated}
          description={catalog.description}
          dependencies={catalog.dependencies}
        />
      )}

      {/* Catalog details (phase/conditions/quota/pricing/meters) are a
          supplement, not the page's core data — a failure here shouldn't
          block the fleet-health content below, just say so instead of the
          whole service-identity/conditions/quota/pricing section silently
          vanishing with no explanation. */}
      {catalogError && (
        <Text size="xs" textColor="muted">
          Service-catalog details (phase, conditions, quota, pricing) failed to load.
        </Text>
      )}

      {isLoading && <LoadingSkeleton />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view compute's consumer fleet."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && data && (
        <>
          <FleetStats data={data} />

          {catalog && <ServiceConditionsSummary conditions={catalog.conditions} />}

          <FailedProjectsNotice failed={data.failed} />

          {unhealthy.length === 0 ? (
            <EmptyContent
              title="every workload across the fleet is healthy"
              size="sm"
              variant="dashed"
            />
          ) : (
            <div className="flex flex-col gap-2">
              <Text size="sm" weight="semibold">
                Worst unhealthy workloads
              </Text>
              <div className="border-border overflow-hidden rounded-lg border">
                {unhealthy.slice(0, PREVIEW_COUNT).map((entry) => (
                  <PreviewRow
                    key={`${entry.project.name}/${entry.workload.uid || entry.workload.name}`}
                    entry={entry}
                  />
                ))}
              </div>
              {unhealthy.length > PREVIEW_COUNT && (
                <Text size="xs" textColor="muted">
                  {unhealthy.length - PREVIEW_COUNT} more on the Workloads tab.
                </Text>
              )}
            </div>
          )}

          {catalog && <QuotaLimitsList limits={catalog.quotaLimits} />}

          {catalog && (catalog.charges.length > 0 || catalog.meters.length > 0) && (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              <PricingList charges={catalog.charges} />
              <MetersList meters={catalog.meters} />
            </div>
          )}
        </>
      )}
    </div>
  );
}
