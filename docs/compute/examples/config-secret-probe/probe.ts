// probe.ts
//
// ConfigMap/Secret mount-verification probe for the Datum Cloud unikraft runtime.
//
// On startup it reads a configurable set of mounted FILES and injected ENV vars,
// then prints ONE line `PROBE_RESULT {json}` to stdout/console. That console line
// is the observation channel: UKC captures the unikernel stdout and it is
// retrievable with `kraft cloud instance logs <name>` (see the mount-runtime-probe
// RFC). It is independent of kraftlet's missing exec/logs/port-forward subresources.
//
// For each item it reports {name, exists, size, sha256, preview}. For SECRET files
// only sha256 + a short preview is printed -- never the full secret bytes -- because
// the console log is readable by anyone holding the metro token. The source sha256 is
// computed the same way (sha256 of the raw value), so a byte-exact mount is provable
// by comparing sha256("<sentinel>") against the reported sha256.
//
// After printing once, the process stays alive (HTTP server) so the instance keeps
// running and `instance logs -f` follows a stable instance. `GET /probe` returns the
// same JSON as a secondary observation path for when a real ingress exists.

import { createHash } from "node:crypto";
import { readFileSync, statSync } from "node:fs";

const PORT = Number(Bun.env.PORT ?? 3000);

// Paths whose backing value is a secret. Files mounted from these paths, and env
// vars named here, are treated as sensitive: preview is suppressed (sha256 only).
// Defaults cover the e2e secret mount/env; override via PROBE_SECRET_PATHS /
// PROBE_SECRET_ENV_VARS if needed.
const SECRET_PATHS = splitList(
  Bun.env.PROBE_SECRET_PATHS ?? "/etc/secret/token",
);
const SECRET_ENV_VARS = splitList(
  Bun.env.PROBE_SECRET_ENV_VARS ?? "APP_TOKEN,SECRET_TOKEN",
);

const PROBE_FILES = splitList(
  Bun.env.PROBE_FILES ?? "/etc/cm/greeting,/etc/secret/token",
);
const PROBE_ENV_VARS = splitList(
  Bun.env.PROBE_ENV_VARS ?? "GREETING,APP_TOKEN",
);

function splitList(v: string): string[] {
  return v
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

function sha256(buf: Buffer | string): string {
  return createHash("sha256").update(buf).digest("hex");
}

// A short, human-eyeball preview. Suppressed entirely for secret-backed items so
// secret material is not splattered across UKC console logs.
function preview(value: string, sensitive: boolean): string {
  if (sensitive) return "<redacted>";
  const oneLine = value.replace(/\s+/g, " ").trim();
  return oneLine.length > 32 ? oneLine.slice(0, 32) + "…" : oneLine;
}

interface FileResult {
  path: string;
  sensitive: boolean;
  exists: boolean;
  size: number;
  sha256: string | null;
  preview: string | null;
  error?: string;
}

interface EnvResult {
  name: string;
  sensitive: boolean;
  present: boolean;
  size: number;
  sha256: string | null;
  preview: string | null;
}

function probeFile(path: string): FileResult {
  const sensitive = SECRET_PATHS.includes(path);
  try {
    const st = statSync(path);
    const raw = readFileSync(path);
    const text = raw.toString("utf8");
    return {
      path,
      sensitive,
      exists: true,
      size: st.size,
      sha256: sha256(raw),
      preview: preview(text, sensitive),
    };
  } catch (err) {
    return {
      path,
      sensitive,
      exists: false,
      size: 0,
      sha256: null,
      preview: null,
      error: err instanceof Error ? err.message : String(err),
    };
  }
}

function probeEnv(name: string): EnvResult {
  const sensitive = SECRET_ENV_VARS.includes(name);
  const value = Bun.env[name];
  if (value === undefined) {
    return {
      name,
      sensitive,
      present: false,
      size: 0,
      sha256: null,
      preview: null,
    };
  }
  return {
    name,
    sensitive,
    present: true,
    size: Buffer.byteLength(value, "utf8"),
    sha256: sha256(value),
    preview: preview(value, sensitive),
  };
}

function buildResult() {
  return {
    files: PROBE_FILES.map(probeFile),
    env: PROBE_ENV_VARS.map(probeEnv),
  };
}

// Primary observation: print the result block to stdout/console once at startup.
// PROBE_BEGIN / PROBE_END bracket the single-line PROBE_RESULT so a verification
// script can slice the block deterministically out of `instance logs`.
const result = buildResult();
console.log("PROBE_BEGIN");
console.log("PROBE_RESULT " + JSON.stringify(result));
console.log("PROBE_END");

// Secondary observation path: serve the same JSON over HTTP for the day an ingress
// exists. Keeps the process alive so the instance stays running.
Bun.serve({
  port: PORT,
  fetch(req) {
    const url = new URL(req.url);
    if (url.pathname === "/healthz") {
      return new Response("ok\n", { status: 200 });
    }
    if (url.pathname === "/probe") {
      return new Response(JSON.stringify(buildResult()), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }
    return new Response("config-secret-probe\n", { status: 200 });
  },
});

console.log(`config-secret-probe listening on :${PORT}`);
