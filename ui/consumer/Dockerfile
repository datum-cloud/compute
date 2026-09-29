# Builds the compute workloads portal plugin (Vite + Module Federation) and
# serves the built output as static files via Bun's own server (serve.ts) —
# no nginx. The portal's asset proxy fetches plugin-manifest.json,
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

FROM --platform=$BUILDPLATFORM golang:1.26 AS console-wasm
WORKDIR /workspace
COPY . .
RUN mkdir -p bin && \
  if grep -q '^build-console-wasm:' Makefile; then make build-console-wasm; fi

FROM oven/bun:1 AS build
WORKDIR /app
COPY ui/consumer/package.json ui/consumer/bun.lock ./
RUN bun install --frozen-lockfile
COPY ui/consumer/ .
COPY --from=console-wasm /workspace/bin/ /console-wasm/
ENV CONSOLE_WASM_DIR=/console-wasm
RUN bun run build

FROM oven/bun:1-slim
WORKDIR /app
COPY --from=build /app/dist ./dist
COPY ui/consumer/serve.ts ./
USER bun
EXPOSE 8443
CMD ["bun", "run", "serve.ts"]
