/**
 * `portal.page/project` extension at `:workloadName/instances/:instanceName`,
 * exposed as `InstanceDetail` — the staff-portal support view for a single
 * Instance of a Workload.
 *
 * Linked from the workload Overview's instance table and the Instances tab.
 * Overview leads with anything wrong on this instance (non-True conditions,
 * scheduling gates, the `Available` condition's message), then its own
 * CPU/memory and full conditions, with the spec and addresses on the side.
 * Logs is the same explorer as the workload's, scoped to this instance.
 *
 * The instance comes from the workload's instance list (same query and cache
 * as the workload page), so navigating between them doesn't refetch.
 */
import { ConditionsTable } from '../components/conditions-table';
import { CopyableMono } from '../components/copyable-mono';
import { DetailList, StatusBadge } from '../components/detail-list';
import { SparklineStatCard } from '../components/sparkline-stat-card';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { WorkloadLogsExplorer } from '../components/workload-logs';
import { useWorkloadInstances } from '../lib/api';
import { formatLocationName, useLocationIndex } from '../lib/locations';
import { cpuUsageQuery, memoryUsageQuery, useInstanceMetricIdentity } from '../lib/metrics-queries';
import { useRollingLastHour } from '../lib/use-rolling-range';
import type { Instance, InstanceStatusValue } from '../schema';
import { Card, CardContent, CardHeader, CardTitle } from '@datum-cloud/datum-ui/card';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@datum-cloud/datum-ui/tabs';
import { formatDistanceToNowStrict } from 'date-fns';
import {
  ArrowLeftIcon,
  BoxIcon,
  ListChecksIcon,
  NetworkIcon,
  TriangleAlertIcon,
} from 'lucide-react';
import { useMemo, useState } from 'react';
import { Link, useParams } from 'react-router';

const TABS = ['Overview', 'Logs'] as const;
type Tab = (typeof TABS)[number];

function instanceBadgeType(instance: Instance): 'success' | 'warning' | 'danger' | 'muted' {
  if (instance.suspended) return 'muted';
  const byStatus: Record<InstanceStatusValue, 'success' | 'warning' | 'danger' | 'muted'> = {
    Available: 'success',
    Pending: 'warning',
    Failed: 'danger',
    Unknown: 'muted',
  };
  return byStatus[instance.status];
}

function InstanceSummary({
  instance,
  workloadName,
  locationLabel,
}: {
  instance: Instance;
  workloadName: string;
  locationLabel: string;
}) {
  return (
    <div className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
      <StatusBadge type={instanceBadgeType(instance)}>
        {instance.suspended ? 'Suspended' : instance.status}
      </StatusBadge>
      <span>
        workload{' '}
        <Link to="../.." relative="path" className="text-primary font-mono hover:underline">
          {workloadName}
        </Link>
      </span>
      <span aria-hidden>·</span>
      <span title={instance.location}>{locationLabel}</span>
      <span aria-hidden>·</span>
      <span>created {formatDistanceToNowStrict(instance.createdAt, { addSuffix: true })}</span>
    </div>
  );
}

