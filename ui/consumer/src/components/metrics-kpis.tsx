export function MetricsKpiCell({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  const muted =
    value === '—' || value === 'Not connected' || value === 'Loading…' || value === 'Not collected yet';
  return (
    <div className="flex min-w-24 flex-1 flex-col gap-1 px-3 py-3">
      <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">{label}</p>
      <p
        className={
          muted ? 'text-muted-foreground text-sm font-medium' : 'text-sm font-medium sm:text-base'
        }>
        {value}
      </p>
      {hint && !muted ? <p className="text-muted-foreground text-xs">{hint}</p> : null}
    </div>
  );
}

export function MetricsKpiRow({ children }: { children: React.ReactNode }) {
  return (
    <div
      className="divide-border border-border flex divide-x overflow-x-auto rounded-lg border"
      style={{ overscrollBehaviorX: 'contain' }}>
      {children}
    </div>
  );
}
