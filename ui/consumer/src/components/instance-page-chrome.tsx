/**
 * Shared instance layout chrome: breadcrumbs, title, and tab bar.
 * Used by the splat layout at `instances/:instanceName/*`; tab bodies render
 * through the parent's `<Outlet />`.
 */
import { PluginTabs, type PluginTab } from './plugin-tabs';
import { type Instance } from '../schema';
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
import { HomeIcon } from 'lucide-react';
import { Link } from 'react-router';

export function instanceDetailTabs(
  overviewHref: string,
  metricsHref: string,
  logsHref: string
): PluginTab[] {
  return [
    { label: 'Overview', href: overviewHref },
    { label: 'Metrics', href: metricsHref },
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
  metricsHref,
  titleName,
  workloadName,
  instance,
  locationLabel,
  locationTooltip,
  children,
}: {
  projectHref: string;
  workloadsHref: string;
  instancesHref: string;
  overviewHref: string;
  logsHref: string;
  metricsHref: string;
  titleName: string;
  workloadName?: string;
  instance?: Instance | null;
  locationLabel?: string;
  locationTooltip?: string;
  children: React.ReactNode;
}) {
  return (
    <div data-testid="compute-plugin-instance-detail" className="flex min-w-0 flex-col gap-6">
      <Breadcrumb className="min-w-0 overflow-x-auto">
        <BreadcrumbList className="flex-nowrap">
          <BreadcrumbItem>
            <BreadcrumbLink asChild>
              <Link to={projectHref}>
                <Icon icon={HomeIcon} size={16} />
              </Link>
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbLink asChild>
              <Link to={workloadsHref}>Workloads</Link>
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem className="min-w-0">
            <BreadcrumbLink asChild className="truncate">
              <Link to={instancesHref}>{workloadName ?? 'Workload'}</Link>
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem className="min-w-0">
            <BreadcrumbPage className="truncate">{titleName}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle
        title="Instance"
        className="flex-col items-start gap-3 sm:flex-row sm:items-center"
        description={
          <span className="break-all">
            {titleName}
            {locationLabel ? (
              <>
                <span className="text-muted-foreground" aria-hidden>
                  {' '}
                  ·{' '}
                </span>
                <span className="text-muted-foreground" title={locationTooltip}>
                  {locationLabel}
                </span>
              </>
            ) : !instance ? (
              <span className="invisible" aria-hidden>
                {' '}
                · Location
              </span>
            ) : null}
          </span>
        }
      />

      <PluginTabs
        tabs={instanceDetailTabs(overviewHref, metricsHref, logsHref)}
        testId="compute-plugin-instance-tabs"
      />

      {children}
    </div>
  );
}
