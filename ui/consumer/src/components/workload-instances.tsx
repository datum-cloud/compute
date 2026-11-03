/**
 * The workload Overview's Instances section: one thin row per instance, in
 * the project home page's row shape (`h-10`, `px-2`, `rounded-md`,
 * `hover:bg-accent`). Each row says whether the instance is serving and why
 * not, where it runs, how to reach it on the private network, and links to
 * its Logs, Shell and Metrics.
 */
import { formatLocationName, formatLocationTooltip, type LocationIndex } from '../lib/locations';
import type { ApiError } from '../lib/api';
import { instanceReadiness, instancesSectionState, privateNameState } from '../lib/workload-presenters';
import type { Instance } from '../schema';
import { InstanceRowsSkeleton } from './skeletons';
import { QuietNotice, SectionFrame, StatusDot } from './surface';
import { LinkButton } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { ChartLineIcon, LogsIcon, SquareTerminalIcon } from 'lucide-react';
import { Link } from 'react-router';

/** The address a person would dial first: the in-network one, else the external. */
function primaryAddress(instance: Instance): string | undefined {
  return instance.addresses[0] ?? instance.internalIP ?? instance.externalIPs[0] ?? instance.externalIP;
}

function PrivateName({ instance }: { instance: Instance }) {
  const state = privateNameState(instance);
  if (state.kind === 'missing') {
    return (
      <span className="text-muted-foreground truncate text-xs" data-testid="compute-plugin-instance-private-name">
        {state.reason}
      </span>
    );
  }
  const [first, ...rest] = state.names;
  return (
    <span
      className="truncate font-mono text-xs"
      title={state.names.join('\n')}
      data-testid="compute-plugin-instance-private-name">
      {first}
      {rest.length > 0 ? <span className="text-muted-foreground"> +{rest.length}</span> : null}
    </span>
  );
}

// Inline: the host stylesheet doesn't reliably compile `w-56`.
const COLUMN_STYLE = { width: '14rem', maxWidth: '100%' } as const;

const ROW_ACTIONS = [
  { label: 'Logs', suffix: '/logs', icon: LogsIcon },
  { label: 'Shell', suffix: '/shell', icon: SquareTerminalIcon },
  { label: 'Metrics', suffix: '/metrics', icon: ChartLineIcon },
] as const;

function InstanceRow({
  instance,
  href,
  locationIndex,
}: {
  instance: Instance;
  href: string;
  locationIndex: LocationIndex;
}) {
  const readiness = instanceReadiness(instance);
  const address = primaryAddress(instance);
  return (
    <li
      className="hover:bg-accent flex min-h-10 flex-wrap items-center gap-x-4 gap-y-1 rounded-md px-2 py-1"
      data-testid="compute-plugin-instance-row">
      <span className="flex min-w-0 shrink-0" style={COLUMN_STYLE}>
        <StatusDot tone={readiness.tone} label={readiness.label} reason={readiness.reason} />
      </span>
      <Link
        to={href}
        className="text-foreground min-w-0 shrink-0 truncate font-mono text-sm hover:underline"
        style={COLUMN_STYLE}>
        {instance.name}
      </Link>
      <span
        className="text-muted-foreground w-32 shrink-0 truncate text-xs"
        title={instance.location ? formatLocationTooltip(instance.location, locationIndex) : undefined}>
        {instance.location ? formatLocationName(instance.location, locationIndex) : 'Unknown location'}
      </span>
      <span className="flex min-w-0 flex-1 items-center gap-4">
        <PrivateName instance={instance} />
        {address ? (
          <span className="text-muted-foreground shrink-0 font-mono text-xs" title={instance.addresses.join('\n')}>
            {address}
          </span>
        ) : null}
      </span>
      <span className="flex shrink-0 items-center gap-1">
        {ROW_ACTIONS.map((action) => (
          <LinkButton
            key={action.label}
            as={Link}
            href={`${href}${action.suffix}`}
            type="quaternary"
            theme="borderless"
            size="xs"
            icon={<Icon icon={action.icon} size={12} aria-hidden />}
            aria-label={`${action.label} for ${instance.name}`}>
            {action.label}
          </LinkButton>
        ))}
      </span>
    </li>
  );
}

/**
 * The Instances section: rows once any instance is known, placeholder rows
 * while the query is unanswered, and a quiet notice for none or a failure.
 */
export function WorkloadInstances({
  instances,
  loading,
  error,
  locationIndex,
  instanceHref,
  deploying,
  deleting,
}: {
  instances: readonly Instance[];
  /** The instances query hasn't answered yet. */
  loading: boolean;
  error: ApiError | null;
  locationIndex: LocationIndex;
  instanceHref: (name: string) => string;
  /** Still coming up, so no instances yet is expected. */
  deploying: boolean;
  deleting: boolean;
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
        <ul className="flex flex-col">
          {instances.map((instance) => (
            <InstanceRow
              key={instance.uid || instance.name}
              instance={instance}
              href={instanceHref(instance.name)}
              locationIndex={locationIndex}
            />
          ))}
        </ul>
      )}
    </SectionFrame>
  );
}
