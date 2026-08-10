/**
 * Minimal static file server for the built plugin bundle (dist/), with TLS
 * termination via Bun.serve's native tls option. Internal-only — no Gateway
 * fronts this service, so the only client is cloud-portal's own server (see
 * Dockerfile for why TLS is required regardless). Cert/key come from the
 * cert-manager CSI volume mounted by the Deployment.
 */
import { join, normalize } from "node:path";

const DIST_DIR = join(import.meta.dir, "dist");
const TLS_DIR = "/etc/tls";

Bun.serve({
  port: 8443,
  tls: {
    cert: Bun.file(join(TLS_DIR, "tls.crt")),
    key: Bun.file(join(TLS_DIR, "tls.key")),
  },
  async fetch(req) {
    const url = new URL(req.url);
    // Strip any leading slashes/.. segments so requests can't escape DIST_DIR.
    const path = normalize(url.pathname).replace(/^(\.\.[/\\])+/, "");
    const file = Bun.file(join(DIST_DIR, path));

    if (await file.exists()) {
      return new Response(file);
    }
    return new Response("Not Found", { status: 404 });
  },
});

console.log(`Serving ${DIST_DIR} on https://0.0.0.0:8443`);
