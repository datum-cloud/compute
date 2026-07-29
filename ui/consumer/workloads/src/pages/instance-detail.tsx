/**
 * `portal.page/project` extension at
 * `workloads/:workloadName/instances/:instanceName`, exposed as
 * `InstanceDetail`.
 *
 * Ported from cloud-portal PR #1315's
 * `app/routes/project/detail/compute/workloads/detail/instances/index.tsx`
 * (Overview tab only — its Settings tab is intentionally not included in
 * v1, matching the plugin's read-only scope).
 *
 * Rewritten for the plugin runtime:
 *  - `useInstance` polls `/api/proxy/…` (see `lib/api.ts`) instead of PR
 *    #1315's generated-SDK service.
 *  - No server loader — loading/error/restricted states are handled inline.
 *  - PR #1315's `TextCopy` (portal-internal, `@/components/text-copy`) is
 *    replaced by a small local `CopyableText` using the same
 *    `useCopyToClipboard` hook `cli-section.tsx` uses (from
 *    `@datum-cloud/datum-ui/hooks`).
 */
import { CommandBlock } from '../components/cli-section';
import { PluginTabs } from '../components/plugin-tabs';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useInstance } from '../lib/api';
import { formatUptime, splitSlashValue } from '../lib/format';
import { instanceStatusToBadgeType, type InstanceCondition } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@datum-cloud/datum-ui/breadcrumb';
import { Card, CardContent, CardHeader, CardTitle } from '@datum-cloud/datum-ui/card';
import { useCopyToClipboard } from '@datum-cloud/datum-ui/hooks';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { toast } from '@datum-cloud/datum-ui/toast';
import {
  BoxIcon,
  CheckIcon,
  CheckCircle2Icon,
  CircleIcon,
  CopyIcon,
  CpuIcon,
  GlobeIcon,
  HomeIcon,
  LinkIcon,
  SquareTerminalIcon,
  TimerIcon,
  WifiIcon,
  XCircleIcon,
} from 'lucide-react';
import { useState } from 'react';
import { useLocation, useParams } from 'react-router';

const INSTANCE_TABS = ['Overview', 'Metrics', 'Logs', 'Manage', 'Activity'];

function StatCard({
  icon,
  label,
  value,
  sub,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  sub?: string;
}) {
  return (
    <Card className="rounded-xl py-0 shadow-none">
      <div className="flex items-start gap-3 p-4">
        <div className="text-muted-foreground mt-0.5 shrink-0">{icon}</div>
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
            {label}
          </span>
          <span className="text-foreground truncate font-semibold">{value}</span>
          {sub && <span className="text-muted-foreground truncate text-xs">{sub}</span>}
        </div>
      </div>
    </Card>
  );
}

function ConditionIcon({ status }: { status: InstanceCondition['status'] }) {
  if (status === 'True')
    return <CheckCircle2Icon className="text-success mt-0.5 size-4 shrink-0" />;
  if (status === 'False')
    return <XCircleIcon className="text-destructive mt-0.5 size-4 shrink-0" />;
  return <CircleIcon className="text-muted-foreground mt-0.5 size-4 shrink-0" />;
}

/** Minimal local stand-in for the portal's internal `TextCopy` component. */
function CopyableText({ value, text, className }: { value: string; text?: string; className?: string }) {
  const [, copy] = useCopyToClipboard();
  const [copied, setCopied] = useState(false);

  return (
    <button
      type="button"
      onClick={() =>
        copy(value).then(() => {
          toast.success('Copied to clipboard');
          setCopied(true);
          setTimeout(() => setCopied(false), 2000);
        })
      }
      className={`inline-flex min-w-0 items-center gap-1.5 text-left ${className ?? ''}`}>
      <span className="min-w-0 truncate">{text ?? value}</span>
      {copied ? (
        <CheckIcon className="size-3.5 shrink-0" />
      ) : (
        <CopyIcon className="size-3.5 shrink-0 opacity-60" />
      )}
    </button>
  );
}

