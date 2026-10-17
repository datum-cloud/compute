# Dynamically-linked glibc PHP runtime proof.
#
# Like the CPython proof (and unlike the static-PIE Go/Rust proofs), the PHP CLI
# is a dynamically-linked glibc ELF: it needs its dynamic loader
# (/lib64/ld-linux-x86-64.so.2) and a set of glibc shared objects at runtime,
# plus the shared libraries that its bundled extensions (the *.so files in PHP's
# extension_dir) link against (e.g. libssl/libcrypto, libxml2, libz, libargon2,
# libsodium). The bet, already proven for Python: the base:latest app-elfloader
# runs a dynamic ELF when the loader and every needed .so are present in the
# rootfs. So we ship them all -- but only the precise closure.
#
# IMPORTANT -- rootfs size matters: the unikernel extracts its rootfs into an
# in-RAM filesystem at boot. Shipping the WHOLE /usr/lib/x86_64-linux-gnu
# directory overflows that RAM disk and the boot FAILS with:
#   [libukcpio] ...: Failed to load content: No space left on device (28)
#   [libposix_vfs_fstab] Failed to extract CPIO to /: -3
# So we copy ONLY the precise shared-library closure the php binary and its
# bundled extensions actually need, computed at build time by walking ldd.
#
# Stage 1 is the upstream PHP CLI image pinned to an exact patch version. The
# closure is computed by walking ldd over /usr/local/bin/php AND every bundled
# extension *.so in extension_dir, then staging each needed library plus its
# SONAME symlink into /rootfs-libs. Stage 2 is a FROM scratch rootfs holding the
# php binary, the extensions dir (at its exact extension_dir path so PHP finds
# them), the glibc loader, and just that library closure.
FROM php:8.3.14-cli-bookworm AS base

# Build the exact shared-library closure for the php binary + every bundled
# extension .so. Walking ldd over each extension captures libraries the
# extensions link (e.g. libssl/libcrypto via openssl, libxml2 via dom/xml,
# libsodium, libargon2) that a hand-written list would miss. SONAME symlinks are
# preserved so the loader resolves NEEDED entries; real targets are copied
# alongside.
RUN set -eu; \
    extdir="$(php -r 'echo ini_get("extension_dir");')"; \
    echo "extension_dir=$extdir"; \
    mkdir -p /rootfs-libs; \
    { \
      ldd /usr/local/bin/php; \
      for f in "$extdir"/*.so; do [ -e "$f" ] && ldd "$f"; done; \
    } 2>/dev/null \
      | awk '/=>/ {print $3}' \
      | grep -E '^/(usr/)?lib' \
      | sort -u > /tmp/sonames.txt; \
    while read -r p; do \
      [ -n "$p" ] || continue; \
      real="$(readlink -f "$p")"; \
      cp -a "$real" "/rootfs-libs/$(basename "$real")"; \
      if [ "$(basename "$p")" != "$(basename "$real")" ]; then \
        ln -sf "$(basename "$real")" "/rootfs-libs/$(basename "$p")"; \
      fi; \
    done < /tmp/sonames.txt; \
    echo "=== staged library closure ==="; ls -la /rootfs-libs; \
    echo "=== closure size ==="; du -sh /rootfs-libs

# Record the php binary's direct ldd closure + ELF shape in the build log.
RUN echo "=== ldd /usr/local/bin/php ===" && ldd /usr/local/bin/php && \
    echo "=== file php ===" && file /usr/local/bin/php

FROM scratch

# The php CLI binary.
COPY --from=base /usr/local/bin/php /usr/local/bin/php

# The bundled extension .so files. Preserve the EXACT extension_dir path so PHP
# resolves them without an explicit -d extension_dir override. For 8.3 CLI this
# is /usr/local/lib/php/extensions/no-debug-non-zts-<api-no>/.
COPY --from=base /usr/local/lib/php /usr/local/lib/php

# glibc dynamic loader (the program interpreter named in the php ELF header).
COPY --from=base /lib64/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2

# The precise shared-library closure, staged in stage 1. We place it under BOTH
# default loader search paths (/lib/x86_64-linux-gnu and /usr/lib/...) so NEEDED
# SONAMEs resolve without an ld.so.cache. (ld.so.cache is intentionally NOT
# copied: it references libraries we did not ship; the loader falls back to the
# default trusted search paths, where these libraries live.)
COPY --from=base /rootfs-libs/ /lib/x86_64-linux-gnu/
COPY --from=base /rootfs-libs/ /usr/lib/x86_64-linux-gnu/

COPY ./server.php /server.php
