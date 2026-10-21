#!/usr/bin/env node
/**
 * Full-page screenshot of one harness page, for eyeballing the plugin without
 * cloud-portal.
 *
 *   bun run dev   # in another terminal (serves :7778 with fixtures)
 *   NODE_PATH=/path/to/cloud-portal/node_modules \
 *     node dev/screenshot.mjs /project/demo-project/services/workloads /tmp/list.png
 *
 * Playwright is resolved from NODE_PATH (cloud-portal already has it and the
 * browsers installed), so this plugin takes no dependency on it. The script
 * waits for the network to settle and for a page root to appear, then captures
 * a 1440px-wide full-page PNG.
 */
import { createRequire } from 'node:module';

const [, , pagePath = '/project/demo-project/services/workloads', outFile = '/tmp/compute-ui.png'] =
  process.argv;
const origin = process.env.HARNESS_ORIGIN ?? 'http://localhost:7778';

const require = createRequire(import.meta.url);
let chromium;
try {
  ({ chromium } = require('playwright'));
} catch {
  console.error('playwright not found: set NODE_PATH to a node_modules that has it installed');
  process.exit(2);
}

// Any of these marks a page that has finished loading its data.
const READY = [
  '[data-testid="compute-plugin-workload-detail"]',
  '[data-testid="compute-plugin-workload-general"]',
  '[data-testid="compute-plugin-workload-list"]:not(:has([aria-busy="true"]))',
  '[data-testid="compute-plugin-workload-table"]',
  '[data-testid="compute-plugin-workload-grid"]',
  '[data-testid="compute-plugin-instance-overview"]',
  '[data-testid="compute-plugin-instances-tab"]',
  '[data-testid="compute-plugin-error"]',
  '[data-testid="compute-plugin-restricted"]',
].join(', ');

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const url = `${origin}/?path=${encodeURIComponent(pagePath)}`;
  await page.goto(url, { waitUntil: 'networkidle' });
  try {
    await page.waitForSelector(READY, { timeout: 15_000 });
    // Skeletons swap out a beat after the data arrives.
    await page.waitForFunction(() => !document.querySelector('[aria-busy="true"]'), null, {
      timeout: 10_000,
    });
  } catch {
    console.warn('page root did not settle in time; capturing anyway');
  }
  await page.waitForLoadState('networkidle');
  await page.screenshot({ path: outFile, fullPage: true });
  console.log(`saved ${outFile} (${pagePath})`);
} finally {
  await browser.close();
}