export default function InstanceDetail() {
  const { projectId, workloadName, instanceName } = useParams<{
    projectId: string;
    workloadName: string;
    instanceName: string;
  }>();
  const location = useLocation();

  const { data: instance, isLoading, error, refetch } = useInstance(projectId, instanceName);

  const basePath = location.pathname.replace(/\/$/, '');
  const instancesHref = basePath.replace(/\/instances\/[^/]+$/, '');
  const workloadsHref = instancesHref.replace(/\/[^/]+$/, '');
  const projectHref = projectId ? `/project/${projectId}` : '/';

  if (isLoading) return <LoadingSkeleton />;

  if (error || !instance) {
    return (
      <ErrorOrRestrictedState
        error={error}
        restrictedMessage="You don't have permission to view this instance."
        onRetry={() => void refetch()}
      />
    );
  }

  const { main: imageShort, sub: imageRegistry } = splitSlashValue(instance.image ?? '');
  const { main: instanceTypeShort, sub: instanceTypeProvider } = splitSlashValue(
    instance.instanceType ?? ''
  );

  const firstPort = instance.ports[0];

  return (
    <div data-testid="compute-plugin-instance-detail" className="flex flex-col gap-4 p-6">
      <Breadcrumb>
        <BreadcrumbList>
          <BreadcrumbItem>
            <BreadcrumbLink href={projectHref}>
              <HomeIcon className="size-4" />
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbLink href={workloadsHref}>Workloads</BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbLink href={instancesHref}>{workloadName}</BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbPage>{instance.name}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle
        title={instance.name}
        description={
          <span className="mt-1 flex items-center gap-2">
            <Badge type={instanceStatusToBadgeType(instance.status)} theme="light">
              {instance.status}
            </Badge>
            {instance.city && <span className="text-muted-foreground text-xs">{instance.city}</span>}
          </span>
        }
      />

      <PluginTabs tabs={INSTANCE_TABS} testId="compute-plugin-instance-tabs" />

      {/* Stat tiles */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard
          icon={<TimerIcon className="size-4" />}
          label="Uptime"
          value={formatUptime(instance.createdAt)}
          sub={`Since ${instance.createdAt.toLocaleDateString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}`}
        />
        <StatCard
          icon={<GlobeIcon className="size-4" />}
          label="Region"
          value={instance.city ?? '—'}
        />
        {instance.instanceType && (
          <StatCard
            icon={<CpuIcon className="size-4" />}
            label="Instance Type"
            value={instanceTypeShort}
            sub={instanceTypeProvider}
          />
        )}
        {instance.image && (
          <StatCard
            icon={<BoxIcon className="size-4" />}
            label="Image"
            value={imageShort}
            sub={imageRegistry}
          />
        )}
      </div>

      {/* Main two-column layout */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        {/* Network */}
        <Card className="rounded-xl shadow-none">
          <CardHeader>
            <CardTitle className="text-base font-semibold">Network</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4 px-5 pt-0 pb-5">
            {instance.externalIP && (
              <div className="flex flex-col gap-1.5">
                <span className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
                  External Endpoint
                </span>
                <div className="bg-muted/50 flex items-start gap-2 rounded-lg px-3 py-2.5">
                  <LinkIcon className="text-muted-foreground mt-0.5 size-3.5 shrink-0" />
                  <CopyableText
                    value={instance.externalIP}
                    className="text-primary min-w-0 text-sm break-all"
                  />
                </div>
              </div>
            )}
            {instance.internalIP && (
              <div className="flex flex-col gap-1.5">
                <span className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
                  Internal IP
                </span>
                <div className="bg-muted/50 flex items-center gap-2 rounded-lg px-3 py-2.5">
                  <WifiIcon className="text-muted-foreground size-3.5 shrink-0" />
                  <CopyableText
                    value={instance.internalIP}
                    text={firstPort ? `${instance.internalIP} :${firstPort}` : instance.internalIP}
                    className="min-w-0 truncate font-mono text-sm"
                  />
                </div>
              </div>
            )}
            {!instance.externalIP && !instance.internalIP && (
              <p className="text-muted-foreground text-sm">No network assignments yet</p>
            )}
          </CardContent>
        </Card>

        {/* Right column */}
        <div className="flex flex-col gap-6">
          {/* Health Conditions */}
          {instance.conditions.length > 0 && (
            <Card className="rounded-xl shadow-none">
              <CardHeader>
                <CardTitle className="text-base font-semibold">Health Conditions</CardTitle>
              </CardHeader>
              <CardContent className="px-5 pt-0 pb-5">
                <dl className="divide-border divide-y text-sm">
                  {instance.conditions.map((c) => (
                    <div key={c.type} className="flex items-start gap-2.5 py-2.5">
                      <ConditionIcon status={c.status} />
                      <div className="flex min-w-0 flex-col">
                        <span className="font-medium">{c.type}</span>
                        {(c.message || c.reason) && (
                          <span className="text-muted-foreground text-xs">
                            {c.message || c.reason}
                          </span>
                        )}
                      </div>
                    </div>
                  ))}
                </dl>
              </CardContent>
            </Card>
          )}

          {/* Runtime */}
          <Card className="rounded-xl shadow-none">
            <CardHeader>
              <CardTitle className="text-base font-semibold">Runtime</CardTitle>
            </CardHeader>
            <CardContent className="px-5 pt-0 pb-5">
              <dl className="divide-border divide-y text-sm">
                {instance.image && (
                  <div className="flex items-baseline justify-between gap-4 py-2.5">
                    <dt className="text-muted-foreground shrink-0">Image</dt>
                    <dd className="min-w-0 truncate text-right font-mono text-xs">
                      {instance.image}
                    </dd>
                  </div>
                )}
                {instance.ports.length > 0 && (
                  <div className="flex items-baseline justify-between gap-4 py-2.5">
                    <dt className="text-muted-foreground shrink-0">Ports</dt>
                    <dd className="font-mono text-xs">{instance.ports.join(', ')}</dd>
                  </div>
                )}
                {instance.placement && (
                  <div className="flex items-baseline justify-between gap-4 py-2.5">
                    <dt className="text-muted-foreground shrink-0">Placement</dt>
                    <dd>{instance.placement}</dd>
                  </div>
                )}
                <div className="flex items-baseline justify-between gap-4 py-2.5">
                  <dt className="text-muted-foreground shrink-0">Created</dt>
                  <dd>
                    {instance.createdAt.toLocaleDateString('en-US', {
                      day: '2-digit',
                      month: 'short',
                      year: 'numeric',
                      hour: '2-digit',
                      minute: '2-digit',
                      timeZoneName: 'short',
                    })}
                  </dd>
                </div>
              </dl>
            </CardContent>
          </Card>
        </div>
      </div>

      {/* CLI reference */}
      <Card className="rounded-xl shadow-none">
        <CardHeader className="flex flex-row items-center gap-2">
          <SquareTerminalIcon className="text-muted-foreground size-4" />
          <CardTitle className="text-base font-semibold">datumctl Commands</CardTitle>
          <span className="text-muted-foreground ml-auto text-xs">CLI</span>
        </CardHeader>
        <CardContent className="grid grid-cols-1 gap-4 px-5 pt-0 pb-5 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <span className="text-muted-foreground text-xs">Get instance</span>
            <CommandBlock value={`datumctl compute instances get ${instance.name}`} />
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-muted-foreground text-xs">Describe instance</span>
            <CommandBlock value={`datumctl compute instances describe ${instance.name}`} />
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-muted-foreground text-xs">List instances</span>
            <CommandBlock value={`datumctl compute instances list --workload=${workloadName ?? ''}`} />
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-muted-foreground text-xs">Get workload</span>
            <CommandBlock value={`datumctl compute workloads get ${workloadName ?? ''}`} />
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
