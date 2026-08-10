# Builds the compute workloads portal plugin (Vite + Module Federation) and
# serves the built output as static files. The portal's asset proxy fetches
# plugin-manifest.json, remoteEntry.js, and page chunks directly from here —
# see README.md for why `build`'s output (not `dev`) is what must be served.
#
# Internal-only: no Gateway/HTTPRoute fronts this service. cloud-portal's
# server is the only caller, over the in-cluster Service DNS name — the
# browser never talks to a plugin origin directly. TLS is still required
# because ServiceConfiguration.spec.userInterface.consumer.assets.baseURL
# requires an https:// URL; the cert comes from a cert-manager CSI volume
# mounted at /etc/nginx/tls by the Deployment (see
# config/components/consumer-ui-plugin/deployment.yaml).

FROM oven/bun:1 AS build
WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY . .
RUN bun run build

FROM nginxinc/nginx-unprivileged:1.29-alpine
COPY --from=build /app/dist /usr/share/nginx/html
COPY default.conf /etc/nginx/conf.d/default.conf
EXPOSE 8443
