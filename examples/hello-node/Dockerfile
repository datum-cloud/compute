# Multi-stage build for the Node.js runtime proof.
#
# Unlike the Go/Rust proofs, Node is NOT a static-PIE binary -- it is a dynamic
# musl ELF that needs the musl loader and a handful of shared libraries at boot.
# So this rootfs ships the `node` interpreter AND every shared library it links
# against, and the unikernel runs on the base-compat:latest runtime (the
# binary-compatibility / dynamic-loader elfloader variant), NOT base:latest.
#
# Stage 1 (node:22-alpine) installs deps and prints `ldd node` so the build log
# records exactly which shared objects the rootfs must carry. The scratch image
# below has no package manager, so EVERY library node links must be copied
# explicitly; a missing .so fails the instance at boot with library-not-found.
FROM node:22-alpine AS build
WORKDIR /usr/src
COPY package*.json ./
RUN npm install
# npm install with zero deps creates no node_modules dir; ensure it exists so the
# COPY below succeeds and adding deps later "just works" with no Dockerfile change.
RUN mkdir -p node_modules
COPY app.js ./
# Record node's dynamic-library requirements in the build log for auditing.
RUN echo "=== ldd /usr/local/bin/node ===" && ldd /usr/local/bin/node || true

FROM scratch
# The node interpreter.
COPY --from=build /usr/local/bin/node /usr/bin/node
# musl dynamic loader + libc (ld-musl is both loader and libc on alpine).
COPY --from=build /lib/ld-musl-x86_64.so.1 /lib/ld-musl-x86_64.so.1
# C++/GCC runtime libraries node links against.
COPY --from=build /usr/lib/libgcc_s.so.1   /usr/lib/libgcc_s.so.1
COPY --from=build /usr/lib/libstdc++.so.6  /usr/lib/libstdc++.so.6
# os-release lets node/libc identify the platform cleanly.
COPY --from=build /etc/os-release          /etc/os-release
# Application + (empty for the hello case) dependency tree.
COPY --from=build /usr/src/node_modules /usr/src/node_modules
COPY --from=build /usr/src/app.js       /usr/src/server.js
