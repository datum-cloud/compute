/**
 * Static topology diagram: ALBs → workload → instances.
 *
 * Layout mirrors PlanetScale-style architecture cards: ingress on the left,
 * the primary (workload) top-right, replicas (instances) grouped beneath it.
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
  ServerIcon,
  SquareLibraryIcon,
} from 'lucide-react';
import { useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router';

export type TopologyStatus = 'success' | 'warning' | 'danger' | 'muted';

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
};

export type TopologyInstance = {
  id: string;
  title: string;
  location?: string;
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
.cpt-root{position:relative;display:flex;min-height:100%;padding:56px 32px 40px;overflow-x:auto;color:var(--card-foreground);background:color-mix(in oklab,var(--muted) 55%,var(--card))}
.cpt-dots{position:absolute;inset:0;pointer-events:none;background-image:radial-gradient(circle,color-mix(in oklab,var(--foreground) 14%,transparent) 1px,transparent 1.4px);background-size:16px 16px}
.cpt-svg{position:absolute;inset:0;width:100%;height:100%;pointer-events:none;color:var(--primary);overflow:visible}
.cpt-chrome{position:absolute;top:12px;left:12px;right:12px;display:flex;align-items:center;justify-content:space-between;gap:8px;pointer-events:none;z-index:1}
.cpt-chip{display:inline-flex;align-items:center;gap:6px;height:24px;padding:0 8px;border-radius:6px;border:1px solid var(--border);background:var(--card);font-family:var(--font-mono);font-size:11px;line-height:1;color:var(--card-foreground);box-shadow:0 1px 2px rgb(0 0 0/.04);white-space:nowrap}
.cpt-chip-dot{width:6px;height:6px;border-radius:9999px}
/* Center when it fits (margin:auto). min-width:0 + max-width:100% so the grid
   can shrink and wrap instead of overflowing both sides of the scrollport. */
