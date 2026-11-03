/**
 * Static topology diagram: ALBs → workload → instances.
 *
 * Layout is an upside-down tree: the workload is the root, region groups sit
 * in a row beneath it, and instances wrap into a few columns inside each
 * region. Large groups show their unhealthy instances first and fold the rest
 * into a "+N more" card that expands in place. Load balancers stack in a
 * column to the workload's left, folding the same way past a few. The frame
 * pans by dragging the
 * background and zooms with a pinch or the corner buttons.
 * Connectors are orthogonal SVG paths measured from port elements.
 *
 * Styling note: this plugin ships no CSS of its own — it renders inside the
 * portal's stylesheet, which only contains Tailwind classes the host happened
 * to compile. Anything layout-critical therefore lives in the scoped <style>
 * block below and uses the host's theme variables (--primary, --border, …).
 */
import { imageShortName } from '../lib/workload-presenters';
import { useCopyToClipboard } from '@datum-cloud/datum-ui/hooks';
import { Icon } from '@datum-cloud/datum-ui/icons';
import {
  ChartLineIcon,
  CheckIcon,
  CopyIcon,
  GlobeIcon,
  LayersIcon,
  LocateFixedIcon,
  MinusIcon,
  PlusIcon,
  ServerIcon,
  SquareLibraryIcon,
} from 'lucide-react';
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router';

export type TopologyStatus = 'success' | 'warning' | 'danger' | 'info' | 'muted';

export type TopologyAlb = {
  id: string;
  title: string;
  hostname?: string;
  /** Live traffic rows rendered under the header. */
  body?: ReactNode;
  /** Requests per second; drives connector animation speed. Undefined/0 = idle. */
  traffic?: number;
  /** Card click target (stretched over the whole card). */
  href?: string;
  /** Chart button target. */
  metricsHref?: string;
  /** Set while the load balancer isn't serving yet; replaces the protocol in the footer. */
  status?: { tone: TopologyStatus; label: string };
};

export type TopologyInstance = {
  id: string;
  title: string;
  location?: string;
  /** Region label used to split the replica row. Omitted when the instance has no location. */
  group?: string;
  status: TopologyStatus;
  statusLabel: string;
  href?: string;
  metricsHref?: string;
  body?: ReactNode;
  footLeft?: ReactNode;
  footLeftTitle?: string;
  footRight?: ReactNode;
};

export type TopologyWorkload = {
  id: string;
  title: string;
  location?: string;
  image?: string;
  status: TopologyStatus;
  statusLabel: string;
  body?: ReactNode;
};

type Point = { x: number; y: number };
type Edge =
  | { kind: 'ingress'; d: string; albId: string; to: Point }
  | { kind: 'fanout'; d: string };

const CARD_WIDTH = 264;
const CARD_GAP = 16;

/**
 * Load balancers shown before the rest fold into a "+N more" card. The column
 * sits beside the workload, so every card makes the top row taller and the
 * whole tree smaller at fit-to-view.
 */
const ALB_LIMIT = 4;
/** Expansion key for the balancer column, alongside the region groups' keys. */
const ALBS_KEY = '\u0000albs';

/**
 * Columns per group, and how many cards a group shows before folding the rest
 * into a "+N more" card. Several regions side by side get narrower groups so
 * the tree stays close to the frame's aspect ratio and fits at a readable zoom.
 */
const SINGLE_GROUP_LAYOUT = { columns: 4, limit: 8 } as const;
const MULTI_GROUP_LAYOUT = { columns: 2, limit: 4 } as const;
const COMPACT_LAYOUT = { columns: 1, limit: 3 } as const;

/**
 * Below this frame width the tree stacks into one column (load balancers over
 * the workload over its instances) and renders at its natural size with no
 * pan or zoom: the side-by-side tree is ~1000px wide and on a phone would
 * shrink past readable or clip. The frame grows to the content instead, so
 * the page scrolls as usual and the canvas never traps a touch.
 */
const COMPACT_BREAKPOINT = 560;
/** Fixed frame height outside compact mode. Inline: the host does not compile `h-[28rem]`. */
const FRAME_HEIGHT = '28rem';
/** Compact top inset, clearing the corner chips. */
const COMPACT_TOP = 52;
const COMPACT_BOTTOM = 20;
const MIN_SCALE = 0.4;
const MAX_SCALE = 2.5;

type View = { x: number; y: number; scale: number };

/**
 * Packet animation on a normalised path (pathLength=100). The dash pattern has
 * a 200-unit period so the packet is fully hidden for the second half of each
 * cycle, giving a natural gap between packets. Layers share a leading edge so
 * the head is bright and the tail fades.
 */
const PACKET_LAYERS = [
  { length: 18, opacity: 0.22, width: 3 },
  { length: 10, opacity: 0.5, width: 2 },
  { length: 3, opacity: 1, width: 2 },
] as const;
const PACKET_HEAD = PACKET_LAYERS[0].length;

function packetDuration(traffic?: number): { dur: number; idle: boolean } {
  if (!traffic || traffic <= 0 || !Number.isFinite(traffic)) return { dur: 4.2, idle: true };
  // ~2.3s at 1 rps, ~1.7s at 10 rps, floor at 1s for busy ALBs. Rounded to
  // 0.2s steps: changing `dur` restarts the SMIL cycle, so a jittering rps
  // reading should not retrigger it on every poll.
  const raw = Math.min(2.6, Math.max(1, 2.6 - Math.log10(traffic + 1) * 0.9));
  const dur = Math.round(raw / 0.2) * 0.2;
  return { dur, idle: false };
}

