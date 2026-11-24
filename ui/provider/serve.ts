/**
 * Minimal static file server for the built plugin bundle (dist/), with TLS
 * termination via node:https. Internal-only — no Gateway fronts this service,
 * so the only client is staff-portal's own server (see Dockerfile for why TLS is
 * required regardless). Cert/key come from the cert-manager CSI volume mounted
 * by the Deployment.
 *
 * Node runs this file directly by stripping its types. Content types match
 * what Bun.file reported, which this server replaced; it is the same server
 * as network-services-operator's consumer plugin.
 */
import { createReadStream, readFileSync } from "node:fs";
import { realpath, stat } from "node:fs/promises";
import type { IncomingMessage, ServerResponse } from "node:http";
import { createServer } from "node:https";
import { extname, join, resolve, sep } from "node:path";
import { pipeline } from "node:stream";

const DIST_DIR = resolve(import.meta.dirname, "dist");
const REAL_DIST_DIR = await realpath(DIST_DIR);
const TLS_DIR = "/etc/tls";
const PORT = 8443;
const HOST = "0.0.0.0";
const SHUTDOWN_DEADLINE_MS = 25_000;

const MIME_TYPES: Record<string, string> = {
  ".html": "text/html;charset=utf-8",
  ".js": "text/javascript;charset=utf-8",
  ".mjs": "text/javascript;charset=utf-8",
  ".cjs": "text/javascript;charset=utf-8",
  ".ts": "text/javascript;charset=utf-8",
  ".css": "text/css;charset=utf-8",
  ".json": "application/json;charset=utf-8",
  ".map": "application/json;charset=utf-8",
  ".txt": "text/plain;charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
  ".ico": "image/x-icon",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".ttf": "font/ttf",
  ".wasm": "application/wasm",
  ".zip": "application/zip",
};

function notFound(res: ServerResponse): void {
  res.writeHead(404, { "content-type": "text/plain;charset=utf-8" });
  res.end("Not Found");
}

async function handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
  const { pathname } = new URL(req.url ?? "/", "https://localhost");
  const filePath = resolve(join(DIST_DIR, pathname));
  if (!filePath.startsWith(DIST_DIR + sep)) {
    notFound(res);
    return;
  }

  const realFilePath = await realpath(filePath).catch(() => null);
  if (realFilePath === null || !realFilePath.startsWith(REAL_DIST_DIR + sep)) {
    notFound(res);
    return;
  }

  const info = await stat(realFilePath).catch(() => null);
  if (!info?.isFile()) {
    notFound(res);
    return;
  }

  res.writeHead(200, {
    "content-type":
      MIME_TYPES[extname(realFilePath).toLowerCase()] ?? "application/octet-stream",
    "content-length": info.size,
  });
  if (req.method === "HEAD") {
    res.end();
    return;
  }
  pipeline(createReadStream(realFilePath), res, (err) => {
    if (!err || res.headersSent) {
      return;
    }
    res.writeHead(500, { "content-type": "text/plain;charset=utf-8" });
    res.end("Internal Server Error");
  });
}

const server = createServer(
  {
    cert: readFileSync(join(TLS_DIR, "tls.crt")),
    key: readFileSync(join(TLS_DIR, "tls.key")),
  },
  (req, res) => {
    handle(req, res).catch(() => {
      if (res.headersSent) {
        res.destroy();
      } else {
        notFound(res);
      }
    });
  },
);

// Callers (the portal servers) pool connections, so do not close idle sockets
// before them; this matches auth-ui, staff-portal and agents.datum.net.
server.keepAliveTimeout = 75_000;
server.headersTimeout = 76_000;

server.listen(PORT, HOST, () => {
  console.log(`Serving ${DIST_DIR} on https://${HOST}:${PORT}`);
});

let shuttingDown = false;

function shutdown(signal: NodeJS.Signals): void {
  if (shuttingDown) {
    return;
  }
  shuttingDown = true;
  console.log(`${signal} received, draining connections`);
  server.close();
  server.closeIdleConnections();
  setTimeout(() => {
    server.closeAllConnections();
    process.exit(0);
  }, SHUTDOWN_DEADLINE_MS).unref();
}

process.once("SIGTERM", shutdown);
process.once("SIGINT", shutdown);
