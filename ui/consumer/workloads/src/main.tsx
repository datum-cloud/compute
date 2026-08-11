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
// in production. Data calls hit /api/proxy/... which 404s standalone (no
// portal proxy), so the data pages show their error states; that's expected
// here. Run the full portal to see live data.
const queryClient = new QueryClient();

const base = '/project/:projectId/services/:serviceSlug';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <div
        style={{ maxWidth: 960, margin: '2rem auto', padding: '0 1rem', fontFamily: 'system-ui' }}>
        <p style={{ opacity: 0.6 }}>
          Standalone preview — the portal loads this plugin via
          <code> /plugin-manifest.json</code> and <code>/remoteEntry.js</code>, not this page. Data
          pages show their error state here (no portal proxy). Run the full portal to see live
          data.
        </p>
        <MemoryRouter
          initialEntries={['/project/demo-project/services/compute/workloads']}>
          <nav style={{ display: 'flex', gap: '1rem', margin: '0 0 1rem' }}>
            <Link to="/project/demo-project/services/compute/workloads">Workloads</Link>
            <Link to="/project/demo-project/services/compute/workloads/demo-workload">
              Workload detail
            </Link>
            <Link to="/project/demo-project/services/compute/workloads/demo-workload/instances/demo-instance">
              Instance detail
            </Link>
          </nav>
          <Routes>
            <Route path={`${base}/workloads`} element={<WorkloadList />} />
            <Route path={`${base}/workloads/:workloadName`} element={<WorkloadDetail />} />
            <Route
              path={`${base}/workloads/:workloadName/instances/:instanceName`}
              element={<InstanceDetail />}
            />
          </Routes>
        </MemoryRouter>
      </div>
    </QueryClientProvider>
  </StrictMode>
);