const STYLES = `
.cpt-root{position:absolute;inset:0;overflow:hidden;cursor:grab;touch-action:none;color:var(--card-foreground);background:color-mix(in oklab,var(--muted) 55%,var(--card))}
.cpt-root.cpt-panning,.cpt-root.cpt-panning *{cursor:grabbing;user-select:none}
.cpt-root a,.cpt-root button{cursor:pointer}
.cpt-dots{position:absolute;inset:0;pointer-events:none;background-image:radial-gradient(circle,color-mix(in oklab,var(--foreground) 14%,transparent) 1px,transparent 1.4px)}
.cpt-world{position:absolute;top:0;left:0;width:max-content;height:max-content;transform-origin:0 0}
.cpt-svg{position:absolute;inset:0;width:100%;height:100%;pointer-events:none;color:var(--primary);overflow:visible}
.cpt-chrome{position:absolute;top:12px;left:12px;right:12px;display:flex;align-items:center;justify-content:space-between;gap:8px;pointer-events:none;z-index:2}
.cpt-zoom{position:absolute;right:12px;bottom:12px;z-index:2;display:flex;flex-direction:column;gap:4px}
.cpt-zoom button{width:28px;height:28px;display:flex;align-items:center;justify-content:center;padding:0;border:1px solid var(--border);border-radius:6px;background:var(--card);color:var(--card-foreground);box-shadow:0 1px 2px rgb(0 0 0/.05)}
.cpt-zoom button:hover{border-color:color-mix(in oklab,var(--primary) 45%,var(--border))}
.cpt-zoom button:focus-visible{outline:2px solid var(--ring);outline-offset:1px}
.cpt-chip{display:inline-flex;align-items:center;gap:6px;height:24px;padding:0 8px;border-radius:6px;border:1px solid var(--border);background:var(--card);font-family:var(--font-mono);font-size:11px;line-height:1;color:var(--card-foreground);box-shadow:0 1px 2px rgb(0 0 0/.04);white-space:nowrap}
.cpt-chip-dot{width:6px;height:6px;border-radius:9999px}
/* Workload stays centered over the region row. With load balancers the top
   row is three equal columns (balancers, workload, an empty spacer) so the
   workload stays in the middle, and the balancer column is in flow: however
   many stack up, the row grows to hold them, pushing the regions down and
   staying inside what fit-to-view measures. */
.cpt-tree{position:relative;display:flex;flex-direction:column;align-items:center;gap:56px;width:max-content}
.cpt-top{position:relative}
.cpt-top-ingress{display:grid;grid-template-columns:264px 264px 264px;column-gap:96px;align-items:center}
.cpt-ingress{display:flex;flex-direction:column;gap:24px}
.cpt-replicas{display:flex;flex-direction:row;align-items:flex-start;justify-content:center;gap:48px;width:max-content}
.cpt-group{position:relative;flex:0 0 auto;width:max-content;display:flex;flex-direction:column;align-items:stretch;gap:12px}
.cpt-group.cpt-boxed{border:1px solid color-mix(in oklab,var(--primary) 28%,transparent);background:color-mix(in oklab,var(--primary) 4%,var(--card));border-radius:12px;padding:16px}
.cpt-group-cards{display:flex;flex-direction:row;flex-wrap:wrap;justify-content:center;gap:16px}
.cpt-group-label{display:flex;align-items:baseline;justify-content:space-between;gap:12px;font-size:12px;font-weight:500;line-height:1.25}
.cpt-group-meta{display:flex;align-items:baseline;gap:10px}
.cpt-group-count{color:var(--muted-foreground);font-weight:400;font-variant-numeric:tabular-nums}
.cpt-group-toggle{padding:0;border:0;background:transparent;font:inherit;font-weight:500;color:var(--primary)}
.cpt-group-toggle:hover{text-decoration:underline}
.cpt-group-toggle:focus-visible{outline:2px solid var(--ring);outline-offset:2px;border-radius:2px}
.cpt-more{width:264px;min-height:120px;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:6px;padding:16px;border:1px dashed color-mix(in oklab,var(--primary) 35%,var(--border));border-radius:8px;background:color-mix(in oklab,var(--card) 70%,transparent);color:var(--card-foreground);font:inherit;text-align:center;transition:border-color 160ms ease,background-color 160ms ease,transform 160ms cubic-bezier(.23,1,.32,1)}
.cpt-more:hover{border-color:color-mix(in oklab,var(--primary) 60%,var(--border));background:color-mix(in oklab,var(--primary) 5%,var(--card))}
.cpt-more:active{transform:scale(.985)}
.cpt-more:focus-visible{outline:2px solid var(--ring);outline-offset:2px}
.cpt-more-title{font-size:13px;font-weight:600;line-height:1.25}
.cpt-more-sub{font-size:12px;line-height:1.25;color:var(--muted-foreground)}
.cpt-more-cta{font-size:12px;font-weight:500;color:var(--primary)}
.cpt-node{position:relative}
/* No white inset ring: it vanishes on a light card and reads as a second bottom stroke in dark mode. */
.cpt-card{position:relative;width:264px;display:flex;flex-direction:column;border:1px solid var(--border);background:var(--card);color:var(--card-foreground);border-radius:8px;box-shadow:0 1px 2px rgb(0 0 0/.05);text-align:left;font:inherit;padding:0;margin:0;transition:border-color 160ms ease,box-shadow 160ms ease,transform 160ms cubic-bezier(.23,1,.32,1)}
.cpt-card:has(.cpt-card-link:hover){border-color:color-mix(in oklab,var(--primary) 45%,var(--border));box-shadow:0 2px 8px rgb(0 0 0/.06)}
.cpt-card:has(.cpt-card-link:active){transform:scale(.985)}
.cpt-card:has(.cpt-card-link:focus-visible){outline:2px solid var(--ring);outline-offset:2px}
.cpt-card-link{display:block;color:inherit;text-decoration:none;outline:none}
.cpt-card-link::after{content:"";position:absolute;inset:0;border-radius:8px}
.cpt-card-link[data-hint]::before,.cpt-action[data-hint]::before{content:attr(data-hint);position:absolute;z-index:3;padding:4px 8px;border-radius:6px;border:1px solid var(--border);background:var(--card);color:var(--card-foreground);font-family:var(--font-sans,ui-sans-serif),system-ui,sans-serif;font-size:11px;font-weight:500;line-height:1.25;white-space:nowrap;pointer-events:none;opacity:0;transition:opacity 160ms ease,transform 160ms ease;box-shadow:0 2px 8px rgb(0 0 0/.08)}
.cpt-card-link[data-hint]::before{left:12px;bottom:calc(100% + 8px);transform:translateY(4px)}
.cpt-card:has(.cpt-card-link:hover) .cpt-card-link[data-hint]::before,.cpt-card:has(.cpt-card-link:focus-visible) .cpt-card-link[data-hint]::before{opacity:1;transform:translateY(0)}
.cpt-action[data-hint]::before{left:50%;bottom:calc(100% + 6px);transform:translate(-50%,4px)}
.cpt-action[data-hint]:hover::before,.cpt-action[data-hint]:focus-visible::before{opacity:1;transform:translate(-50%,0)}
/* Body and footer sit above the stretched link so IPs, ports and images stay hoverable/selectable. */
.cpt-head{display:flex;align-items:center;gap:10px;padding:10px 12px}
.cpt-head-divided{border-bottom:1px solid var(--border)}
.cpt-icon{width:32px;height:32px;border-radius:9999px;display:flex;align-items:center;justify-content:center;flex-shrink:0}
.cpt-icon-primary{background:color-mix(in oklab,var(--primary) 10%,transparent);color:var(--primary)}
.cpt-icon-success{background:var(--color-badge-success);color:#fff}
.cpt-icon-warning{background:var(--color-badge-warning);color:#fff}
.cpt-icon-danger{background:var(--color-badge-danger);color:#fff}
.cpt-icon-info{background:var(--color-badge-info);color:#fff}
.cpt-icon-muted{background:var(--muted);color:var(--muted-foreground)}
.cpt-text{min-width:0;flex:1}
.cpt-title{margin:0;font-size:13px;font-weight:500;line-height:1.25;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.cpt-title-strong{font-weight:600}
.cpt-title-mono{font-family:var(--font-mono);font-size:12px}
.cpt-sub{margin:2px 0 0;font-size:12px;line-height:1.25;color:var(--muted-foreground);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.cpt-action{position:relative;z-index:2;width:28px;height:28px;flex-shrink:0;display:flex;align-items:center;justify-content:center;border:1px solid var(--border);border-radius:6px;color:var(--primary);background:var(--card);text-decoration:none;transition:border-color 160ms ease,background-color 160ms ease}
.cpt-action:hover{border-color:color-mix(in oklab,var(--primary) 45%,var(--border));background:color-mix(in oklab,var(--primary) 8%,var(--card))}
.cpt-action:focus-visible{outline:2px solid var(--ring);outline-offset:1px}
.cpt-body{position:relative;z-index:1;display:flex;flex-direction:column;gap:5px;padding:10px 12px;font-size:12px;line-height:1.25}
.cpt-row{display:flex;align-items:baseline;justify-content:space-between;gap:12px;min-width:0}
.cpt-row-label{color:var(--muted-foreground);white-space:nowrap}
.cpt-row-value{font-variant-numeric:tabular-nums;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.cpt-row-value-mono{font-family:var(--font-mono);font-size:11px}
.cpt-strong{font-weight:600}
.cpt-foot{position:relative;z-index:1;display:flex;align-items:center;justify-content:space-between;gap:12px;border-top:1px solid var(--border);background:color-mix(in oklab,var(--muted) 45%,transparent);padding:7px 12px;font-family:var(--font-mono);font-size:11px;line-height:1;border-radius:0 0 7px 7px}
/* Muted-on-transparent reads as a light strip on a dark card. Darken it so the bar stays distinct. */
html.dark .cpt-foot{background:color-mix(in oklab,black 22%,var(--card))}
.cpt-foot-left{display:flex;align-items:center;gap:6px;min-width:0}
.cpt-foot-text{min-width:0;overflow:hidden;white-space:nowrap;text-overflow:ellipsis}
.cpt-copy{display:inline-flex;align-items:center;justify-content:center;width:18px;height:18px;flex-shrink:0;padding:0;border:0;border-radius:4px;background:transparent;color:var(--muted-foreground);cursor:pointer;transition:color 160ms ease,background-color 160ms ease}
.cpt-copy:hover{color:var(--foreground);background:color-mix(in oklab,var(--foreground) 8%,transparent)}
.cpt-copy:focus-visible{outline:2px solid var(--ring);outline-offset:1px}
.cpt-copy-done{color:var(--color-badge-success)}
.cpt-foot-right{flex-shrink:0;text-align:right;white-space:nowrap}
.cpt-muted{color:var(--muted-foreground)}
.cpt-status-success{color:var(--color-badge-success)}
.cpt-status-warning{color:var(--color-badge-warning)}
.cpt-status-danger{color:var(--color-badge-danger)}
.cpt-status-info{color:var(--color-badge-info)}
.cpt-status-muted{color:var(--muted-foreground)}
.cpt-bg-success{background:var(--color-badge-success)}
.cpt-bg-warning{background:var(--color-badge-warning)}
.cpt-bg-danger{background:var(--color-badge-danger)}
.cpt-bg-info{background:var(--color-badge-info)}
.cpt-bg-muted{background:var(--muted-foreground)}
.cpt-port{position:absolute;width:6px;height:6px;border-radius:9999px;background:var(--primary);box-shadow:0 0 0 2px var(--card);z-index:1}
.cpt-port-right{top:50%;right:-3px;transform:translateY(-50%)}
.cpt-port-left{top:50%;left:-3px;transform:translateY(-50%)}
.cpt-port-bottom{bottom:-3px;left:50%;transform:translateX(-50%)}
.cpt-port-top{top:-3px;left:50%;transform:translateX(-50%)}
.cpt-flow{transition:opacity 400ms ease}
.cpt-flow-idle{opacity:.55}
.cpt-root.cpt-compact{cursor:default;touch-action:auto}
.cpt-compact .cpt-zoom{display:none}
.cpt-compact .cpt-top-ingress{grid-template-columns:264px;row-gap:48px}
.cpt-compact .cpt-replicas{flex-direction:column;align-items:center;gap:24px}
@media (prefers-reduced-motion:reduce){.cpt-flow{display:none}}
`;

