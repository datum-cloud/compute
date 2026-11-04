/**
 * The workload Overview's open sections: how to reach the workload
 * privately, its running instances, and its details. Laid out like the
 * portal's project home: headings and hairline rows, no boxes.
 */
import { useShellAvailable } from '../lib/use-shell-available';
import { formatLocationTooltip, type LocationIndex } from '../lib/locations';
import { formatElapsed, instanceStatusLabel } from '../lib/workload-presenters';
import { instanceNotReady, instancePrivateName, type PrivateReach } from '../lib/workload-overview';
import type { Instance } from '../schema';
import { useCopyToClipboard } from '@datum-cloud/datum-ui/hooks';
import { Icon, SpinnerIcon } from '@datum-cloud/datum-ui/icons';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { cn } from '@datum-cloud/datum-ui/utils';
import {
  ChartLineIcon,
  CheckIcon,
  CopyIcon,
  LockIcon,
  ScrollTextIcon,
  TerminalIcon,
  type LucideIcon,
} from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { Link } from 'react-router';

/** Dot colour per instance status. Inline: the host compiles no `bg-(--color-badge-*)`. */
const STATUS_DOT: Record<Instance['status'], string> = {
  Available: 'var(--color-badge-success)',
  Pending: 'var(--color-badge-warning)',
  Failed: 'var(--color-badge-danger)',
  Unknown: 'var(--color-badge-muted)',
};

/** A value in mono with a button that copies it. */
export function CopyValue({ value, className }: { value: string; className?: string }) {
  const [, copy] = useCopyToClipboard();
  const [copied, flash] = useFlash();
  return (
    <span className={cn('inline-flex min-w-0 items-center gap-1.5', className)}>
      <span className="truncate font-mono text-xs" title={value}>
        {value}
      </span>
      <Tooltip message={copied ? 'Copied' : 'Copy'}>
        <button
          type="button"
          aria-label={`Copy ${value}`}
          className="text-muted-foreground hover:text-foreground shrink-0 rounded p-0.5"
          onClick={() => {
            void copy(value).then(flash);
          }}>
          <Icon icon={copied ? CheckIcon : CopyIcon} size={12} />
        </button>
      </Tooltip>
    </span>
  );
}

/** True for a moment after `flash()`, for a "copied" tick. */
function useFlash(ms = 1500): [boolean, () => void] {
  const [on, setOn] = useState(false);
  useEffect(() => {
    if (!on) return;
    const timer = setTimeout(() => setOn(false), ms);
    return () => clearTimeout(timer);
  }, [on, ms]);
  return [on, () => setOn(true)];
}

/**
 * How to reach a workload no load balancer publishes, for the status line:
 * its one instance's address, or the network and port when it has several.
 */
export function PrivateReachLine({ reach }: { reach: PrivateReach }) {
  return (
    <span className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs">
      <Icon icon={LockIcon} size={12} className="shrink-0" />
      Private
      {reach.kind === 'address' ? (
        <CopyValue value={reach.address} className="text-foreground" />
      ) : (
        <span>
          {reach.network ? (
            <>
              on <span className="text-foreground font-mono">{reach.network}</span>
            </>
          ) : null}
          {reach.port !== undefined ? (
            <>
              {reach.network ? ', ' : ''}port <span className="text-foreground font-mono">{reach.port}</span>
            </>
          ) : null}
        </span>
      )}
    </span>
  );
}

/** A section with a small heading, a count, and an optional note on the right. */
function SectionHeading({
  id,
  title,
  count,
  note,
}: {
  id: string;
  title: string;
  count?: number;
  note?: ReactNode;
}) {
  return (
    <div className="flex items-center gap-2">
      <h2 id={id} className="text-sm font-medium">
        {title}
      </h2>
      {count !== undefined ? (
        <span className="text-muted-foreground text-xs tabular-nums">{count}</span>
      ) : null}
      {note ? <span className="text-muted-foreground ml-auto text-xs">{note}</span> : null}
    </div>
  );
}

function RowAction({ href, icon, label }: { href: string; icon: LucideIcon; label: string }) {
  return (
    <Link
      to={href}
      className="hover:bg-muted/50 inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs transition-colors"
      data-e2e={`compute-instance-${label.toLowerCase()}`}>
      <Icon icon={icon} size={12} className="text-muted-foreground" />
      {label}
    </Link>
  );
}

/** Where to connect to an instance: its published name when it has one, its IP otherwise. */
function InstanceAddress({ instance }: { instance: Instance }) {
  const name = instancePrivateName(instance);
  const ip = instance.internalIP;
  if (name?.ready) {
    return (
      <span className="flex min-w-0 flex-col gap-0.5">
        <CopyValue value={name.hostname} />
        {ip ? <span className="text-muted-foreground truncate font-mono text-[11px]">{ip}</span> : null}
      </span>
    );
  }
  if (!ip) return <span className="text-muted-foreground">—</span>;
  return (
    <span className="flex min-w-0 flex-col gap-0.5">
      <CopyValue value={ip} />
      {name?.message ? (
        <span className="text-muted-foreground truncate text-[11px]" title={name.message}>
          {name.message}
        </span>
      ) : null}
    </span>
  );
}

