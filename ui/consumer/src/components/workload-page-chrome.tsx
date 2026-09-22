/**
 * Shared workload layout chrome: breadcrumbs, title, and tab bar.
 * Used by the splat layout at `:workloadName/*`.
 */
import { PluginTabs, type PluginTab } from './plugin-tabs';
import { workloadHealthToBadgeType, type Workload } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@datum-cloud/datum-ui/breadcrumb';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { HomeIcon } from 'lucide-react';
import { Link } from 'react-router';

export function workloadDetailTabs(overviewHref: string, metricsHref: string): PluginTab[] {
  return [
    { label: 'Overview', href: overviewHref },
    { label: 'Deployments' },
    { label: 'Metrics', href: metricsHref },
    { label: 'Activity' },
  ];
}

export function WorkloadPageChrome({
  projectHref,
  workloadsHref,
  overviewHref,
  metricsHref,
  titleName,
  workload,
  children,
}: {
  projectHref: string;
  workloadsHref: string;
  overviewHref: string;
  metricsHref: string;
  titleName: string;
  workload?: Workload | null;
  children: React.ReactNode;
}) {
  return (
    <div data-testid="compute-plugin-workload-detail" className="flex min-w-0 flex-col gap-6">
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
            <BreadcrumbPage className="truncate">{titleName}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle
        title="Workload"
        className="flex-col items-start gap-3 sm:flex-row sm:items-center"
        description={titleName}
        descriptionClassName="break-all"
        actions={
          workload ? (
            <Badge type={workloadHealthToBadgeType(workload.health)} theme="light">
              {workload.health}
            </Badge>
          ) : undefined
        }
      />

      <PluginTabs
        tabs={workloadDetailTabs(overviewHref, metricsHref)}
        testId="compute-plugin-workload-tabs"
      />

      {children}
    </div>
  );
}
