/**
 * Deterministic placeholder trend line for a workload card. We don't have a
 * metrics backend wired up yet, so this renders a stable, seeded "fake"
 * sparkline (same shape math as the design's `sparkline.js` pencil script)
 * rather than a flat/empty chart area.
 */
import { useMemo } from 'react';

const VIEW_WIDTH = 300;
const VIEW_HEIGHT = 60;
const POINT_COUNT = 28;

function hashSeed(input: string): number {
  let hash = 0;
  for (let i = 0; i < input.length; i++) {
    hash = (hash << 5) - hash + input.charCodeAt(i);
    hash |= 0;
  }
  return (hash % 1000) / 1000;
}

function buildPaths(seed: number) {
  function rand(i: number) {
    const x = Math.sin(i * 9.301 + seed * 127.1) * 43758.5453;
    return x - Math.floor(x);
  }
  function smooth(i: number) {
    return rand(i - 1) * 0.25 + rand(i) * 0.5 + rand(i + 1) * 0.25;
  }

  const points: [number, number][] = [];
  for (let i = 0; i < POINT_COUNT; i++) {
    const x = (i / (POINT_COUNT - 1)) * VIEW_WIDTH;
    const y = VIEW_HEIGHT - (smooth(i) * VIEW_HEIGHT * 0.75 + VIEW_HEIGHT * 0.1);
    points.push([x, y]);
  }

  let line = `M ${points[0][0]} ${points[0][1]}`;
  for (let i = 1; i < points.length; i++) {
    const [x0, y0] = points[i - 1];
    const [x1, y1] = points[i];
    const cx = (x0 + x1) / 2;
    line += ` C ${cx} ${y0} ${cx} ${y1} ${x1} ${y1}`;
  }
  const area = `${line} L ${VIEW_WIDTH} ${VIEW_HEIGHT} L 0 ${VIEW_HEIGHT} Z`;

  return { line, area };
}

export function Sparkline({ seedKey, className }: { seedKey: string; className?: string }) {
  const { line, area } = useMemo(() => buildPaths(hashSeed(seedKey)), [seedKey]);

  return (
    <svg
      viewBox={`0 0 ${VIEW_WIDTH} ${VIEW_HEIGHT}`}
      preserveAspectRatio="none"
      className={className}
      data-testid="compute-plugin-workload-sparkline"
      aria-hidden="true">
      <path d={area} className="fill-muted-foreground/15" />
      <path d={line} fill="none" strokeWidth={1.5} className="stroke-foreground/70" />
    </svg>
  );
}
