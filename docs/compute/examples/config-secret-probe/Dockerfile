# Dockerfile for the Datum Cloud ConfigMap/Secret mount-verification probe.
#
# Sandbox (OCI container) image that runs under the unikraft runtime on the lab
# (city code DFW). It boots `probe.ts` under Bun, which prints a single
# `PROBE_RESULT {json}` line describing the mounted files/env it observes, then
# stays alive serving the same JSON at GET /probe.
#
# The current unikraft-provider no longer hardcodes an entrypoint
# (`buildPodSpecFromContainers` sets no Command/Args), so UKC honors this image's
# own OCI ENTRYPOINT. We still base on oven/bun and place the app at the
# conventional /usr/src path + symlink bun to /usr/bin/bun, matching the proven
# unikraft-sandbox example, so the image also runs on older provider builds.
#
# Build for the architecture UKC expects (linux/amd64).
FROM oven/bun:1.1-slim

RUN ln -sf "$(command -v bun)" /usr/bin/bun

WORKDIR /usr/src
COPY probe.ts /usr/src/probe.ts

# Defaults; overridden by the Workload's container env for the e2e.
ENV PROBE_FILES=/etc/cm/greeting,/etc/secret/token
ENV PROBE_ENV_VARS=GREETING,APP_TOKEN
ENV PORT=3000
EXPOSE 3000

ENTRYPOINT ["/usr/bin/bun", "run", "/usr/src/probe.ts"]
