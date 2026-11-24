/**
 * The instance overview's problem callout: what is wrong in plain words, the
 * platform's own message, and what to try. Shown only while the instance is
 * failing or crashing; the General card's status badge stays as the summary.
 */
import { STATUS_TONE_COLOR, type InstanceProblem } from '../lib/workload-presenters';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { CircleXIcon, LogsIcon, TriangleAlertIcon } from 'lucide-react';
import { Link } from 'react-router';

function tint(color: string, percent: number): string {
  return `color-mix(in oklab, ${color} ${percent}%, transparent)`;
}

export function InstanceProblemCallout({ problem, logsHref }: { problem: InstanceProblem; logsHref?: string }) {
  const color = STATUS_TONE_COLOR[problem.tone];
  return (
    <section
      role="status"
      aria-label={problem.title}
      className="flex gap-3 rounded-xl border px-4 py-4"
      style={{ background: tint(color, 6), borderColor: tint(color, 40) }}
      data-testid="compute-plugin-instance-problem">
      <Icon
        icon={problem.tone === 'error' ? CircleXIcon : TriangleAlertIcon}
        size={18}
        className="mt-0.5 shrink-0"
        style={{ color }}
        aria-hidden
      />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-sm font-semibold">{problem.title}</h2>
          {problem.showLogs && logsHref ? (
            <Link
              to={logsHref}
              className="bg-foreground text-background inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium hover:opacity-90">
              <Icon icon={LogsIcon} size={12} aria-hidden />
              View logs
            </Link>
          ) : null}
        </div>
        {problem.message ? (
          <p className="text-muted-foreground font-mono text-xs" style={{ overflowWrap: 'anywhere' }}>
            {problem.message}
          </p>
        ) : null}
        {problem.hints.length > 0 ? (
          <ul className="flex flex-col gap-1 text-sm">
            {problem.hints.map((hint) => (
              <li key={hint} className="flex gap-2">
                <span className="text-muted-foreground" aria-hidden>
                  •
                </span>
                <span>{hint}</span>
              </li>
            ))}
          </ul>
        ) : null}
      </div>
    </section>
  );
}
