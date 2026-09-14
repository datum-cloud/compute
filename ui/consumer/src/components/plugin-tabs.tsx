/**
 * Detail-page tab bar matching cloud-portal's SubNavigationTabs: datum-ui
 * `TabsList variant="line"` + `TabsLinkTrigger`. The border bleeds full-width
 * across the ContentWrapper padding (`-mx-4 md:-mx-9` cancels `p-4` / `md:p-9`).
 *
 * Tabs with an `href` are navigable; others stay disabled placeholders.
 */
import { Tabs, TabsLinkTrigger, TabsList, TabsTrigger } from '@datum-cloud/datum-ui/tabs';
import { useMemo } from 'react';
import { Link, useLocation } from 'react-router';

export type PluginTab = {
  label: string;
  /** When set, the tab is a real link. Otherwise it is a disabled placeholder. */
  href?: string;
};

export function PluginTabs({ tabs, testId }: { tabs: PluginTab[]; testId?: string }) {
  const { pathname } = useLocation();

  const activeHref = useMemo(() => {
    let best = '';
    for (const tab of tabs) {
      if (!tab.href) continue;
      if (pathname === tab.href || pathname.startsWith(`${tab.href}/`)) {
        if (tab.href.length > best.length) best = tab.href;
      }
    }
    return best;
  }, [pathname, tabs]);

  // When no tab href matches (placeholder-only bars), highlight the first tab.
  const activeValue = activeHref || tabs[0]?.href || tabs[0]?.label || '';

  return (
    <div className="border-border -mx-4 border-b px-4 md:-mx-9 md:px-9" data-testid={testId}>
      <Tabs value={activeValue} className="gap-0">
        <TabsList variant="line">
          {tabs.map((tab) =>
            tab.href ? (
              <TabsLinkTrigger
                key={tab.label}
                value={tab.href}
                href={tab.href}
                linkComponent={Link}
                className="text-xs md:py-2">
                {tab.label}
              </TabsLinkTrigger>
            ) : (
              <TabsTrigger
                key={tab.label}
                value={tab.label}
                disabled={activeValue !== tab.label}
                className="text-xs md:py-2">
                {tab.label}
              </TabsTrigger>
            )
          )}
        </TabsList>
      </Tabs>
    </div>
  );
}
