import FleetWorkloads from './pages/fleet-workloads';
import ServiceOverview from './pages/service-overview';
import WorkloadDetail from './pages/workload-detail';
import WorkloadList from './pages/workload-list';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Link, MemoryRouter, Route, Routes } from 'react-router';

// Standalone preview only. Wraps the page in a MemoryRouter so `useParams()`
// resolves the same params the host's project-scoped plugin mount would
// supply, and a QueryClientProvider so the data hooks run — exactly what the
// host provides in production. Data calls hit /api/internal/... which 404s
// standalone (no staff-portal proxy), so the page shows its error state;
// that's expected here. Run the full staff-portal to see live data.
const queryClient = new QueryClient();

const base = '/customers/projects/:projectName/plugins/workloads';
const workloadsBase = '/admin/service-catalog/:name/plugins/workloads/workloads';
const overviewBase = '/admin/service-catalog/:name';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <div
        style={{ maxWidth: 960, margin: '2rem auto', padding: '0 1rem', fontFamily: 'system-ui' }}>
        <p style={{ opacity: 0.6 }}>
          Standalone preview — staff-portal loads this plugin via
          <code> /plugin-manifest.json</code> and <code>/remoteEntry.js</code>, not this page.
          The data tabs show their error state here (no staff-portal proxy). Run the full
          staff-portal to see live data.
        </p>
        <MemoryRouter initialEntries={['/customers/projects/demo-project/plugins/workloads']}>
          <nav style={{ display: 'flex', gap: '1rem', margin: '0 0 1rem' }}>
            <Link to="/customers/projects/demo-project/plugins/workloads">Workload list</Link>
            <Link to="/customers/projects/demo-project/plugins/workloads/demo-workload">
              Workload detail
            </Link>
            <Link to="/admin/service-catalog/compute/plugins/workloads/workloads">
              Fleet workloads
            </Link>
            <Link to="/admin/service-catalog/compute">Service overview</Link>
          </nav>
          <Routes>
            <Route path={base} element={<WorkloadList />} />
            <Route path={`${base}/:workloadName`} element={<WorkloadDetail />} />
            <Route path={workloadsBase} element={<FleetWorkloads />} />
            <Route path={overviewBase} element={<ServiceOverview />} />
          </Routes>
        </MemoryRouter>
      </div>
    </QueryClientProvider>
  </StrictMode>
);
