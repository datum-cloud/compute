/**
 * Shared workload layout chrome: breadcrumbs, title, and tab bar.
 * Used by the splat layout at `:workloadName/*`.
 *
 * The title is the workload's own name, with a line under it saying what it
 * runs and where. No URL: a load balancer's hostname belongs to the load
 * balancer, and the Overview lists those. Deleting lives in a quiet menu: it
 * is the page's rarest action, not its loudest.
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@datum-cloud/datum-ui/dropdown';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { EllipsisIcon, HomeIcon, Trash2Icon } from 'lucide-react';
import { Link } from 'react-router';

export function workloadDetailTabs(
  overviewHref: string,
  metricsHref: string,
  logsHref: string
): PluginTab[] {
  return [
    { label: 'Overview', href: overviewHref },
    { label: 'Topology', href: `${overviewHref}/topology` },
    { label: 'Metrics', href: metricsHref },
    { label: 'Logs', href: logsHref },
    { label: 'Activity', href: `${overviewHref}/activity` },
  ];
}

export function WorkloadPageChrome({
  projectHref,
  workloadsHref,
  overviewHref,
  metricsHref,
  logsHref,
  titleName,
  summary,
  onDelete,
  deleteLoading = false,
  children,
}: {
  projectHref: string;
  workloadsHref: string;
  overviewHref: string;
  metricsHref: string;
  logsHref: string;
  titleName: string;
  /** What the workload runs and where, under its name: "Workload · redis:8.10 on app-net in us-central-1". */
  summary?: React.ReactNode;
  /** Omitted when the user can't delete this workload — the button is hidden. */
  onDelete?: () => void;
  /**
   * Still checking whether the user may delete. The button shows disabled
   * meanwhile, so it doesn't pop in and push the layout once the check lands.
   */
  deleteLoading?: boolean;
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
        title={titleName}
        className="flex-col items-start gap-3 sm:flex-row sm:items-center"
        // PageTitle pads its actions (`pl-2`) to space them from an inline title.
        // Stacked on a phone that just indents them; `gap-3` above already
        // spaces the two side by side. (`px-0` because `pl-0` isn't compiled.)
        actionsClassName="px-0"
        // A long generated name has no spaces to wrap at.
        titleClassName="break-all"
        description={summary}
        actions={
          onDelete || deleteLoading ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  type="quaternary"
                  theme="outline"
                  size="xs"
                  aria-label="More actions"
                  aria-busy={deleteLoading || undefined}
                  disabled={!onDelete}
                  icon={<Icon icon={EllipsisIcon} size={14} />}
                  data-e2e="workload-more-actions"
                />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem
                  onSelect={onDelete}
                  className="text-destructive"
                  data-e2e="workload-delete">
                  <Icon icon={Trash2Icon} size={14} />
                  Delete workload
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          ) : undefined
        }
      />

      <PluginTabs
        tabs={workloadDetailTabs(overviewHref, metricsHref, logsHref)}
        testId="compute-plugin-workload-tabs"
      />

      {children}
    </div>
  );
}