function InstanceRow({
  instance,
  projectId,
  locationIndex,
  instanceHref,
  now,
}: {
  instance: Instance;
  projectId?: string;
  locationIndex: LocationIndex;
  instanceHref: (name: string) => string;
  now: number;
}) {
  const shell = useShellAvailable(projectId, instance);
  const href = instanceHref(instance.name);
  const notReady = instanceNotReady(instance);
  const resources =
    instance.cpu || instance.memory
      ? [instance.cpu ? `${instance.cpu} vCPU` : undefined, instance.memory].filter(Boolean).join(' · ')
      : '—';
  return (
    <tr className="border-t align-middle first:border-t-0" data-testid="compute-plugin-instance-row">
      <td className="max-w-0 py-2.5 pr-4 pl-4">
        <Link to={href} className="flex min-w-0 items-center gap-2 hover:underline">
          <span
            className="size-2 shrink-0 rounded-full"
            style={{ background: notReady ? STATUS_DOT.Pending : STATUS_DOT[instance.status] }}
            aria-hidden
          />
          <span className="truncate font-mono text-xs font-medium">{instance.name}</span>
          {instance.status !== 'Available' ? (
            <span className="text-muted-foreground shrink-0 text-xs">{instanceStatusLabel(instance)}</span>
          ) : null}
        </Link>
        {notReady ? (
          <span className="text-muted-foreground block truncate pl-4 text-[11px]" title={notReady}>
            Not ready: {notReady}
          </span>
        ) : null}
      </td>
      <td className="text-muted-foreground py-2.5 pr-4 font-mono text-xs">
        {instance.location ? (
          <span title={formatLocationTooltip(instance.location, locationIndex)}>{instance.location}</span>
        ) : (
          '—'
        )}
      </td>
      <td className="max-w-0 py-2.5 pr-4">
        <InstanceAddress instance={instance} />
      </td>
      <td className="text-muted-foreground py-2.5 pr-4 text-xs whitespace-nowrap">{resources}</td>
      <td className="text-muted-foreground py-2.5 pr-4 font-mono text-xs whitespace-nowrap">
        {formatElapsed(instance.createdAt, now)}
      </td>
      <td className="py-2.5 pr-4">
        <span className="flex justify-end gap-1.5">
          <RowAction href={`${href}/logs`} icon={ScrollTextIcon} label="Logs" />
          {shell ? <RowAction href={`${href}/shell`} icon={TerminalIcon} label="Shell" /> : null}
          <RowAction href={`${href}/metrics`} icon={ChartLineIcon} label="Metrics" />
        </span>
      </td>
    </tr>
  );
}

/** Instance, region, private address, CPU and memory, age, actions. */
const COLUMN_WIDTHS = ['24%', '11%', '31%', '11%', '6%', '17%'];

/** The workload's running instances, the Overview's main section. */
export function InstancesSection({
  instances,
  projectId,
  locationIndex,
  deploying,
  instanceHref,
  now = Date.now(),
}: {
  instances: Instance[];
  projectId?: string;
  locationIndex: LocationIndex;
  /** The workload is still coming up, so no instances yet is expected. */
  deploying: boolean;
  instanceHref: (name: string) => string;
  now?: number;
}) {
  return (
    <section
      aria-labelledby="compute-instances-heading"
      className="flex min-w-0 flex-col gap-2"
      data-testid="compute-plugin-instances">
      <SectionHeading
        id="compute-instances-heading"
        title="Instances"
        count={instances.length}
      />
      {instances.length === 0 ? (
        <p
          className="text-muted-foreground flex items-center gap-2 rounded-lg border px-4 py-4 text-sm"
          data-testid={deploying ? 'compute-plugin-instances-deploying' : undefined}>
          {deploying ? (
            <>
              <SpinnerIcon size="xs" aria-hidden />
              Instances are being created…
            </>
          ) : (
            'No running instances'
          )}
        </p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          {/* Inline widths: the host compiles none of these arbitrary values. */}
          <table className="w-full table-fixed text-left text-sm" style={{ minWidth: '48rem' }}>
            <colgroup>
              {COLUMN_WIDTHS.map((width, index) => (
                <col key={index} style={{ width }} />
              ))}
            </colgroup>
            <thead>
              <tr className="bg-muted/40 text-muted-foreground border-b text-xs">
                <th className="py-2 pr-4 pl-4 font-medium">Instance</th>
                <th className="py-2 pr-4 font-medium">Region</th>
                <th className="py-2 pr-4 font-medium">Private address</th>
                <th className="py-2 pr-4 font-medium">CPU · Mem</th>
                <th className="py-2 pr-4 font-medium">Age</th>
                <th className="py-2 pr-4">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {instances.map((instance) => (
                <InstanceRow
                  key={instance.uid || instance.name}
                  instance={instance}
                  projectId={projectId}
                  locationIndex={locationIndex}
                  instanceHref={instanceHref}
                  now={now}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

/** A detail row. One with no value (undefined, false, or empty) is left out. */
export type DetailItem = { label: string; value?: ReactNode; mono?: boolean };

const hasValue = (item: DetailItem) =>
  item.value !== undefined && item.value !== null && item.value !== false && item.value !== '';

/** Label and value pairs in two open columns, with hairline rows. */
export function DetailsSection({ items: all }: { items: DetailItem[] }) {
  const items = all.filter(hasValue);
  const half = Math.ceil(items.length / 2);
  const columns = [items.slice(0, half), items.slice(half)];
  return (
    <section
      aria-labelledby="compute-details-heading"
      className="flex min-w-0 flex-col gap-2"
      data-testid="compute-plugin-workload-details">
      <SectionHeading id="compute-details-heading" title="Details" />
      <div className="grid grid-cols-1 gap-x-8 md:grid-cols-2">
        {columns.map((column, index) => (
          <dl key={index} className="flex min-w-0 flex-col">
            {column.map((item) => (
              <div key={item.label} className="flex min-w-0 items-baseline gap-4 border-t py-2.5">
                <dt className="text-muted-foreground shrink-0 text-xs">{item.label}</dt>
                <dd className={cn('ml-auto min-w-0 truncate text-right text-xs', item.mono && 'font-mono')}>
                  {item.value}
                </dd>
              </div>
            ))}
          </dl>
        ))}
      </div>
    </section>
  );
}
