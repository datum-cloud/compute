import { Children, Fragment, isValidElement, type ReactNode } from 'react';

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
    <div className="flex flex-col gap-1 px-3 py-3">
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

/** Cells passed inside fragments (`{cond ? <>…</> : …}`) count one by one. */
function flattenCells(children: ReactNode): ReactNode[] {
  return Children.toArray(children).flatMap((child) =>
    isValidElement<{ children?: ReactNode }>(child) && child.type === Fragment
      ? flattenCells(child.props.children)
      : [child]
  );
}

/**
 * Two columns on phones, one row from `sm` up. The 1px gap over a border-
 * coloured background draws the dividers in both layouts; an odd last cell
 * spans the full width on phones (the span is ignored once the row is flex).
 */
export function MetricsKpiRow({ children }: { children: ReactNode }) {
  const cells = flattenCells(children);
  return (
    <div className="bg-border border-border grid grid-cols-2 gap-px overflow-hidden rounded-lg border sm:flex">
      {cells.map((cell, index) => (
        <div
          key={index}
          className="bg-card min-w-0 flex-1"
          style={
            cells.length % 2 === 1 && index === cells.length - 1 ? { gridColumn: 'span 2' } : undefined
          }>
          {cell}
        </div>
      ))}
    </div>
  );
}
