# Dynamically-linked glibc CPython runtime proof (Variant A).
#
# Unlike the Go/Rust proofs -- which ship a single static-PIE binary with no
# interpreter -- CPython is a dynamically-linked glibc ELF (PIE) that needs its
# dynamic loader (/lib64/ld-linux-x86-64.so.2) and a set of glibc shared
# objects at runtime. The bet of this example: the base:latest app-elfloader
# DOES run a dynamic ELF when the loader and every needed .so is present in the
# rootfs. So instead of stripping the dynamic deps, we ship them all.
#
# IMPORTANT -- rootfs size matters: the unikernel extracts its rootfs into an
# in-RAM filesystem at boot. Shipping the WHOLE /usr/lib/x86_64-linux-gnu
# directory (358 MB: ICU .a/.so, tcl/tk, X11, static archives, ...) overflows
# that RAM disk and the boot FAILS with:
#   [libukcpio] ...: Failed to load content: No space left on device (28)
#   [libposix_vfs_fstab] Failed to extract CPIO to /: -3
# So we copy ONLY the precise shared-library closure the interpreter and its
# stdlib C extension modules actually need (~25 MB), computed at build time.
#
# Stage 1 is the upstream CPython image pinned to an exact patch version; the
# copied /usr/local/lib/python3.12 tree must match the interpreter minor
# version. The closure is computed by walking ldd over the interpreter,
# libpython, and every lib-dynload/*.so extension module, then staging each
# needed library plus its SONAME symlink into /rootfs-libs. Stage 2 is a FROM
# scratch rootfs holding the interpreter, the stdlib, the glibc loader, and just
# that library closure.
FROM python:3.12.11-bookworm AS base

# Build the exact shared-library closure for the interpreter + the whole stdlib
# C-extension set. We include EVERY lib-dynload module (not just what server.py
# imports) so the example stays reusable for any stdlib usage, while still
# excluding the unused bulk (static .a archives, ICU data, etc.) that overflows
# the unikernel RAM disk. SONAME symlinks are preserved so the loader resolves
# NEEDED entries; real targets are copied alongside.
RUN set -eu; \
    mkdir -p /rootfs-libs; \
    { \
      ldd /usr/local/bin/python3.12; \
      ldd /usr/local/lib/libpython3.12.so.1.0; \
      for f in /usr/local/lib/python3.12/lib-dynload/*.so; do ldd "$f"; done; \
    } 2>/dev/null \
      | awk '/=>/ {print $3}' \
      | grep -E '^/(usr/)?lib' \
      | sort -u > /tmp/sonames.txt; \
    while read -r p; do \
      [ -n "$p" ] || continue; \
      real="$(readlink -f "$p")"; \
      cp -a "$real" "/rootfs-libs/$(basename "$real")"; \
      sopath="/rootfs-libs/$(basename "$p")"; \
      if [ "$(basename "$p")" != "$(basename "$real")" ]; then \
        ln -sf "$(basename "$real")" "$sopath"; \
      fi; \
    done < /tmp/sonames.txt; \
    echo "=== staged library closure ==="; ls -la /rootfs-libs; \
    echo "=== closure size ==="; du -sh /rootfs-libs

# Record the interpreter's direct ldd closure + ELF shape in the build log.
RUN echo "=== ldd /usr/local/bin/python3.12 ===" && ldd /usr/local/bin/python3.12 && \
    echo "=== file python3.12 ===" && file /usr/local/bin/python3.12

FROM scratch

# Interpreter + stdlib (the python3.12 dir version MUST match libpython below).
COPY --from=base /usr/local/bin/python3.12 /usr/local/bin/python3
COPY --from=base /usr/local/lib/python3.12 /usr/local/lib/python3.12
COPY --from=base /usr/local/lib/libpython3.12.so.1.0 /usr/local/lib/libpython3.12.so.1.0

# glibc dynamic loader (the program interpreter named in the python ELF header).
COPY --from=base /lib64/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2

# The precise shared-library closure, staged in stage 1. We place it under BOTH
# default loader search paths (/lib/x86_64-linux-gnu and /usr/lib/...) so NEEDED
# SONAMEs resolve without an ld.so.cache. (ld.so.cache is intentionally NOT
# copied: it references hundreds of libs we did not ship; the loader falls back
# to the default trusted search paths, where these libs live.)
COPY --from=base /rootfs-libs/ /lib/x86_64-linux-gnu/
COPY --from=base /rootfs-libs/ /usr/lib/x86_64-linux-gnu/

COPY ./server.py /server.py
