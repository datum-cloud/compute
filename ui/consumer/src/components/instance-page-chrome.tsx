/**
 * Shared instance layout chrome: breadcrumbs, title, refresh, and tab bar.
 * Used by the splat layout at `instances/:instanceName/*`; tab bodies render
 * through the parent's `<Outlet />`.
 */
import { PluginTabs, type PluginTab } from './plugin-tabs';
import { instanceStatusToBadgeType, type Instance } from '../schema';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@datum-cloud/datum-ui/breadcrumb';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { cn } from '@datum-cloud/datum-ui/utils';
import { HomeIcon, RefreshCwIcon } from 'lucide-react';

/** Matches `StatusBadge` / Badge `type` solid fill tokens. */
const BADGE_TYPE_DOT: Record<ReturnType<typeof instanceStatusToBadgeType>, string> = {
  success: 'bg-[var(--color-badge-success)]',
  warning: 'bg-[var(--color-badge-warning)]',
  danger: 'bg-[var(--color-badge-danger)]',
  muted: 'bg-[var(--color-badge-muted)]',
};

export function instanceDetailTabs(overviewHref: string, logsHref: string): PluginTab[] {
  return [
    { label: 'Overview', href: overviewHref },
    { label: 'Metrics' },
    { label: 'Logs', href: logsHref },
    { label: 'Manage' },
    { label: 'Activity' },
  ];
}

export function InstancePageChrome({
  projectHref,
  workloadsHref,
  instancesHref,
  overviewHref,
  logsHref,
  titleName,
  workloadName,
  instance,
  onRefresh,
  children,
}: {
  projectHref: string;
  workloadsHref: string;
  instancesHref: string;
  overviewHref: string;
  logsHref: string;
  titleName: string;
  workloadName?: string;
  instance?: Instance | null;
  onRefresh?: () => void;
  children: React.ReactNode;
}) {
  return (
    <div data-testid="compute-plugin-instance-detail" className="flex min-w-0 flex-col gap-6">
      <Breadcrumb className="min-w-0 overflow-x-auto">
        <BreadcrumbList className="flex-nowrap">
          <BreadcrumbItem>
            <BreadcrumbLink href={projectHref}>
              <Icon icon={HomeIcon} size={16} />
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbLink href={workloadsHref}>Workloads</BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem className="min-w-0">
            <BreadcrumbLink href={instancesHref} className="truncate">
              {workloadName ?? 'Workload'}
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem className="min-w-0">
            <BreadcrumbPage className="truncate">{titleName}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle
        title={titleName}
        titleClassName="text-primary break-all sm:break-normal"
        className="flex-col items-start gap-3 sm:flex-row sm:items-center"
        description={
          <span className="mt-1 flex min-h-5 items-center gap-2 text-sm">
            {instance ? (
              <>
                <span
                  className={cn(
                    'size-2 shrink-0 rounded-full',
                    BADGE_TYPE_DOT[instanceStatusToBadgeType(instance.status)]
                  )}
                  aria-hidden
                />
                <span>{instance.status}</span>
                {instance.city ? (
                  <>
                    <span className="text-muted-foreground" aria-hidden>
                      ·
                    </span>
                    <span className="text-muted-foreground">{instance.city}</span>
                  </>
                ) : null}
              </>
            ) : (
              <span className="invisible" aria-hidden>
                Pending
              </span>
            )}
          </span>
        }
        actions={
          onRefresh ? (
            <button
              type="button"
              onClick={onRefresh}
              className="border-border hover:bg-muted inline-flex items-center justify-center gap-1.5 rounded-md border px-3 py-1.5 text-sm font-medium transition-colors">
              <Icon icon={RefreshCwIcon} size={14} />
              Refresh
            </button>
          ) : undefined
        }
      />

      <PluginTabs
        tabs={instanceDetailTabs(overviewHref, logsHref)}
        testId="compute-plugin-instance-tabs"
      />

      {children}
    </div>
  );
}
