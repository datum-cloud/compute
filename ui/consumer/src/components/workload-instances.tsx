/**
 * The workload Overview's Instances section: a bordered table, one row per
 * instance. Each row says whether the instance is serving and why not, where
 * it runs, how to reach it on the private network, what it was given, how
 * old it is, and links to its Logs, Shell (where the runtime has one) and
 * Metrics.
 */
import { formatLocationName, formatLocationTooltip, type LocationIndex } from '../lib/locations';
import type { ApiError } from '../lib/api';
import { useShellAvailable } from '../lib/use-shell-available';
import {
  formatAge,
  instanceReadiness,
  instanceResourcesText,
  instancesSectionState,
  privateNameState,
  STATUS_TONE_COLOR,
} from '../lib/workload-presenters';
import type { Instance } from '../schema';
import { CopyValue } from './copy-value';
import { InstanceRowsSkeleton } from './skeletons';
import { QuietNotice, SectionFrame } from './surface';
import { LinkButton } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { ChartLineIcon, LogsIcon, SquareTerminalIcon, type LucideIcon } from 'lucide-react';
import { Link } from 'react-router';

/** The address a person would dial first: the in-network one, else the external. */
function primaryAddress(instance: Instance): string | undefined {
  return instance.addresses[0] ?? instance.internalIP ?? instance.externalIPs[0] ?? instance.externalIP;
}

// Inline: the host compiles none of these arbitrary sizes.
const SUBLINE_STYLE = { fontSize: '11px' } as const;
const TABLE_STYLE = { tableLayout: 'fixed', minWidth: '48rem' } as const;
/**
 * Instance, region, private address, CPU and memory, age. Actions take what
 * is left, so a third button (Shell) has room.
 */
const COLUMN_WIDTHS = ['29%', '10%', '30%', '10%', '5%', undefined];

/** Where to connect to an instance: its published name when it has one, its address under it. */
function PrivateAddress({ instance }: { instance: Instance }) {
  const state = privateNameState(instance);
  const address = primaryAddress(instance);
  const subline = (text: string, mono: boolean) => (
    <span
      className={mono ? 'text-muted-foreground truncate font-mono' : 'text-muted-foreground truncate'}
      style={SUBLINE_STYLE}
      title={mono ? instance.addresses.join('\n') || text : text}>
      {text}
    </span>
  );
  if (state.kind === 'names') {
    const [first, ...rest] = state.names;
    return (
      <span className="flex min-w-0 flex-col gap-0.5" data-testid="compute-plugin-instance-private-name">
        <span className="flex min-w-0 items-center gap-1" title={state.names.join('\n')}>
          <CopyValue value={first} />
          {rest.length > 0 ? <span className="text-muted-foreground shrink-0 text-xs">+{rest.length}</span> : null}
        </span>
        {address ? subline(address, true) : null}
      </span>
    );
  }
  return (
    <span className="flex min-w-0 flex-col gap-0.5" data-testid="compute-plugin-instance-private-name">
      {address ? <CopyValue value={address} /> : null}
      {subline(state.reason, false)}
    </span>
  );
}

function RowAction({ href, icon, label, instanceName }: { href: string; icon: LucideIcon; label: string; instanceName: string }) {
  return (
    <LinkButton
      as={Link}
      href={href}
      type="quaternary"
      theme="outline"
      size="xs"
      icon={<Icon icon={icon} size={12} aria-hidden />}
      aria-label={`${label} for ${instanceName}`}
      data-e2e={`compute-instance-${label.toLowerCase()}`}>
      {label}
    </LinkButton>
  );
}

