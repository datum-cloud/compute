/**
 * Shared tab bar for the workload/instance detail pages. Only the first tab
 * ("Overview") has a real view in this plugin's v1 scope — the rest render
 * disabled rather than linking to pages that don't exist yet.
 */
import { cn } from '@datum-cloud/datum-ui/utils';

export function PluginTabs({ tabs, testId }: { tabs: string[]; testId?: string }) {
  return (
    <div className="border-border flex w-full border-b" data-testid={testId}>
      {tabs.map((tab, index) => {
        const active = index === 0;
        return (
          <div
            key={tab}
            className={cn(
              'border-b-2 px-4 py-2 text-sm',
              active
                ? 'border-foreground text-foreground font-medium'
                : 'text-muted-foreground cursor-not-allowed border-transparent'
            )}>
            {tab}
          </div>
        );
      })}
    </div>
  );
}
