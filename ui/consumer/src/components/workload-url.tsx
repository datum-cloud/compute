/**
 * Where a workload is reached: its load balancer's platform hostname. The
 * workload cards show it as a link that opens it; the detail page header,
 * where a Visit button already does that, shows it as plain text to read or
 * select (`plain`).
 *
 * The hostname wraps rather than truncates — it is the one thing here a user
 * needs in full, and on a phone a truncated one is useless. It only links once
 * the workload and its load balancer are serving; until then it shows why not,
 * so nobody is sent to a URL that won't answer yet. User-attached hostnames
 * sit behind a "+N" tooltip: unlike the platform one, we don't know whether
 * each of those resolves yet.
 */
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { cn } from '@datum-cloud/datum-ui/utils';
import { GlobeIcon, SquareArrowOutUpRightIcon } from 'lucide-react';

const FOCUS_RING = 'focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none';

/**
 * Icons sized and aligned in `em`, so they sit on the text at whatever size it
 * inherits. `display` is explicit: the host's Tailwind preflight makes every
 * `svg` a block, which would stack them on lines of their own.
 */
const GLOBE_STYLE = { display: 'inline-block', width: '1em', height: '1em', verticalAlign: '-0.15em' } as const;
const OPEN_STYLE = {
  display: 'inline-block',
  width: '0.8em',
  height: '0.8em',
  verticalAlign: '-0.05em',
  marginLeft: '0.3em',
} as const;
/** Hanging indent: a wrapped hostname lines up under itself, not under the globe. */
const HANG = '1.4em';

export function WorkloadUrl({
  hostname,
  customHostnames = [],
  live,
  status,
  size = 'md',
  plain = false,
  className,
}: {
  hostname: string;
  customHostnames?: string[];
  /** The workload and its load balancer are serving, so the link should work. */
  live: boolean;
  /** Why it isn't live yet, e.g. "Provisioning load balancer". */
  status?: string;
  /** `md` takes the surrounding text size; `sm` is the cards' smaller text. */
  size?: 'sm' | 'md';
  /** Hostname as quiet text, no icon or link, for where another control opens it. */
  plain?: boolean;
  className?: string;
}) {
  const url = `https://${hostname}`;
  const extras = customHostnames.filter((host) => host && host !== hostname);
  // The last label (e.g. `net`) and the open icon never part, so a hostname
  // that fills the line can't leave the icon stranded on a line of its own.
  const cut = hostname.lastIndexOf('.') + 1;
  const extrasEl = extras.length > 0 ? <ExtraHostnames extras={extras} /> : null;
  // Spaced with a real space rather than a margin, so it sits flush when it
  // wraps onto a line of its own.
  const statusEl =
    !live && status ? (
      <>
        {' '}
        <span className="text-muted-foreground whitespace-nowrap" style={{ fontSize: '0.85em' }}>
          {status}
        </span>
      </>
    ) : null;

  if (plain) {
    return (
      <span
        className={cn('block min-w-0', size === 'sm' && 'text-xs', className)}
        style={{ overflowWrap: 'anywhere' }}
        data-testid="compute-plugin-workload-url">
        <span className="text-muted-foreground">{hostname}</span>
        {extrasEl}
        {statusEl}
      </span>
    );
  }

  return (
    // One run of inline text rather than a flex row, so the icons align to
    // the text itself and wrapping behaves like text.
    <span
      className={cn('block min-w-0', size === 'sm' && 'text-xs', className)}
      // Inline: `break-anywhere` isn't something the host is guaranteed to compile.
      style={{ overflowWrap: 'anywhere', paddingLeft: HANG, textIndent: `-${HANG}` }}
      data-testid="compute-plugin-workload-url">
      <Icon icon={GlobeIcon} className="text-muted-foreground" style={GLOBE_STYLE} aria-hidden />
      <span style={{ display: 'inline-block', width: `calc(${HANG} - 1em)` }} />
      {live ? (
        <a
          href={url}
          target="_blank"
          rel="noopener noreferrer"
          className={cn('text-primary font-medium hover:underline', FOCUS_RING)}
          data-e2e="workload-url-link">
          {hostname.slice(0, cut)}
          <span style={{ whiteSpace: 'nowrap' }}>
            {hostname.slice(cut)}
            <Icon icon={SquareArrowOutUpRightIcon} className="opacity-70" style={OPEN_STYLE} aria-hidden />
          </span>
        </a>
      ) : (
        <span className="text-muted-foreground">{hostname}</span>
      )}
      {extrasEl}
      {statusEl}
    </span>
  );
}

/** "+N" for user-attached hostnames, listed on hover. */
function ExtraHostnames({ extras }: { extras: string[] }) {
  return (
    <>
      {' '}
      <Tooltip
        side="bottom"
        align="start"
        message={
          <span className="flex flex-col items-start gap-0.5 text-left">
            {extras.map((host) => (
              <span key={host} className="font-mono">
                {host}
              </span>
            ))}
          </span>
        }>
        <span
          tabIndex={0}
          className={cn('text-muted-foreground whitespace-nowrap', FOCUS_RING)}
          style={{
            // The tooltip trigger can become an inline-block, which would
            // inherit a hanging indent's negative offset and jump left.
            display: 'inline-block',
            textIndent: 0,
            marginLeft: '0.25em',
            fontSize: '0.85em',
            cursor: 'help',
            textDecoration: 'underline dotted',
            textUnderlineOffset: '2px',
          }}
          aria-label={`Also published at ${extras.join(', ')}`}>
          +{extras.length}
        </span>
      </Tooltip>
    </>
  );
}
