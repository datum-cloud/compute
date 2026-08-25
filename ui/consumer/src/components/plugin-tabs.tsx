/**
 * Detail-page tab bar. Visually matches cloud-portal's SubLayout sub-nav:
 * the border bleeds full-width across the ContentWrapper padding
 * (`-mx-4 md:-mx-9` cancels `p-4` / `md:p-9`).
 *
 * Tabs with an `href` are navigable; others stay disabled placeholders.
 */
import { cn } from '@datum-cloud/datum-ui/utils';
import { Link, useLocation } from 'react-router';

export type PluginTab = {
  label: string;
  /** When set, the tab is a real link. Otherwise it is a disabled placeholder. */
  href?: string;
};

export function PluginTabs({ tabs, testId }: { tabs: PluginTab[]; testId?: string }) {
  const { pathname } = useLocation();

  const activeHref = (() => {
    let best = '';
    for (const tab of tabs) {
      if (!tab.href) continue;
      if (pathname === tab.href || pathname.startsWith(`${tab.href}/`)) {
        if (tab.href.length > best.length) best = tab.href;
      }
    }
    return best;
  })();

  // When no tab href matches (e.g. placeholder-only bars), highlight the first tab.
  const fallbackActive = !activeHref;

  return (
    <div
      className="border-border -mx-4 border-b px-4 md:-mx-9 md:px-9"
      data-testid={testId}>
      <div className="-mx-1 flex h-auto w-full justify-start gap-0 overflow-x-auto overscroll-x-contain px-1 [-ms-overflow-style:none] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {tabs.map((tab, index) => {
          const enabled = !!tab.href;
          const active = enabled
            ? tab.href === activeHref
            : fallbackActive && index === 0;
          const className = cn(
            'relative flex w-fit shrink-0 items-center px-0 py-2.5 text-xs md:py-2',
            'mx-3.5 first:ml-0 last:mr-0',
            active
              ? 'text-primary font-medium'
              : enabled
                ? 'text-muted-foreground hover:text-foreground font-normal'
                : 'text-muted-foreground cursor-not-allowed font-normal'
          );

          if (enabled && tab.href) {
            return (
              <Link key={tab.label} to={tab.href} className={className}>
                {tab.label}
                {active && <span className="bg-primary absolute inset-x-0 bottom-0 h-0.5" />}
              </Link>
            );
          }

          return (
            <div key={tab.label} className={className} aria-disabled={!active}>
              {tab.label}
              {active && <span className="bg-primary absolute inset-x-0 bottom-0 h-0.5" />}
            </div>
          );
        })}
      </div>
    </div>
  );
}
