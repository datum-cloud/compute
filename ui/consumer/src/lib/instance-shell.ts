// The portal only opens shells with a terminal, so it needs a class that
// serves terminals, not just exec.
export const TERMINAL_FEATURE = 'execTerminal';

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
    !!runtimeClass?.features.includes(TERMINAL_FEATURE) &&
    containers.length > 0
  );
}

export function initialContainer(containers: string[]): string | undefined {
  return containers[0];
}

export interface ShellStatusBadgeStyle {
  label: string;
  type: 'success' | 'muted' | 'warning';
}

export function shellStatusBadge(state: { phase: string }): ShellStatusBadgeStyle {
  if (state.phase === 'connected') return { label: 'Connected', type: 'success' };
  if (state.phase === 'ended') return { label: 'Ended', type: 'muted' };
  return { label: 'Opening', type: 'warning' };
}
