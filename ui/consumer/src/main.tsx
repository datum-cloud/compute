import '../dev/harness.css';
declare const __HARNESS_UPSTREAM__: string;

import InstanceDetail from './pages/instance-detail';
import WorkloadDetail from './pages/workload-detail';
import WorkloadList from './pages/workload-list';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Link, MemoryRouter, Route, Routes } from 'react-router';

// Standalone preview only. Wraps the pages in a MemoryRouter so `useParams()`
// resolves the same params the host mount would supply, and a
// QueryClientProvider so the data hooks run — exactly what the host provides
// in production. Styling comes from dev/harness.css (the host's Tailwind
// build, reproduced) and data from dev/mock-proxy.ts fixtures, so the pages
// render as they do inside cloud-portal for a project named "demo-project".
const queryClient = new QueryClient();

const base = '/project/:projectId/services/:serviceSlug';
const mount = '/project/demo-project/services/workloads';

// `?path=` picks the page to open, e.g. ?path=/project/demo-project/services/
// workloads/global-mesh/instances, so screenshot scripts can target one page.
const requested = new URLSearchParams(window.location.search).get('path');
const initialPath = requested && requested.startsWith('/') ? requested : mount;

const links: Array<[label: string, to: string]> = [
  ['Workloads', mount],
  ['Workload detail', `${mount}/global-mesh`],
  ['Instances tab', `${mount}/global-mesh/instances`],
  ['Instance detail', `${mount}/global-mesh/instances/global-mesh-north-america-us-central-1-0`],
];

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialPath]}>
        <div className="bg-background text-foreground flex min-h-svh flex-col">
          <header className="border-border bg-muted/40 text-muted-foreground flex items-center gap-4 border-b px-4 py-2 text-xs md:px-9">
            <span>
              Standalone preview — the portal loads <code>/remoteEntry.js</code>, not this page.
              Data comes from{' '}
              {__HARNESS_UPSTREAM__ ? (
                <code>{__HARNESS_UPSTREAM__}</code>
              ) : (
                <code>dev/fixtures/</code>
              )}
              .
            </span>
            <nav className="ml-auto flex gap-3">
              {links.map(([label, to]) => (
                <Link key={to} to={to} className="text-foreground hover:underline">
                  {label}
                </Link>
              ))}
            </nav>
          </header>
          {/* Mirrors the portal's content column: flex column, px-4 / md:px-9, capped width. */}
          <main
            className="flex w-full flex-1 flex-col px-4 py-6 md:px-9"
            style={{ maxWidth: 1200, margin: '0 auto' }}
          >
            <Routes>
              <Route path={base} element={<WorkloadList />} />
              <Route path={`${base}/:workloadName/*`} element={<WorkloadDetail />} />
              <Route
                path={`${base}/:workloadName/instances/:instanceName/*`}
                element={<InstanceDetail />}
              />
            </Routes>
          </main>
        </div>
      </MemoryRouter>
    </QueryClientProvider>
  </StrictMode>,
);
