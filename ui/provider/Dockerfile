# Builds the compute provider portal plugin (Vite + Module Federation,
# manifest-only — no real page code, just a `portal.resource/platform`
# extension declaring compute's search target) and serves the built output
# as static files via Node's own https server (serve.ts) — no nginx.
# staff-portal's asset proxy fetches plugin-manifest.json (and the otherwise-unused
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

FROM node:26.13.0-alpine3.24@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216 AS build
WORKDIR /app
# Node 26 ships no corepack, so install the pnpm that package.json's
# packageManager pins; it stays in this stage only.
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN npm install -g "$(node -p 'require("./package.json").packageManager')" --no-audit --no-fund && \
  pnpm install --frozen-lockfile --ignore-scripts
COPY . .
RUN pnpm build

FROM node:26.13.0-alpine3.24@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216
# Node keeps its own copy of the CA roots apart from Alpine's bundle. Fail the
# build if either still trusts the compromised eMudhra root (emSign ECC Root
# CA - G3).
RUN apk add --no-cache --upgrade 'ca-certificates>=20261020-r0'
RUN ! grep -q MIICTjCCAdOgAwIBAgIKPPYHqWhwDtqLhDAKBggqhkjOPQQDAzBrMQswCQYDVQQG /etc/ssl/certs/ca-certificates.crt
RUN node -e 'const { rootCertificates } = require("node:tls"); const { X509Certificate } = require("node:crypto"); const compromised = rootCertificates.some((pem) => new X509Certificate(pem).subject.split("\n").includes("CN=emSign ECC Root CA - G3")); if (compromised) { console.error("refusing to build: Node trusts emSign ECC Root CA - G3"); process.exit(1); }'
WORKDIR /app
COPY --from=build /app/dist ./dist
COPY serve.ts ./
# uid/gid 1000, as the Deployment's securityContext expects.
USER 1000:1000
EXPOSE 8443
CMD ["node", "serve.ts"]
