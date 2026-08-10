# Builds the compute workloads portal plugin (Vite + Module Federation) and
# serves the built output as static files. The portal's asset proxy fetches
# plugin-manifest.json, remoteEntry.js, and page chunks directly from here —
# see README.md for why `build`'s output (not `dev`) is what must be served.

FROM oven/bun:1 AS build
WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY . .
RUN bun run build

FROM nginxinc/nginx-unprivileged:1.29-alpine
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080
