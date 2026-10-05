/**
 * Shared workload layout chrome: breadcrumbs, title, and tab bar.
 * Used by the splat layout at `:workloadName/*`.
 *
 * The workload's public URL sits beside its name, so where to reach it is
 * visible from every tab rather than buried in the overview.
 */
import { PluginTabs, type PluginTab } from './plugin-tabs';
import { Button, LinkButton } from '@datum-cloud/datum-ui/button';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@datum-cloud/datum-ui/breadcrumb';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { HomeIcon, SquareArrowOutUpRightIcon, Trash2Icon } from 'lucide-react';
import { Link } from 'react-router';

export function workloadDetailTabs(
  overviewHref: string,
  metricsHref: string,
  logsHref: string
): PluginTab[] {
  return [
    { label: 'Overview', href: overviewHref },
    { label: 'Deployments' },
    { label: 'Metrics', href: metricsHref },
    { label: 'Logs', href: logsHref },
    { label: 'Activity' },
  ];
}

const VISIT_ICON = <Icon icon={SquareArrowOutUpRightIcon} size={12} />;

/**
 * The page's "go to my app" action, alongside the URL by the name. A real link
 * once the workload serves; disabled, with the reason on hover, while it comes
 * up; and holding its place while that's still being worked out.
 */
function VisitButton({
  visit,
  loading,
}: {
  visit?: { href: string; live: boolean; status?: string };
  loading: boolean;
}) {
  if (visit?.live) {
    return (
      <LinkButton
        href={visit.href}
        target="_blank"
        rel="noopener noreferrer"
        type="secondary"
        theme="solid"
        size="xs"
        icon={VISIT_ICON}
        iconPosition="right"
        data-e2e="workload-visit">
        Visit
      </LinkButton>
    );
  }
  if (!visit && !loading) return null;
  const button = (
    <Button
      type="secondary"
      theme="solid"
      size="xs"
      icon={VISIT_ICON}
      iconPosition="right"
      disabled
      aria-busy={loading || undefined}
      data-e2e="workload-visit">
      Visit
    </Button>
  );
  // A disabled button gets no pointer events, so the tooltip hangs off a wrapper.
  return visit?.status ? (
    <Tooltip message={visit.status}>
      <span tabIndex={0} className="inline-flex">
        {button}
      </span>
    </Tooltip>
  ) : (
    button
  );
}

export function WorkloadPageChrome({
  projectHref,
  workloadsHref,
  overviewHref,
  metricsHref,
  logsHref,
  titleName,
  url,
  urlLoading = false,
  visit,
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
  /** The workload's public URL (`WorkloadUrl`), when a load balancer publishes it. */
  url?: React.ReactNode;
  /**
   * Still finding out whether there is a URL: hold its place (and the Visit
   * button's) so nothing jumps.
   */
  urlLoading?: boolean;
  /** Opens the workload's URL. `live` is false until it's serving; `status` says why. */
  visit?: { href: string; live: boolean; status?: string };
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
        title="Workload"
        className="flex-col items-start gap-3 sm:flex-row sm:items-center"
        // PageTitle pads its actions (`pl-2`) to space them from an inline title.
        // Stacked on a phone that just indents them; `gap-3` above already
        // spaces the two side by side. (`px-0` because `pl-0` isn't compiled.)
        actionsClassName="px-0"
        description={
          url || urlLoading ? (
            // On a phone the URL always takes its own line under the name, so its
            // loading placeholder sits exactly where it will land. From `sm` up
            // they share a line, with a separator.
            <span className="flex flex-col items-start gap-x-2 gap-y-1 sm:flex-row sm:flex-wrap sm:items-center">
              <span style={{ overflowWrap: 'anywhere' }}>{titleName}</span>
              <span aria-hidden className="text-muted-foreground hidden sm:inline">
                ·
              </span>
              {url ?? (
                <Skeleton
                  aria-hidden
                  data-testid="compute-plugin-workload-url-loading"
                  style={{ display: 'inline-block', width: '18em', maxWidth: '100%', height: '1em' }}
                />
              )}
            </span>
          ) : (
            titleName
          )
        }
        descriptionClassName={url || urlLoading ? undefined : 'break-all'}
        actions={
          visit || urlLoading || onDelete || deleteLoading ? (
            <div className="flex items-center gap-2">
              <VisitButton visit={visit} loading={urlLoading} />
              {onDelete || deleteLoading ? (
                <Button
                  type="danger"
                  theme="outline"
                  size="xs"
                  icon={<Icon icon={Trash2Icon} size={12} />}
                  onClick={onDelete}
                  disabled={!onDelete}
                  aria-busy={deleteLoading || undefined}
                  data-e2e="workload-delete">
                  Delete
                </Button>
              ) : null}
            </div>
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