function AttentionCard({ instance }: { instance: Instance }) {
  const items: Array<{ key: string; title: string; detail?: string }> = [];
  if (instance.schedulingGates.length > 0) {
    items.push({
      key: 'gates',
      title: `Gated: ${instance.schedulingGates.join(', ')}`,
    });
  }
  for (const c of instance.conditions.filter((c) => c.status !== 'True')) {
    items.push({
      key: c.type,
      title: `${c.type}: ${c.reason ?? c.status}`,
      detail: c.message,
    });
  }
  if (items.length === 0) return null;

  return (
    <Card size="sm" sectioned className="w-full overflow-hidden" data-testid="instance-attention">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <TriangleAlertIcon className="text-destructive size-4 stroke-2" />
          Needs attention
          <span className="text-muted-foreground font-normal">({items.length})</span>
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        <ul className="divide-border divide-y">
          {items.map((item) => (
            <li key={item.key} className="flex flex-col gap-0.5 px-4 py-3">
              <span className="text-sm font-medium">{item.title}</span>
              {item.detail ? (
                <p className="text-muted-foreground text-sm whitespace-normal">{item.detail}</p>
              ) : null}
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}

function DetailsCard({
  instance,
  workloadName,
  locationLabel,
}: {
  instance: Instance;
  workloadName: string;
  locationLabel: string;
}) {
  const resources = [
    instance.cpu && `${instance.cpu} CPU`,
    instance.memory && `${instance.memory} memory`,
  ]
    .filter(Boolean)
    .join(' · ');

  return (
    <Card size="sm" sectioned className="w-full overflow-hidden" data-testid="instance-details">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <BoxIcon className="text-secondary size-4 stroke-2" />
          Details
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        <DetailList
          labelWidth="7rem"
          items={[
            { label: 'Name', content: <CopyableMono value={instance.name} /> },
            {
              label: 'Workload',
              content: <CopyableMono value={workloadName} />,
            },
            { label: 'Placement', content: instance.placement ?? '—' },
            {
              label: 'Location',
              content: <span title={instance.location}>{locationLabel}</span>,
            },
            { label: 'Instance type', content: instance.instanceType ?? '—' },
            {
              label: 'Resources',
              content: resources || '—',
              hidden: !resources,
            },
            {
              label: 'Image',
              content: instance.image ? <CopyableMono value={instance.image} /> : '—',
            },
            {
              label: 'Created',
              content: (
                <span title={instance.createdAt.toISOString()}>
                  {formatDistanceToNowStrict(instance.createdAt, {
                    addSuffix: true,
                  })}
                </span>
              ),
            },
          ]}
        />
      </CardContent>
    </Card>
  );
}

function NetworkCard({ instance }: { instance: Instance }) {
  return (
    <Card size="sm" sectioned className="w-full overflow-hidden" data-testid="instance-network">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <NetworkIcon className="text-secondary size-4 stroke-2" />
          Network
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        <DetailList
          labelWidth="7rem"
          items={[
            {
              label: 'Internal IP',
              content: instance.internalIP ? <CopyableMono value={instance.internalIP} /> : '—',
            },
            {
              label: 'External IP',
              content: instance.externalIP ? <CopyableMono value={instance.externalIP} /> : '—',
            },
          ]}
        />
      </CardContent>
    </Card>
  );
}

function OverviewTab({
  instance,
  workloadName,
  projectName,
  locationLabel,
  onOpenLogs,
}: {
  instance: Instance;
  workloadName: string;
  projectName?: string;
  locationLabel: string;
  onOpenLogs: () => void;
}) {
  const timeRange = useRollingLastHour();
  const { identity, isLoading, isDenied } = useInstanceMetricIdentity(projectName, instance);
  const cpuQuery = projectName && identity ? cpuUsageQuery(projectName, identity) : undefined;
  const memoryQuery = projectName && identity ? memoryUsageQuery(projectName, identity) : undefined;

  return (
    <div className="flex flex-col gap-6">
      <AttentionCard instance={instance} />
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        <div className="flex min-w-0 flex-col gap-6 lg:col-span-2">
          <div className="grid grid-cols-2 gap-3">
            <SparklineStatCard
              title="CPU (cores)"
              query={cpuQuery}
              format="number"
              timeRange={timeRange}
              rangeLabel="Last 1h"
              pending={isLoading}
              denied={isDenied}
              unavailable={!isLoading && !isDenied && !cpuQuery}
              unavailableLabel="No data"
            />
            <SparklineStatCard
              title="Memory"
              query={memoryQuery}
              format="bytes"
              color="var(--color-chart-1)"
              timeRange={timeRange}
              rangeLabel="Last 1h"
              pending={isLoading}
              denied={isDenied}
              unavailable={!isLoading && !isDenied && !memoryQuery}
              unavailableLabel="No data"
            />
          </div>
          {instance.statusMessage ? (
            <Card size="sm" className="w-full">
              <CardContent className="flex flex-col gap-1">
                <span className="text-muted-foreground text-xs font-medium">
                  Status
                  {instance.statusReason ? ` · ${instance.statusReason}` : ''}
                </span>
                <p className="text-sm whitespace-normal">{instance.statusMessage}</p>
                <button
                  type="button"
                  onClick={onOpenLogs}
                  className="text-primary w-fit cursor-pointer text-xs hover:underline">
                  View logs
                </button>
              </CardContent>
            </Card>
          ) : null}
          <Card
            size="sm"
            sectioned
            className="w-full overflow-hidden"
            data-testid="instance-conditions">
            <CardHeader size="sm" bordered>
              <CardTitle className="flex items-center gap-2 text-sm">
                <ListChecksIcon className="text-secondary size-4 stroke-2" />
                Conditions
              </CardTitle>
            </CardHeader>
            <CardContent>
              <ConditionsTable conditions={instance.conditions} />
            </CardContent>
          </Card>
        </div>
        <div className="flex min-w-0 flex-col gap-6">
          <DetailsCard
            instance={instance}
            workloadName={workloadName}
            locationLabel={locationLabel}
          />
          <NetworkCard instance={instance} />
        </div>
      </div>
    </div>
  );
}

export default function InstanceDetail() {
  const { projectName, workloadName, instanceName } = useParams<{
    projectName: string;
    workloadName: string;
    instanceName: string;
  }>();
  const [tab, setTab] = useState<Tab>('Overview');
  const {
    data: instances,
    isLoading,
    error,
    refetch,
  } = useWorkloadInstances(projectName, workloadName);
  const instance = useMemo(
    () => instances?.find((i) => i.name === instanceName),
    [instances, instanceName]
  );
  const instanceNames = useMemo(() => (instance ? [instance.name] : []), [instance]);
  const locationIndex = useLocationIndex(projectName);
  const locationLabel = instance ? formatLocationName(instance.location, locationIndex) : '';

  return (
    <div
      className="flex min-w-0 flex-col gap-6 p-4 sm:p-6"
      // Size from the host's column, not from content: without inline-size
      // containment a long instance name or table row widens the whole
      // host page instead of scrolling inside its own card.
      style={{ containerType: 'inline-size' }}
      data-testid="provider-plugin-instance-detail">
      <div className="flex flex-col gap-3">
        <Link
          to="../.."
          relative="path"
          className="text-muted-foreground hover:text-foreground flex w-fit items-center gap-1.5 text-xs">
          <ArrowLeftIcon className="size-3.5" />
          {workloadName}
        </Link>
        <PageTitle
          title={instanceName ?? 'Instance'}
          titleClassName="break-all sm:break-normal"
          description={
            instance && workloadName ? (
              <InstanceSummary
                instance={instance}
                workloadName={workloadName}
                locationLabel={locationLabel}
              />
            ) : undefined
          }
          descriptionClassName="max-w-none"
        />
      </div>

      {isLoading && <LoadingSkeleton />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this instance."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && !instance && (
        <EmptyContent
          title="instance not found"
          subtitle={`${workloadName ?? 'This workload'} has no instance named ${instanceName ?? ''}. It may have been replaced.`}
          size="sm"
          variant="dashed"
        />
      )}

      {!isLoading && !error && instance && workloadName && (
        <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)} className="gap-6">
          <div className="border-border -mx-4 border-b px-4 sm:-mx-6 sm:px-6">
            <TabsList variant="line">
              {TABS.map((t) => (
                <TabsTrigger key={t} value={t} className="text-xs md:py-2">
                  {t}
                </TabsTrigger>
              ))}
            </TabsList>
          </div>
          <TabsContent value="Overview">
            <OverviewTab
              instance={instance}
              workloadName={workloadName}
              projectName={projectName}
              locationLabel={locationLabel}
              onOpenLogs={() => setTab('Logs')}
            />
          </TabsContent>
          <TabsContent value="Logs">
            <WorkloadLogsExplorer projectName={projectName} instanceNames={instanceNames} />
          </TabsContent>
        </Tabs>
      )}
    </div>
  );
}