/** Hundredths of a pixel: tight enough to sit on the port, coarse enough that sub-pixel jitter does not restart the packet animation. */
function snap(n: number): number {
  return Math.round(n * 100) / 100;
}

function portCenter(world: HTMLElement, el: Element | null): Point | null {
  if (!el) return null;
  const origin = world.getBoundingClientRect();
  const rect = el.getBoundingClientRect();
  // Pan and zoom are a CSS transform on the world. Divide them out so the
  // path stays in the SVG's unscaled coordinate space, on the port center.
  const scale = origin.width / world.offsetWidth || 1;
  return {
    x: snap((rect.left + rect.width / 2 - origin.left) / scale),
    y: snap((rect.top + rect.height / 2 - origin.top) / scale),
  };
}

function fitView(viewport: HTMLElement, world: HTMLElement): View {
  const vw = viewport.clientWidth;
  const vh = viewport.clientHeight;
  const ww = world.offsetWidth;
  const wh = world.offsetHeight;
  if (!vw || !vh || !ww || !wh) return { x: 0, y: 0, scale: 1 };
  const scale = Math.min(1, Math.max(MIN_SCALE, Math.min((vw - 48) / ww, (vh - 48) / wh)));
  return { scale, x: (vw - ww * scale) / 2, y: (vh - wh * scale) / 2 };
}

