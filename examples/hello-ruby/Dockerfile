# Dynamically-linked glibc Ruby (MRI/CRuby) runtime proof (Variant A).
#
# Like the CPython proof -- and unlike the Go/Rust proofs which ship a single
# static-PIE binary with no interpreter -- MRI Ruby is a dynamically-linked
# glibc ELF (PIE) that needs its dynamic loader (/lib64/ld-linux-x86-64.so.2)
# and a set of glibc/system shared objects at runtime. The bet of this example:
# the base:latest app-elfloader DOES run a dynamic ELF when the loader and every
# needed .so is present in the rootfs. So instead of stripping the dynamic deps,
# we ship them all.
#
# IMPORTANT -- rootfs size matters: the unikernel extracts its rootfs into an
# in-RAM filesystem at boot. Shipping the WHOLE /usr/lib/x86_64-linux-gnu
# directory (hundreds of MB) overflows that RAM disk and the boot FAILS with:
#   [libukcpio] ...: Failed to load content: No space left on device (28)
#   [libposix_vfs_fstab] Failed to extract CPIO to /: -3
# So we copy ONLY the precise shared-library closure the interpreter, libruby,
# and the Ruby stdlib C-extension modules (rubyarchdir/**/*.so, incl. enc/*.so)
# actually need (a handful of small libs), computed at build time.
#
# Stage 1 is the upstream Ruby image pinned to an exact patch version; the copied
# /usr/local/lib/ruby tree and libruby.so MUST match that interpreter version.
# The closure is computed by walking ldd over the ruby binary, libruby, and every
# stdlib *.so under rubyarchdir, then staging each needed library plus its SONAME
# symlink into /rootfs-libs. Stage 2 is a FROM scratch rootfs holding the
# interpreter, the stdlib, libruby, the glibc loader, and just that closure.
FROM ruby:3.3.6-bookworm AS base

# Build the exact shared-library closure for the interpreter + libruby + the full
# stdlib C-extension set (rubyarchdir, which includes enc/*.so encoding modules
# Ruby autoloads). We include EVERY archdir *.so (not just what server.rb uses)
# so the example stays reusable for any stdlib usage, while still excluding the
# unused bulk (static .a archives, ICU data, etc.) that overflows the unikernel
# RAM disk. SONAME symlinks are preserved so the loader resolves NEEDED entries;
# real targets are copied alongside.
RUN set -eu; \
    mkdir -p /rootfs-libs; \
    archdir="$(ruby -e 'print RbConfig::CONFIG["rubyarchdir"]')"; \
    { \
      ldd /usr/local/bin/ruby; \
      ldd /usr/local/lib/libruby.so.3.3; \
      for f in $(find "$archdir" -name '*.so'); do ldd "$f"; done; \
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
RUN echo "=== ldd /usr/local/bin/ruby ===" && ldd /usr/local/bin/ruby && \
    echo "=== file ruby ===" && file /usr/local/bin/ruby && \
    echo "=== rubyarchdir ===" && ruby -e 'puts RbConfig::CONFIG["rubyarchdir"]'

FROM scratch

# Interpreter + stdlib + libruby. The /usr/local/lib/ruby tree and libruby
# version MUST match the ruby binary (all pinned via ruby:3.3.6-bookworm).
COPY --from=base /usr/local/bin/ruby /usr/local/bin/ruby
COPY --from=base /usr/local/lib/ruby /usr/local/lib/ruby
COPY --from=base /usr/local/lib/libruby.so.3.3.6 /usr/local/lib/libruby.so.3.3.6
COPY --from=base /usr/local/lib/libruby.so.3.3 /usr/local/lib/libruby.so.3.3
COPY --from=base /usr/local/lib/libruby.so /usr/local/lib/libruby.so

# glibc dynamic loader (the program interpreter named in the ruby ELF header).
# In the base image /lib64/ld-linux-x86-64.so.2 is a symlink into
# /lib/x86_64-linux-gnu; readlink -f resolves it to the real ELF so the rootfs
# has a concrete loader at the path the ELF header expects.
COPY --from=base /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2

# The precise shared-library closure, staged in stage 1. We place it under BOTH
# default loader search paths (/lib/x86_64-linux-gnu and /usr/lib/...) so NEEDED
# SONAMEs resolve without an ld.so.cache. (ld.so.cache is intentionally NOT
# copied: it references hundreds of libs we did not ship; the loader falls back
# to the default trusted search paths, where these libs live.)
COPY --from=base /rootfs-libs/ /lib/x86_64-linux-gnu/
COPY --from=base /rootfs-libs/ /usr/lib/x86_64-linux-gnu/

COPY ./server.rb /server.rb
