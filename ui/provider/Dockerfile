# Builds the compute provider portal plugin (Vite + Module Federation,
# manifest-only — no real page code, just a `portal.resource/platform`
# extension declaring compute's search target) and serves the built output
# as static files via Bun's own server (serve.ts) — no nginx. staff-portal's
# asset proxy fetches plugin-manifest.json (and the otherwise-unused
# remoteEntry.js the build still produces) directly from here.
#
# Internal-only: no Gateway/HTTPRoute fronts this service. staff-portal's
# server is the only caller, over the in-cluster Service DNS name — the
# browser never talks to a plugin origin directly. TLS is still required
# because ServiceConfiguration.spec.userInterface.provider.assets.baseURL
# requires an https:// URL; the cert comes from a cert-manager CSI volume
# mounted at /etc/tls by the Deployment (see
# config/components/provider-ui-plugin/deployment.yaml) — the same mount
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
