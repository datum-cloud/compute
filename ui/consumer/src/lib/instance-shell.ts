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
  runtimeClass,
  containers,
}: {
  clientBundled: boolean;
  canCreateSessions: boolean;
  runtimeClass: RuntimeClassSummary | undefined;
  containers: string[];
}): boolean {
  return (
    clientBundled &&
    canCreateSessions &&
    !!runtimeClass?.features.includes(EXEC_FEATURE) &&
    containers.length > 0
  );
}

export function initialContainer(containers: string[]): string | undefined {
  return containers.length === 1 ? containers[0] : undefined;
}
