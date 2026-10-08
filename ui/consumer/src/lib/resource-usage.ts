/**
 * CPU and memory usage as a share of what each instance was allocated. CPU
 * arrives as cores in use (rate of CPU-seconds) and memory as bytes; charts
 * and headline numbers show both as a percentage, falling back to cores or
 * bytes when an instance's size is unknown.
 */

/** "500m" → 0.5, "2" → 2. */
export function parseCpuCores(value?: string): number | undefined {
  if (!value) return undefined;
  const trimmed = value.trim();
  if (trimmed.endsWith('m')) {
    const n = Number(trimmed.slice(0, -1));
    return Number.isFinite(n) && n > 0 ? n / 1000 : undefined;
  }
  const n = Number(trimmed);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

/**
 * Kubernetes quantity ("2Gi", "512Mi", "1G", "1e9", "500m") → bytes. Suffixes
 * are case-sensitive as in k8s: `m` is milli, `M` mega; binary suffixes end
 * in `i`.
 */
export function parseMemoryBytes(value?: string): number | undefined {
  if (!value) return undefined;
  const match = value.trim().match(/^(\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)\s*(Ki|Mi|Gi|Ti|Pi|Ei|[mkKMGTPE])?$/);
  if (!match) return undefined;
  const n = Number(match[1]);
  if (!Number.isFinite(n)) return undefined;
  const table: Record<string, number> = {
    m: 1e-3,
    k: 1e3,
    K: 1e3,
    M: 1e6,
    G: 1e9,
    T: 1e12,
    P: 1e15,
    E: 1e18,
    Ki: 1024,
    Mi: 1024 ** 2,
    Gi: 1024 ** 3,
    Ti: 1024 ** 4,
    Pi: 1024 ** 5,
    Ei: 1024 ** 6,
  };
  const factor = match[2] ? table[match[2]] : 1;
  return factor && n > 0 ? n * factor : undefined;
}

type SizedInstance = { name: string; cpu?: string; memory?: string };

/**
 * One allocation per instance, keyed by name, or undefined when any
 * instance's size is unknown (a percentage would then be wrong for it).
 */
function allocatedPerInstance(
  instances: readonly SizedInstance[],
  size: (instance: SizedInstance) => number | undefined
): Map<string, number> | undefined {
  const allocated = new Map<string, number>();
  for (const instance of instances) {
    const n = size(instance);
    if (n === undefined) return undefined;
    allocated.set(instance.name, n);
  }
  return allocated.size > 0 ? allocated : undefined;
}

export function allocatedCores(instances: readonly SizedInstance[]): Map<string, number> | undefined {
  return allocatedPerInstance(instances, (instance) => parseCpuCores(instance.cpu));
}

export function allocatedMemory(instances: readonly SizedInstance[]): Map<string, number> | undefined {
  return allocatedPerInstance(instances, (instance) => parseMemoryBytes(instance.memory));
}

/** Sum of `allocated` over `instances`, or undefined when sizes are unknown. */
export function allocatedTotal(
  allocated: Map<string, number> | undefined,
  instances: readonly { name: string }[]
): number | undefined {
  if (!allocated) return undefined;
  return instances.reduce((total, instance) => total + (allocated.get(instance.name) ?? 0), 0);
}

/** Total vCPU and memory allocated to a set of instances; a field is undefined when any size is unknown. */
export interface Allocation {
  cores?: number;
  memoryBytes?: number;
}

export function allocationOf(instances: readonly SizedInstance[]): Allocation {
  return {
    cores: allocatedTotal(allocatedCores(instances), instances),
    memoryBytes: allocatedTotal(allocatedMemory(instances), instances),
  };
}

export function formatBytes(value: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let n = value;
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i += 1;
  }
  return `${n.toFixed(n >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

/** Fixed decimals without trailing zeros: 0.0020 → "0.002", 1.000 → "1". */
function trimmedFixed(n: number, decimals: number): string {
  const fixed = n.toFixed(decimals);
  return fixed.includes('.') ? fixed.replace(/0+$/, '').replace(/\.$/, '') : fixed;
}

/** Decimals that keep `significant` digits of a value below 1. */
function decimalsFor(n: number, significant: number): number {
  return Math.max(0, significant - 1 - Math.floor(Math.log10(n)));
}

/**
 * Cores as a plain decimal, never SI-prefixed: "2.1m" reads as minutes or
 * millions. Values stop at hundredths of a core ("0.01", "1.5"); a non-zero
 * figure below that reads "<0.01" rather than looking like none. Axis ticks
 * keep two significant digits so an idle instance's axis isn't all "0".
 */
export function formatCores(value: number, forTick: boolean): string {
  if (!Number.isFinite(value)) return forTick ? '' : '—';
  const n = Math.abs(value);
  if (n < 1e-9) return '0';
  const sign = value < 0 ? '-' : '';
  if (n >= 100) return `${sign}${n.toFixed(0)}`;
  if (!forTick) {
    const rounded = trimmedFixed(n, 2);
    return rounded === '0' ? '<0.01' : `${sign}${rounded}`;
  }
  if (n >= 1) return `${sign}${trimmedFixed(n, 2)}`;
  if (n < 1e-6) return `${sign}${n.toExponential(0)}`;
  return `${sign}${trimmedFixed(n, Math.min(6, decimalsFor(n, 2)))}`;
}

/**
 * A 0–1 ratio as a percentage that stays readable when tiny: an idle
 * instance at 0.07% must not round to "0%".
 */
export function formatPercent(ratio: number, forTick: boolean): string {
  if (!Number.isFinite(ratio)) return forTick ? '' : '—';
  const pct = Math.abs(ratio * 100);
  if (pct < 1e-9) return '0%';
  const sign = ratio < 0 ? '-' : '';
  if (pct >= 10) return `${sign}${pct.toFixed(0)}%`;
  if (pct >= 1) return `${sign}${trimmedFixed(pct, 1)}%`;
  if (pct < 0.001) return forTick ? `${sign}${pct.toExponential(0)}%` : '<0.001%';
  return `${sign}${trimmedFixed(pct, Math.min(3, decimalsFor(pct, 2)))}%`;
}

/** Headline CPU figure: a share of `allocated` cores, or cores in use when that is unknown. */
export function formatCpuUsage(cores: number | undefined, allocated?: number): string {
  if (cores === undefined || !Number.isFinite(cores)) return '—';
  if (allocated) return formatPercent(cores / allocated, false);
  return formatCores(cores, false);
}

/** The absolute figure behind a percentage, "0.0081 of 5 vCPU"; "cores in use" without one. */
export function cpuUsageHint(cores: number | undefined, allocated?: number): string {
  if (!allocated) return 'cores in use';
  const used = cores !== undefined && Number.isFinite(cores) ? `${formatCores(cores, false)} ` : '';
  return `${used}of ${formatCores(allocated, false)} vCPU`;
}

/** Headline memory figure: a share of `allocated` bytes, or bytes in use when that is unknown. */
export function formatMemoryUsage(bytes: number | undefined, allocated?: number): string {
  if (bytes === undefined || !Number.isFinite(bytes)) return '—';
  if (allocated) return formatPercent(bytes / allocated, false);
  return formatBytes(bytes);
}

/** The absolute figure behind a percentage, "190 MB of 2.0 GB"; "in use" without one. */
export function memoryUsageHint(bytes: number | undefined, allocated?: number): string {
  if (!allocated) return 'in use';
  const used = bytes !== undefined && Number.isFinite(bytes) ? `${formatBytes(bytes)} ` : '';
  return `${used}of ${formatBytes(allocated)}`;
}
