/**
 * Same dot-matrix world map image used by cloud-portal's "Active POPs" map
 * (app/features/edge/proxy/overview/active-pops-flat-map.tsx). Reused here as
 * a static backdrop for the workload detail page's "Instance Locations" card.
 *
 * Unlike that component, this renders the background only — compute regions
 * (e.g. `DFW`) aren't in the edge module's lat/lng table, so there's no
 * reliable way to plot per-region markers on it.
 *
 * Loaded via `?raw` and inlined rather than referenced by URL: this plugin is
 * loaded as a Module Federation remote, and the portal's asset proxy only
 * rewrites federated JS chunk URLs, not plain Vite asset imports — an
 * `<img src>` pointing at the built asset path resolves against the host
 * portal's origin instead of the plugin's, and 404s.
 */
import worldMapMarkup from '../assets/world-map-dots.svg?raw';

const RESPONSIVE_MARKUP = worldMapMarkup.replace(
  'width="1038" height="591"',
  'width="100%" height="100%" preserveAspectRatio="xMidYMid meet"'
);

export function WorldMap({ className }: { className?: string }) {
  return (
    <div
      className={className}
      data-testid="compute-plugin-world-map"
      dangerouslySetInnerHTML={{ __html: RESPONSIVE_MARKUP }}
    />
  );
}