.cpt-grid{position:relative;display:grid;align-items:start;margin:auto;min-width:0;max-width:100%}
.cpt-ingress{display:flex;flex-direction:column;gap:24px;align-self:center}
.cpt-primary{justify-self:center}
.cpt-group{min-width:0;max-width:100%;display:flex;flex-wrap:wrap;justify-content:center;gap:16px}
.cpt-group.cpt-boxed{border:1px solid color-mix(in oklab,var(--primary) 28%,transparent);background:color-mix(in oklab,var(--primary) 4%,var(--card));border-radius:12px;padding:16px}
.cpt-node{position:relative}
.cpt-card{position:relative;width:264px;display:flex;flex-direction:column;border:1px solid var(--border);background:var(--card);color:var(--card-foreground);border-radius:8px;box-shadow:0 1px 2px rgb(0 0 0/.05),0 0 0 1px rgb(255 255 255/.4) inset;text-align:left;font:inherit;padding:0;margin:0;transition:border-color 160ms ease,box-shadow 160ms ease,transform 160ms cubic-bezier(.23,1,.32,1)}
.cpt-card:has(.cpt-card-link:hover){border-color:color-mix(in oklab,var(--primary) 45%,var(--border));box-shadow:0 2px 8px rgb(0 0 0/.06)}
.cpt-card:has(.cpt-card-link:active){transform:scale(.985)}
.cpt-card:has(.cpt-card-link:focus-visible){outline:2px solid var(--ring);outline-offset:2px}
.cpt-card-link{display:block;color:inherit;text-decoration:none;outline:none}
.cpt-card-link::after{content:"";position:absolute;inset:0;border-radius:8px}
/* Body and footer sit above the stretched link so IPs, ports and images stay hoverable/selectable. */
.cpt-head{display:flex;align-items:center;gap:10px;padding:10px 12px}
.cpt-head-divided{border-bottom:1px solid var(--border)}
.cpt-icon{width:32px;height:32px;border-radius:9999px;display:flex;align-items:center;justify-content:center;flex-shrink:0}
.cpt-icon-primary{background:color-mix(in oklab,var(--primary) 10%,transparent);color:var(--primary)}
.cpt-icon-success{background:var(--color-badge-success);color:#fff}
.cpt-icon-warning{background:var(--color-badge-warning);color:#fff}
.cpt-icon-danger{background:var(--color-badge-danger);color:#fff}
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
.cpt-foot{position:relative;z-index:1;display:flex;align-items:center;justify-content:space-between;gap:12px;border-top:1px solid var(--border);background:color-mix(in oklab,var(--muted) 45%,transparent);padding:7px 12px;font-family:var(--font-mono);font-size:11px;line-height:1;border-radius:0 0 8px 8px}
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
.cpt-status-muted{color:var(--muted-foreground)}
.cpt-bg-success{background:var(--color-badge-success)}
.cpt-bg-warning{background:var(--color-badge-warning)}
.cpt-bg-danger{background:var(--color-badge-danger)}
.cpt-bg-muted{background:var(--muted-foreground)}
.cpt-port{position:absolute;width:6px;height:6px;border-radius:9999px;background:var(--primary);box-shadow:0 0 0 2px var(--card);z-index:1}
.cpt-port-right{top:50%;right:-3px;transform:translateY(-50%)}
.cpt-port-left{top:50%;left:-3px;transform:translateY(-50%)}
.cpt-port-bottom{bottom:-3px;left:50%;transform:translateX(-50%)}
.cpt-port-top{top:-3px;left:50%;transform:translateX(-50%)}
.cpt-flow{transition:opacity 400ms ease}
.cpt-flow-idle{opacity:.55}
@media (prefers-reduced-motion:reduce){.cpt-flow{display:none}}
`;

function portCenter(root: HTMLElement, el: Element | null): Point | null {
  if (!el) return null;
  const box = root.getBoundingClientRect();
  const rect = el.getBoundingClientRect();
  // The SVG scrolls with the content, so add the container's scroll offset.
  return {
    x: Math.round(rect.left + rect.width / 2 - box.left + root.scrollLeft) + 0.5,
    y: Math.round(rect.top + rect.height / 2 - box.top + root.scrollTop) + 0.5,
  };
}

/** Horizontal → vertical → horizontal elbow between two side ports. */
function elbowH(from: Point, to: Point) {
  if (Math.abs(from.y - to.y) < 1) return `M ${from.x} ${from.y} H ${to.x}`;
  const midX = Math.round((from.x + to.x) / 2) + 0.5;
  return `M ${from.x} ${from.y} H ${midX} V ${to.y} H ${to.x}`;
}

/** Trunk down from a bottom port to a bus, then drops to each top port. */
function fanOut(from: Point, targets: Point[]) {
  if (targets.length === 0) return '';
  const busY = Math.round(from.y + (targets[0].y - from.y) / 2) + 0.5;
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
            aria-label={hrefLabel}>
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
          title="Metrics"
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
  const rootRef = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState<Edge[]>([]);

  // Key the measurement on node identity, not array identity: the parent
  // rebuilds `albs`/`instances` whenever live metrics tick, and geometry
  // only changes when nodes are added/removed (ResizeObserver covers size).
  const albIds = albs.map((alb) => alb.id).join('\u0000');
  const instanceIds = instances.map((instance) => instance.id).join('\u0000');

  useLayoutEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    const albList = albIds ? albIds.split('\u0000') : [];
    const instanceList = instanceIds ? instanceIds.split('\u0000') : [];

    const measure = () => {
      const next: Edge[] = [];
      const workloadIn = portCenter(root, root.querySelector('[data-port="workload-in"]'));
      const workloadOut = portCenter(root, root.querySelector('[data-port="workload-out"]'));

      for (const albId of albList) {
        const from = portCenter(root, root.querySelector(`[data-port="alb-${albId}"]`));
        if (from && workloadIn) {
          next.push({ kind: 'ingress', d: elbowH(from, workloadIn), albId, to: workloadIn });
        }
      }

      const targets = instanceList
        .map((id) => portCenter(root, root.querySelector(`[data-port="instance-${id}"]`)))
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
    observer.observe(root);
    for (const el of root.querySelectorAll('.cpt-node')) observer.observe(el);
    return () => observer.disconnect();
  }, [albIds, instanceIds, workload.id]);

  const hasIngress = albs.length > 0;
  const hasReplicas = instances.length > 0;

  return (
    <div ref={rootRef} data-testid="compute-plugin-topology-canvas" className="cpt-root">
      <style>{STYLES}</style>
      <div aria-hidden className="cpt-dots" />

      {chromeLeft || chromeRight ? (
        <div className="cpt-chrome" aria-hidden>
          <span>{chromeLeft}</span>
          <span>{chromeRight}</span>
        </div>
      ) : null}

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

      <div
        className="cpt-grid"
        style={{
          display: 'grid',
          gridTemplateColumns: hasIngress ? `${CARD_WIDTH}px minmax(0, 1fr)` : 'minmax(0, 1fr)',
          gridTemplateRows: hasReplicas ? 'auto auto' : 'auto',
          columnGap: hasIngress ? 96 : 0,
          rowGap: 48,
        }}>
        {hasIngress ? (
          <div className="cpt-ingress" style={{ gridColumn: 1, gridRow: 1 }}>
            {albs.map((alb) => (
              <div key={alb.id} className="cpt-node">
                <GraphCard>
                  <CardHead
                    icon={GlobeIcon}
                    tone="primary"
                    title={alb.title}
                    subtitle="Load balancer"
                    href={alb.href}
                    hrefLabel={`Open load balancer ${alb.title}`}
                    metricsHref={alb.metricsHref}
                    divided={!!alb.body}
                  />
                  {alb.body ? <div className="cpt-body">{alb.body}</div> : null}
                  <CardFoot
                    left={alb.hostname ?? '—'}
                    leftTitle={alb.hostname}
                    copyValue={alb.hostname}
                    copyLabel={`Copy hostname ${alb.hostname ?? ''}`}
                    right={<span className="cpt-muted">HTTPS</span>}
                  />
                </GraphCard>
                <Port id={`alb-${alb.id}`} side="right" />
              </div>
            ))}
          </div>
        ) : null}

        <div
          className="cpt-primary cpt-node"
          style={{ gridColumn: hasIngress ? 2 : 1, gridRow: 1 }}>
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
          {hasIngress ? <Port id="workload-in" side="left" /> : null}
          {hasReplicas ? <Port id="workload-out" side="bottom" /> : null}
        </div>

        {hasReplicas ? (
          <div
            className={`cpt-group${instances.length > 1 ? ' cpt-boxed' : ''}`}
            style={{ gridColumn: hasIngress ? 2 : 1, gridRow: 2 }}>
            {instances.map((instance) => (
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
                <Port id={`instance-${instance.id}`} side="top" />
              </div>
            ))}
          </div>
        ) : null}
      </div>
    </div>
  );
}
