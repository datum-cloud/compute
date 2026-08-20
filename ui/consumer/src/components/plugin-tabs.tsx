/**
 * Detail-page tab bar. Visually matches cloud-portal's SubLayout sub-nav:
 * the border bleeds full-width across the ContentWrapper padding
 * (`-mx-4 md:-mx-9` cancels `p-4` / `md:p-9`).
 *
 * Only the first tab ("Overview") is active in v1 — the rest are disabled
 * placeholders until those views exist.
 */
import { cn } from '@datum-cloud/datum-ui/utils';

export function PluginTabs({ tabs, testId }: { tabs: string[]; testId?: string }) {
  return (
    <div
      className="border-border -mx-4 border-b px-4 md:-mx-9 md:px-9"
      data-testid={testId}>
      <div className="-mx-1 flex h-auto w-full justify-start gap-0 overflow-x-auto overscroll-x-contain px-1 [-ms-overflow-style:none] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {tabs.map((tab, index) => {
          const active = index === 0;
          return (
            <div
              key={tab}
              className={cn(
                'relative flex w-fit shrink-0 items-center px-0 py-2.5 text-xs md:py-2',
                'mx-3.5 first:ml-0 last:mr-0',
                active
                  ? 'text-primary font-medium'
                  : 'text-muted-foreground cursor-not-allowed font-normal'
              )}>
              {tab}
              {active && <span className="bg-primary absolute inset-x-0 bottom-0 h-0.5" />}
            </div>
          );
        })}
      </div>
    </div>
  );
}