/** Compact fit: natural size (shrunk only if wider than the frame), top-aligned, with the frame height that holds it. */
function compactView(viewport: HTMLElement, world: HTMLElement): { view: View; height: number } {
  const vw = viewport.clientWidth;
  const ww = world.offsetWidth;
  const wh = world.offsetHeight;
  if (!vw || !ww || !wh) return { view: { x: 0, y: COMPACT_TOP, scale: 1 }, height: 0 };
  const scale = Math.min(1, (vw - 32) / ww);
  return {
    view: { scale, x: (vw - ww * scale) / 2, y: COMPACT_TOP },
    height: Math.ceil(wh * scale + COMPACT_TOP + COMPACT_BOTTOM),
  };
}

function zoomAt(current: View, px: number, py: number, nextScale: number): View {
  const scale = Math.min(MAX_SCALE, Math.max(MIN_SCALE, nextScale));
  const wx = (px - current.x) / current.scale;
  const wy = (py - current.y) / current.scale;
  return { scale, x: px - wx * scale, y: py - wy * scale };
}

function isInteractiveTarget(target: EventTarget | null): boolean {
  return target instanceof Element && Boolean(target.closest('a, button, input, textarea'));
}

/** Horizontal → vertical → horizontal elbow between two side ports. */
function elbowH(from: Point, to: Point) {
  // A short slope still runs through both port centers. Sharing one Y leaves
  // the dots sitting off the stroke.
  if (Math.abs(from.y - to.y) < 8) return `M ${from.x} ${from.y} L ${to.x} ${to.y}`;
  const midX = snap((from.x + to.x) / 2);
  return `M ${from.x} ${from.y} H ${midX} V ${to.y} H ${to.x}`;
}

/** Vertical → horizontal → vertical elbow from a bottom port to a top port. */
function elbowV(from: Point, to: Point) {
  if (Math.abs(from.x - to.x) < 8) return `M ${from.x} ${from.y} L ${to.x} ${to.y}`;
  const midY = snap((from.y + to.y) / 2);
  return `M ${from.x} ${from.y} V ${midY} H ${to.x} V ${to.y}`;
}

/** Trunk down from a bottom port to a bus, then drops to each top port. */
function fanOut(from: Point, targets: Point[]) {
  if (targets.length === 0) return '';
  const busY = snap(from.y + (targets[0].y - from.y) / 2);
  const xs = [from.x, ...targets.map((t) => t.x)];
  const minX = Math.min(...xs);
  const maxX = Math.max(...xs);
  const drops = targets.map((t) => `M ${t.x} ${busY} V ${t.y}`).join(' ');
  return `M ${from.x} ${from.y} V ${busY} M ${minX} ${busY} H ${maxX} ${drops}`;
}

function Port({ id, side }: { id: string; side: 'left' | 'right' | 'bottom' | 'top' }) {
  return <span data-port={id} aria-hidden className={`cpt-port cpt-port-${side}`} />;
}

function GraphCard({ children }: { children: ReactNode }) {
  return <div className="cpt-card">{children}</div>;
}

