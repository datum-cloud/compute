/**
 * Shared workload layout chrome: breadcrumbs, title, and tab bar.
 * Used by the splat layout at `:workloadName/*`.
 */
import { PluginTabs, type PluginTab } from './plugin-tabs';
import { Button } from '@datum-cloud/datum-ui/button';
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
import { HomeIcon, Trash2Icon } from 'lucide-react';
import { Link } from 'react-router';

export function workloadDetailTabs(
  overviewHref: string,
  instancesHref: string,
  metricsHref: string,
  logsHref: string
): PluginTab[] {
  // Deployments have no tab of their own: each is a column and search term on
  // the Instances table.
  return [
    { label: 'Overview', href: overviewHref },
    { label: 'Instances', href: instancesHref },
    { label: 'Metrics', href: metricsHref },
    { label: 'Logs', href: logsHref },
    { label: 'Activity' },
  ];
}

export function WorkloadPageChrome({
  projectHref,
  workloadsHref,
  overviewHref,
  instancesHref,
  metricsHref,
  logsHref,
  titleName,
  onDelete,
  children,
}: {
  projectHref: string;
  workloadsHref: string;
  overviewHref: string;
  instancesHref: string;
  metricsHref: string;
  logsHref: string;
  titleName: string;
  /** Omitted when the user can't delete this workload — the button is hidden. */
  onDelete?: () => void;
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
          onDelete ? (
            <Button
              type="danger"
              theme="outline"
              size="xs"
              icon={<Icon icon={Trash2Icon} size={12} />}
              onClick={onDelete}
              data-e2e="workload-delete">
              Delete
            </Button>
          ) : undefined
        }
      />

      <PluginTabs
        tabs={workloadDetailTabs(overviewHref, instancesHref, metricsHref, logsHref)}
        testId="compute-plugin-workload-tabs"
      />

      {children}
    </div>
  );
}
