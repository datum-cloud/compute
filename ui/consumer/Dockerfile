# Builds the compute workloads portal plugin (Vite + Module Federation) and
# serves the built output as static files via Node's own https server
# (serve.ts) — no nginx. The portal's asset proxy fetches plugin-manifest.json,
# remoteEntry.js, and page chunks directly from here — see README.md for why
# `build`'s output (not `dev`) is what must be served.
#
# Internal-only: no Gateway/HTTPRoute fronts this service. cloud-portal's
# server is the only caller, over the in-cluster Service DNS name — the
# browser never talks to a plugin origin directly. TLS is still required
# because ServiceConfiguration.spec.userInterface.consumer.assets.baseURL
# requires an https:// URL; the cert comes from a cert-manager CSI volume
# mounted at /etc/tls by the Deployment (see
# config/components/consumer-ui-plugin/deployment.yaml) — the same mount
# path serve.ts reads from.
#
# The build context is the repository root so the shell's wasm client can be
# built from the Go sources with `make build-console-wasm`. A tree without that
# target yields a plugin without the client, which hides the Shell tab.
#
# Dockerfile.dockerignore admits only the files that build needs plus this
# plugin, and the Go sources are bind-mounted rather than copied, so no source
# file lands in a cached layer.

FROM --platform=$BUILDPLATFORM golang:1.26 AS console-wasm
WORKDIR /workspace
COPY go.mod go.sum Makefile ./
RUN go mod download
RUN --mount=type=bind,target=/src \
  --mount=type=cache,target=/root/.cache/go-build \
  mkdir -p /out && cd /src && \
  if grep -q '^build-console-wasm:' Makefile; then \
    make build-console-wasm \
      CONSOLE_WASM=/out/console-session.wasm \
      CONSOLE_WASM_EXEC=/out/console-session-wasm_exec.js; \
  fi

FROM node:26.13.0-alpine3.24@sha256:c6ad9e9c1d80f6eb607c26b4b0a132ff7a2344fad1c78ea3baa2a7d4c8de6d94 AS build
WORKDIR /app
COPY ui/consumer/package.json ui/consumer/package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY ui/consumer/ .
COPY --from=console-wasm /out/ /console-wasm/
ENV CONSOLE_WASM_DIR=/console-wasm
RUN npm run build

FROM node:26.13.0-alpine3.24@sha256:c6ad9e9c1d80f6eb607c26b4b0a132ff7a2344fad1c78ea3baa2a7d4c8de6d94
# Node keeps its own copy of the CA roots apart from Alpine's bundle. Fail the
# build if either still trusts the compromised eMudhra root (emSign ECC Root
# CA - G3), which Bun's built-in roots kept until 1.4.4 with no way to drop it.
RUN apk add --no-cache --upgrade 'ca-certificates>=20261020-r0'
RUN ! grep -q MIICTjCCAdOgAwIBAgIKPPYHqWhwDtqLhDAKBggqhkjOPQQDAzBrMQswCQYDVQQG /etc/ssl/certs/ca-certificates.crt
RUN node -e 'const { rootCertificates } = require("node:tls"); const { X509Certificate } = require("node:crypto"); const compromised = rootCertificates.some((pem) => new X509Certificate(pem).subject.split("\n").includes("CN=emSign ECC Root CA - G3")); if (compromised) { console.error("refusing to build: Node trusts emSign ECC Root CA - G3"); process.exit(1); }'
WORKDIR /app
COPY --from=build /app/dist ./dist
COPY ui/consumer/serve.ts ./
# uid/gid 1000, as the Bun image's bun user was and the Deployment's
# securityContext expects.
USER 1000:1000
EXPOSE 8443
CMD ["node", "serve.ts"]