function CardHead({
  icon,
  tone,
  title,
  titleClassName,
  subtitle,
  href,
  hrefLabel,
  hint,
  metricsHref,
  divided,
}: {
  icon: typeof GlobeIcon;
  tone: TopologyStatus | 'primary';
  title: string;
  titleClassName?: string;
  subtitle?: string;
  /** Primary link, stretched over the whole card. */
  href?: string;
  hrefLabel?: string;
  /** Visible hover label for what the stretched link does. `aria-label` stays the accessible name. */
  hint?: string;
  /** Secondary chart button, layered above the stretched link. */
  metricsHref?: string;
  divided?: boolean;
}) {
  const titleClass = `cpt-title ${titleClassName ?? ''}`;
  return (
    <div className={`cpt-head${divided ? ' cpt-head-divided' : ''}`}>
      <span className={`cpt-icon cpt-icon-${tone}`}>
        <Icon icon={icon} size={15} />
      </span>
      <div className="cpt-text">
        {href ? (
          <Link
            to={href}
            className={`${titleClass} cpt-card-link`}
            title={title}
            aria-label={hrefLabel}
            data-hint={hint}>
            {title}
          </Link>
        ) : (
          <p className={titleClass} title={title}>
            {title}
          </p>
        )}
        {subtitle ? <p className="cpt-sub">{subtitle}</p> : null}
      </div>
      {metricsHref ? (
        <Link
          to={metricsHref}
          className="cpt-action"
          data-hint="View metrics"
          aria-label={`Metrics for ${title}`}>
          <Icon icon={ChartLineIcon} size={13} />
        </Link>
      ) : null}
    </div>
  );
}

function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, copy] = useCopyToClipboard();
  return (
    <button
      type="button"
      className={`cpt-copy${copied ? ' cpt-copy-done' : ''}`}
      title={copied ? 'Copied' : 'Copy'}
      aria-label={label}
      onClick={() => void copy(value, { withToast: true })}>
      <Icon icon={copied ? CheckIcon : CopyIcon} size={11} />
    </button>
  );
}

function CardFoot({
  left,
  right,
  leftTitle,
  copyValue,
  copyLabel,
}: {
  left: ReactNode;
  right: ReactNode;
  leftTitle?: string;
  /** When set, renders a small copy button after the left text. */
  copyValue?: string;
  copyLabel?: string;
}) {
  return (
    <div className="cpt-foot">
      <span className="cpt-foot-left">
        <span className="cpt-foot-text" title={leftTitle}>
          {left}
        </span>
        {copyValue ? <CopyButton value={copyValue} label={copyLabel ?? 'Copy'} /> : null}
      </span>
      <span className="cpt-foot-right">{right}</span>
    </div>
  );
}

/** Label / value line inside a card body. Exported so the card can compose rows. */
export function TopologyRow({
  label,
  value,
  mono,
  title,
}: {
  label: ReactNode;
  value: ReactNode;
  mono?: boolean;
  title?: string;
}) {
  return (
    <div className="cpt-row">
      <span className="cpt-row-label">{label}</span>
      <span className={`cpt-row-value${mono ? ' cpt-row-value-mono' : ''}`} title={title}>
        {value}
      </span>
    </div>
  );
}

type InstanceGroup = {
  /** Set when the replica row is split across regions. */
  label?: string;
  instances: TopologyInstance[];
};

const STATUS_PRIORITY: Record<TopologyStatus, number> = {
  danger: 0,
  warning: 1,
  info: 2,
  muted: 3,
  success: 4,
};

/** Unhealthy first so they survive folding, then natural name order (…-2 before …-10). */
function byAttention(a: TopologyInstance, b: TopologyInstance): number {
  return (
    STATUS_PRIORITY[a.status] - STATUS_PRIORITY[b.status] ||
    a.title.localeCompare(b.title, undefined, { numeric: true, sensitivity: 'base' })
  );
}

/** "48 Available · 1 Pending" for the folded instances. */
function statusSummary(instances: TopologyInstance[]): string {
  const counts = new Map<string, number>();
  for (const instance of [...instances].sort(byAttention)) {
    counts.set(instance.statusLabel, (counts.get(instance.statusLabel) ?? 0) + 1);
  }
  return [...counts].map(([label, count]) => `${count} ${label}`).join(' · ');
}

function cardsWidth(count: number, columns: number): number {
  const cols = Math.max(1, Math.min(count, columns));
  return cols * CARD_WIDTH + (cols - 1) * CARD_GAP;
}

/**
 * One unlabeled box when every instance shares a region (or none do).
 * Labeled boxes once two or more regions appear. Instances with no location
 * land in "Unknown" only in that split case.
 */
function instanceGroups(instances: TopologyInstance[]): InstanceGroup[] {
  const buckets = new Map<string, TopologyInstance[]>();
  for (const instance of instances) {
    const key = instance.group?.trim() ?? '';
    const list = buckets.get(key);
    if (list) list.push(instance);
    else buckets.set(key, [instance]);
  }
  if (buckets.size <= 1) return [{ instances }];

  const groups: InstanceGroup[] = [];
  for (const [key, grouped] of buckets) {
    if (key) groups.push({ label: key, instances: grouped });
  }
  const unknown = buckets.get('');
  if (unknown) groups.push({ label: 'Unknown', instances: unknown });
  return groups;
}

function instanceCountLabel(count: number): string {
  return count === 1 ? '1 instance' : `${count} instances`;
}

