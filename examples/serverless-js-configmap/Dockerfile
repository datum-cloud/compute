# Multi-stage build for the GENERIC serverless Node.js runtime image.
#
# This is the key difference from examples/hello-node: NO application/JS code is
# baked in. The final scratch stage carries ONLY the Node runtime -- the `node`
# interpreter plus every shared library it links against. The function itself
# (index.js) is supplied at deploy time via a ConfigMap mounted at /app, so one
# image serves any number of functions and code swaps need no rebuild.
#
# Node is NOT a static-PIE binary -- it is a dynamic musl ELF that needs the
# musl loader and a handful of shared libraries at boot. So this rootfs ships
# the node interpreter AND every .so it links, and the unikernel runs on the
# base-compat:latest runtime (the binary-compatibility / dynamic-loader
# elfloader variant), NOT base:latest. A scratch image missing those libs won't
# boot (library-not-found).
FROM node:22-alpine AS build
# Record node's dynamic-library requirements in the build log for auditing.
RUN echo "=== ldd /usr/local/bin/node ===" && ldd /usr/local/bin/node || true

FROM scratch
# The node interpreter -- the entire "function runtime". No app code here.
COPY --from=build /usr/local/bin/node /usr/bin/node
# musl dynamic loader + libc (ld-musl is both loader and libc on alpine).
COPY --from=build /lib/ld-musl-x86_64.so.1 /lib/ld-musl-x86_64.so.1
# C++/GCC runtime libraries node links against.
COPY --from=build /usr/lib/libgcc_s.so.1   /usr/lib/libgcc_s.so.1
COPY --from=build /usr/lib/libstdc++.so.6  /usr/lib/libstdc++.so.6
# os-release lets node/libc identify the platform cleanly.
COPY --from=build /etc/os-release          /etc/os-release