function InstanceRow({
  instance,
  href,
  projectId,
  locationIndex,
  now,
}: {
  instance: Instance;
  href: string;
  projectId?: string;
  locationIndex: LocationIndex;
  now: number;
}) {
  const readiness = instanceReadiness(instance);
  // Hidden until known: a runtime without a shell (a unikernel) never gets one.
  const shell = useShellAvailable(projectId, instance);
  const state =
    readiness.tone === 'ok'
      ? undefined
      : readiness.reason
        ? `${readiness.label}: ${readiness.reason}`
        : readiness.label;
  return (
    <TableRow data-testid="compute-plugin-instance-row">
      <TableCell className="px-4 py-2.5">
        <span className="flex min-w-0 flex-col gap-0.5">
          <Link to={href} className="flex min-w-0 items-start gap-2 hover:underline">
            <span
              className="size-2 shrink-0 rounded-full"
              // Centered on the first line (`text-xs`, 1rem) of a name that may wrap.
              style={{ background: STATUS_TONE_COLOR[readiness.tone], marginTop: '0.25rem' }}
              aria-hidden
            />
            {/* Wrapped rather than cut: the name is how a person finds this instance elsewhere. */}
            <span className="min-w-0 font-mono text-xs font-medium break-all whitespace-normal">
              {instance.name}
            </span>
          </Link>
          {/* The dot's meaning in words, so state isn't carried by color alone. */}
          {state ? (
            <span className="text-muted-foreground truncate pl-4" style={SUBLINE_STYLE} title={state}>
              {state}
            </span>
          ) : (
            <span className="sr-only">{readiness.label}</span>
          )}
        </span>
      </TableCell>
      <TableCell className="text-muted-foreground truncate text-xs">
        <span title={instance.location ? formatLocationTooltip(instance.location, locationIndex) : undefined}>
          {instance.location ? formatLocationName(instance.location, locationIndex) : '—'}
        </span>
      </TableCell>
      <TableCell>
        <PrivateAddress instance={instance} />
      </TableCell>
      <TableCell className="text-muted-foreground text-xs">{instanceResourcesText(instance) ?? '—'}</TableCell>
      <TableCell className="text-muted-foreground font-mono text-xs" title={instance.createdAt.toLocaleString()}>
        {formatAge(instance.createdAt, now)}
      </TableCell>
      <TableCell className="pr-4">
        <span className="flex justify-end gap-1.5">
          <RowAction href={`${href}/logs`} icon={LogsIcon} label="Logs" instanceName={instance.name} />
          {shell ? (
            <RowAction href={`${href}/shell`} icon={SquareTerminalIcon} label="Shell" instanceName={instance.name} />
          ) : null}
          <RowAction href={`${href}/metrics`} icon={ChartLineIcon} label="Metrics" instanceName={instance.name} />
        </span>
      </TableCell>
    </TableRow>
  );
}

/**
 * The Instances section: the table once any instance is known, placeholder
 * rows while the query is unanswered, and a quiet notice for none or a failure.
 */
export function WorkloadInstances({
  instances,
  loading,
  error,
  projectId,
  locationIndex,
  instanceHref,
  deploying,
  deleting,
  now,
}: {
  instances: readonly Instance[];
  /** The instances query hasn't answered yet. */
  loading: boolean;
  error: ApiError | null;
  /** Decides, with each instance's runtime, whether it offers a shell. */
  projectId?: string;
  locationIndex: LocationIndex;
  instanceHref: (name: string) => string;
  /** Still coming up, so no instances yet is expected. */
  deploying: boolean;
  deleting: boolean;
  now: number;
}) {
  const state = instancesSectionState({ count: instances.length, loading, error, deploying, deleting });
  return (
    <SectionFrame
      title="Instances"
      count={instances.length}
      isLoading={state.kind === 'loading'}
      testId="compute-plugin-workload-instances">
      {state.kind === 'loading' ? (
        <InstanceRowsSkeleton />
      ) : state.kind === 'notice' ? (
        <QuietNotice testId="compute-plugin-instances-empty" title={state.title} description={state.description} />
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <Table style={TABLE_STYLE}>
            <colgroup>
              {COLUMN_WIDTHS.map((width, index) => (
                <col key={index} style={{ width }} />
              ))}
            </colgroup>
            <TableHeader className="bg-muted/50">
              <TableRow className="text-xs hover:bg-transparent">
                <TableHead className="h-9 px-4">Instance</TableHead>
                <TableHead className="h-9">Region</TableHead>
                <TableHead className="h-9">Private address</TableHead>
                <TableHead className="h-9">CPU · Mem</TableHead>
                <TableHead className="h-9">Age</TableHead>
                <TableHead className="h-9 pr-4">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {instances.map((instance) => (
                <InstanceRow
                  key={instance.uid || instance.name}
                  instance={instance}
                  href={instanceHref(instance.name)}
                  projectId={projectId}
                  locationIndex={locationIndex}
                  now={now}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </SectionFrame>
  );
}