export function TopologyCanvas({
  workload,
  albs,
  instances,
  chromeLeft,
  chromeRight,
}: {
  workload: TopologyWorkload;
  albs: TopologyAlb[];
  instances: TopologyInstance[];
  /** Corner chips, e.g. health on the left and instance class on the right. */
  chromeLeft?: ReactNode;
  chromeRight?: ReactNode;
}) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const worldRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<View>({ x: 0, y: 0, scale: 1 });
  const [view, setView] = useState<View>({ x: 0, y: 0, scale: 1 });
  const [panning, setPanning] = useState(false);
  const [edges, setEdges] = useState<Edge[]>([]);
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  const [compact, setCompact] = useState(false);
  const [compactHeight, setCompactHeight] = useState(0);
  viewRef.current = view;

  useLayoutEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    const check = () => setCompact(viewport.clientWidth > 0 && viewport.clientWidth < COMPACT_BREAKPOINT);
    check();
    const observer = new ResizeObserver(check);
    observer.observe(viewport);
    return () => observer.disconnect();
  }, []);

  const groups = instanceGroups(instances);
  const branchToGroups = groups.length > 1;
  const { columns, limit } = compact
    ? COMPACT_LAYOUT
    : branchToGroups
      ? MULTI_GROUP_LAYOUT
      : SINGLE_GROUP_LAYOUT;
  const laidOut = groups.map((group) => {
    const key = group.label ?? 'all';
    const sorted = [...group.instances].sort(byAttention);
    const foldable = sorted.length > limit;
    const open = foldable && expanded.has(key);
    const shown = foldable && !open ? sorted.slice(0, limit - 1) : sorted;
    const folded = foldable && !open ? sorted.slice(limit - 1) : [];
    const cardCount = shown.length + (folded.length > 0 ? 1 : 0);
    return { ...group, key, shown, folded, foldable, open, cardCount };
  });
  // One trunk per group once cards wrap, so drops never cross a card row.
  const groupPorts = branchToGroups || laidOut.some((group) => group.cardCount > columns);

  const toggleGroup = (key: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  // Key the measurement on node identity, not array identity: the parent
  // rebuilds `albs`/`instances` whenever live metrics tick, and geometry
  // only changes when nodes are added/removed, regrouped, reordered by
  // status or folded (ResizeObserver covers size).
  const albsFoldable = albs.length > ALB_LIMIT;
  const albsOpen = albsFoldable && expanded.has(ALBS_KEY);
  const shownAlbs = albsFoldable && !albsOpen ? albs.slice(0, ALB_LIMIT - 1) : albs;
  const foldedAlbs = albsFoldable && !albsOpen ? albs.slice(ALB_LIMIT - 1) : [];
  const albIds = shownAlbs.map((alb) => alb.id).join('\u0000');
  const shownKey = laidOut
    .map((group) => `${group.key}:${group.shown.map((i) => i.id).join(',')}:${group.folded.length}`)
    .join('\u0000');
  const layoutKey = `${compact}\u0000${workload.id}\u0000${albIds}\u0000${shownKey}`;

  useLayoutEffect(() => {
    const world = worldRef.current;
    if (!world) return;
    const albList = albIds ? albIds.split('\u0000') : [];

    const measure = () => {
      const next: Edge[] = [];
      const workloadIn = portCenter(world, world.querySelector('[data-port="workload-in"]'));
      const workloadOut = portCenter(world, world.querySelector('[data-port="workload-out"]'));

      for (const albId of albList) {
        const from = portCenter(world, world.querySelector(`[data-port="alb-${albId}"]`));
        if (from && workloadIn) {
          const d = compact ? elbowV(from, workloadIn) : elbowH(from, workloadIn);
          next.push({ kind: 'ingress', d, albId, to: workloadIn });
        }
      }

      const groupEls = [...world.querySelectorAll<Element>('[data-port^="group-"]')];
      const targetEls =
        groupEls.length > 0
          ? groupEls
          : [...world.querySelectorAll<Element>('[data-port^="instance-"]')];
      const targets = targetEls
        .map((el) => portCenter(world, el))
        .filter((point): point is Point => point !== null);
      if (workloadOut && targets.length > 0) {
        next.push({ d: fanOut(workloadOut, targets), kind: 'fanout' });
      }

      // Skip the commit when nothing moved so SMIL animations are not restarted.
      setEdges((prev) => {
        const same =
          prev.length === next.length && prev.every((edge, i) => edge.d === next[i].d);
        return same ? prev : next;
      });
    };

    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(world);
    for (const el of world.querySelectorAll('.cpt-node, .cpt-group')) observer.observe(el);
    return () => observer.disconnect();
  }, [albIds, shownKey, workload.id, compact]);

  useLayoutEffect(() => {
    const viewport = viewportRef.current;
    const world = worldRef.current;
    if (!viewport || !world) return;
    if (!compact) {
      const next = fitView(viewport, world);
      viewRef.current = next;
      setView(next);
      return;
    }
    // Compact: track the tree's size, since the frame grows with it.
    const apply = () => {
      const { view: next, height } = compactView(viewport, world);
      viewRef.current = next;
      setView(next);
      setCompactHeight(height);
    };
    apply();
    const observer = new ResizeObserver(apply);
    observer.observe(world);
    observer.observe(viewport);
    return () => observer.disconnect();
  }, [layoutKey, compact]);

  useEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport || compact) return;

    const onWheel = (event: WheelEvent) => {
      event.preventDefault();
      // Pinch-to-zoom (ctrlKey). A plain wheel keeps scrolling the page.
      if (!event.ctrlKey) return;
      event.preventDefault();
      const rect = viewport.getBoundingClientRect();
      const current = viewRef.current;
      const next = zoomAt(
        current,
        event.clientX - rect.left,
        event.clientY - rect.top,
        current.scale * Math.exp(-event.deltaY * 0.01)
      );
      viewRef.current = next;
      setView(next);
    };

    viewport.addEventListener('wheel', onWheel, { passive: false });
    return () => viewport.removeEventListener('wheel', onWheel);
  }, [compact]);

  useEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport || compact) return;

    const onPointerDown = (event: PointerEvent) => {
      if (event.button !== 0 && event.button !== 1) return;
      if (event.button === 0 && isInteractiveTarget(event.target)) return;
      event.preventDefault();
      const startX = event.clientX;
      const startY = event.clientY;
      const origin = viewRef.current;
      setPanning(true);
      const onMove = (move: PointerEvent) => {
        const next = {
          ...origin,
          x: origin.x + move.clientX - startX,
          y: origin.y + move.clientY - startY,
        };
        viewRef.current = next;
        setView(next);
      };
      const onUp = () => {
        setPanning(false);
        window.removeEventListener('pointermove', onMove);
        window.removeEventListener('pointerup', onUp);
      };
      window.addEventListener('pointermove', onMove);
      window.addEventListener('pointerup', onUp);
    };

    viewport.addEventListener('pointerdown', onPointerDown);
    return () => viewport.removeEventListener('pointerdown', onPointerDown);
  }, [compact]);

  const hasIngress = albs.length > 0;
  const hasReplicas = instances.length > 0;

  const zoomBy = (factor: number) => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    const next = zoomAt(
      viewRef.current,
      viewport.clientWidth / 2,
      viewport.clientHeight / 2,
      viewRef.current.scale * factor
    );
    viewRef.current = next;
    setView(next);
  };

  const resetView = () => {
    const viewport = viewportRef.current;
    const world = worldRef.current;
    if (!viewport || !world) return;
    const next = fitView(viewport, world);
    viewRef.current = next;
    setView(next);
  };

  return (
    <div
      ref={viewportRef}
      data-testid="compute-plugin-topology-canvas"
      className={`cpt-root${panning ? ' cpt-panning' : ''}${compact ? ' cpt-compact' : ''}`}
      style={{ position: 'relative', height: compact ? compactHeight || FRAME_HEIGHT : FRAME_HEIGHT }}
      aria-label={
        compact
          ? 'Topology diagram.'
          : 'Topology diagram. Drag the background to move. Pinch or use the zoom buttons to zoom.'
      }>
      <style>{STYLES}</style>
      <div
        aria-hidden
        className="cpt-dots"
        style={{
          backgroundPosition: `${view.x}px ${view.y}px`,
          backgroundSize: `${16 * view.scale}px ${16 * view.scale}px`,
        }}
      />

      {chromeLeft || chromeRight ? (
        <div className="cpt-chrome" aria-hidden>
          <span>{chromeLeft}</span>
          <span>{chromeRight}</span>
        </div>
      ) : null}

      <div className="cpt-zoom">
        <button type="button" aria-label="Zoom in" onClick={() => zoomBy(1.2)}>
          <Icon icon={PlusIcon} size={14} />
        </button>
        <button type="button" aria-label="Zoom out" onClick={() => zoomBy(1 / 1.2)}>
          <Icon icon={MinusIcon} size={14} />
        </button>
        <button type="button" aria-label="Reset view" onClick={resetView}>
          <Icon icon={LocateFixedIcon} size={14} />
        </button>
      </div>

      <div
        ref={worldRef}
        className="cpt-world"
        style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }}>
      <svg className="cpt-svg" aria-hidden>
        {edges.map((edge, index) => {
          if (edge.kind !== 'ingress') {
            return (
              <path
                key={`fanout-${index}`}
                d={edge.d}
                fill="none"
                stroke="currentColor"
                strokeOpacity={0.4}
                strokeWidth={1.25}
                strokeDasharray="3 3"
                strokeLinecap="round"
              />
            );
          }

          const traffic = albs.find((alb) => alb.id === edge.albId)?.traffic;
          const { dur, idle } = packetDuration(traffic);
          const cycle = `${dur.toFixed(1)}s`;

          return (
            <g key={`ingress-${edge.albId}`}>
              <path
                d={edge.d}
                fill="none"
                stroke="currentColor"
                strokeOpacity={0.3}
                strokeWidth={1.5}
                strokeLinecap="round"
                strokeLinejoin="round"
              />
              <g className={`cpt-flow${idle ? ' cpt-flow-idle' : ''}`} key={edge.d}>
                {PACKET_LAYERS.map((layer) => {
                  // Shift each layer so all leading edges coincide with the head.
                  const shift = PACKET_HEAD - layer.length;
                  return (
                    <path
                      key={layer.length}
                      d={edge.d}
                      pathLength={100}
                      fill="none"
                      stroke="currentColor"
                      strokeOpacity={layer.opacity}
                      strokeWidth={layer.width}
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeDasharray={`${layer.length} ${200 - layer.length}`}>
                      <animate
                        attributeName="stroke-dashoffset"
                        from={200 + PACKET_HEAD - shift}
                        to={PACKET_HEAD - shift}
                        dur={cycle}
                        repeatCount="indefinite"
                      />
                    </path>
                  );
                })}
                {/* Landing ping: the head reaches the workload port halfway through the cycle. */}
                <circle cx={edge.to.x} cy={edge.to.y} r={3} fill="none" stroke="currentColor" strokeWidth={1.25}>
                  <animate
                    attributeName="r"
                    values="3;3;11;11"
                    keyTimes="0;0.5;0.68;1"
                    dur={cycle}
                    repeatCount="indefinite"
                  />
                  <animate
                    attributeName="stroke-opacity"
                    values="0;0.55;0;0"
                    keyTimes="0;0.5;0.68;1"
                    dur={cycle}
                    repeatCount="indefinite"
                  />
                </circle>
              </g>
            </g>
          );
        })}
      </svg>

      <div className="cpt-tree">
        <div className={`cpt-top${hasIngress ? ' cpt-top-ingress' : ''}`}>
        {hasIngress ? (
          <div className="cpt-ingress">
            {albsOpen ? (
              <button type="button" className="cpt-group-toggle" onClick={() => toggleGroup(ALBS_KEY)}>
                Show fewer load balancers
              </button>
            ) : null}
            {shownAlbs.map((alb) => (
              <div key={alb.id} className="cpt-node">
                <GraphCard>
                  <CardHead
                    icon={GlobeIcon}
                    tone="primary"
                    title={alb.title}
                    subtitle="Load balancer"
                    href={alb.href}
                    hrefLabel={`Open load balancer ${alb.title}`}
                    hint={alb.href ? 'Configure load balancer' : undefined}
                    metricsHref={alb.metricsHref}
                    divided={!!alb.body}
                  />
                  {alb.body ? <div className="cpt-body">{alb.body}</div> : null}
                  <CardFoot
                    left={alb.hostname ?? '—'}
                    leftTitle={alb.hostname}
                    copyValue={alb.hostname}
                    copyLabel={`Copy hostname ${alb.hostname ?? ''}`}
                    right={
                      alb.status ? (
                        <span className={`cpt-status-${alb.status.tone}`}>{alb.status.label}</span>
                      ) : (
                        <span className="cpt-muted">HTTPS</span>
                      )
                    }
                  />
                </GraphCard>
                <Port id={`alb-${alb.id}`} side={compact ? 'bottom' : 'right'} />
              </div>
            ))}
            {foldedAlbs.length > 0 ? (
              <button
                type="button"
                className="cpt-more"
                onClick={() => toggleGroup(ALBS_KEY)}
                aria-label={`Show all ${albs.length} load balancers`}>
                <span className="cpt-icon cpt-icon-primary">
                  <Icon icon={GlobeIcon} size={15} />
                </span>
                <span className="cpt-more-title">+{foldedAlbs.length} more</span>
                <span className="cpt-more-sub">load balancers</span>
                <span className="cpt-more-cta">Show all</span>
              </button>
            ) : null}
          </div>
        ) : null}

        <div className="cpt-primary cpt-node">
          <GraphCard>
            <CardHead
              icon={SquareLibraryIcon}
              tone={workload.status}
              title={workload.title}
              titleClassName="cpt-title-strong"
              subtitle={workload.location ?? 'Workload'}
              divided={!!workload.body}
            />
            {workload.body ? <div className="cpt-body">{workload.body}</div> : null}
            <CardFoot
              left={imageShortName(workload.image) ?? 'Workload'}
              leftTitle={workload.image}
              right={<span className={`cpt-status-${workload.status}`}>{workload.statusLabel}</span>}
            />
          </GraphCard>
          {hasIngress ? <Port id="workload-in" side={compact ? 'top' : 'left'} /> : null}
          {hasReplicas ? <Port id="workload-out" side="bottom" /> : null}
        </div>
        {/* Balances the balancer column so the workload stays centered. */}
        {hasIngress && !compact ? <div aria-hidden /> : null}
        </div>

        {hasReplicas ? (
          <div className="cpt-replicas">
            {laidOut.map((group) => {
              const showLabel = !!group.label || group.foldable;
              return (
              <div
                key={group.key}
                className={`cpt-group${group.label || group.instances.length > 1 ? ' cpt-boxed' : ''}`}>
                {groupPorts ? <Port id={`group-${group.key}`} side="top" /> : null}
                {showLabel ? (
                  <div className="cpt-group-label">
                    <span>{group.label ?? 'Instances'}</span>
                    <span className="cpt-group-meta">
                      <span className="cpt-group-count">{instanceCountLabel(group.instances.length)}</span>
                      {group.open ? (
                        <button
                          type="button"
                          className="cpt-group-toggle"
                          onClick={() => toggleGroup(group.key)}>
                          Show fewer
                        </button>
                      ) : null}
                    </span>
                  </div>
                ) : null}
                <div className="cpt-group-cards" style={{ width: cardsWidth(group.cardCount, columns) }}>
                  {group.shown.map((instance) => (
                    <div key={instance.id} className="cpt-node">
                      <GraphCard>
                        <CardHead
                          icon={ServerIcon}
                          tone={instance.status}
                          title={instance.title}
                          titleClassName="cpt-title-mono"
                          subtitle={instance.location ?? 'Instance'}
                          href={instance.href}
                          hrefLabel={`Open instance ${instance.title}`}
                          hint={instance.href ? 'Open instance' : undefined}
                          metricsHref={instance.metricsHref}
                          divided={!!instance.body}
                        />
                        {instance.body ? <div className="cpt-body">{instance.body}</div> : null}
                        <CardFoot
                          left={instance.footLeft ?? '—'}
                          leftTitle={instance.footLeftTitle}
                          right={
                            instance.footRight ?? (
                              <span className={`cpt-status-${instance.status}`}>
                                {instance.statusLabel}
                              </span>
                            )
                          }
                        />
                      </GraphCard>
                      {groupPorts ? null : <Port id={`instance-${instance.id}`} side="top" />}
                    </div>
                  ))}
                  {group.folded.length > 0 ? (
                    <button
                      type="button"
                      className="cpt-more"
                      onClick={() => toggleGroup(group.key)}
                      aria-label={`Show all ${group.instances.length} instances${group.label ? ` in ${group.label}` : ''}`}>
                      <span className="cpt-icon cpt-icon-primary">
                        <Icon icon={LayersIcon} size={15} />
                      </span>
                      <span className="cpt-more-title">+{group.folded.length} more</span>
                      <span className="cpt-more-sub">{statusSummary(group.folded)}</span>
                      <span className="cpt-more-cta">Show all</span>
                    </button>
                  ) : null}
                </div>
              </div>
              );
            })}
          </div>
        ) : null}
      </div>
      </div>
    </div>
  );
}
