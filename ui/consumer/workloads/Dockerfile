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

FROM oven/bun:1 AS build
WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY . .
RUN bun run build

FROM oven/bun:1-slim
WORKDIR /app
COPY --from=build /app/dist ./dist
COPY serve.ts ./
USER bun
EXPOSE 8443
CMD ["bun", "run", "serve.ts"]
