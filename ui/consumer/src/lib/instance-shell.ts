export const EXEC_FEATURE = 'exec';

export interface RuntimeClassSummary {
  name: string;
  isDefault: boolean;
  features: string[];
}

export interface RawRuntimeClassList {
  items?: Array<{
    metadata?: { name?: string };
    spec?: { default?: boolean; capabilities?: { features?: string[] } };
  }>;
}

export function toRuntimeClasses(body: RawRuntimeClassList): RuntimeClassSummary[] {
  return (body.items ?? []).map((item) => ({
    name: item.metadata?.name ?? '',
    isDefault: !!item.spec?.default,
    features: item.spec?.capabilities?.features ?? [],
  }));
}

export function resolveRuntimeClass(
  instanceClass: string | undefined,
  classes: RuntimeClassSummary[]
): RuntimeClassSummary | undefined {
  if (instanceClass) return classes.find((c) => c.name === instanceClass);
  return classes.find((c) => c.isDefault);
}

export function canShowShell({
  clientBundled,
  canCreateSessions,
  sessionAllowance,
  runtimeClass,
  containers,
}: {
  clientBundled: boolean;
  canCreateSessions: boolean;
  sessionAllowance: number | undefined;
  runtimeClass: RuntimeClassSummary | undefined;
  containers: string[];
}): boolean {
  return (
    clientBundled &&
    canCreateSessions &&
    sessionAllowance !== 0 &&
    !!runtimeClass?.features.includes(EXEC_FEATURE) &&
    containers.length > 0
  );
}

export function initialContainer(containers: string[]): string | undefined {
  return containers.length === 1 ? containers[0] : undefined;
}

export interface ShellStatusBadgeStyle {
  label: string;
  type: 'success' | 'muted' | 'warning';
  theme: 'light' | 'solid';
}

// The muted badge's light theme draws pale grey text on white, so an ended
// session uses the solid theme, which pairs dark text with a tinted fill.
export function shellStatusBadge(state: { phase: string }): ShellStatusBadgeStyle {
  if (state.phase === 'connected') return { label: 'Connected', type: 'success', theme: 'light' };
  if (state.phase === 'ended') return { label: 'Ended', type: 'muted', theme: 'solid' };
  return { label: 'Opening', type: 'warning', theme: 'light' };
}
