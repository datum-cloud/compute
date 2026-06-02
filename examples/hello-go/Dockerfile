# Multi-stage build for the static-PIE Go runtime.
#
# The Unikraft Cloud base:latest runtime is an app-elfloader that (a) REQUIRES a
# position-independent executable and (b) has NO dynamic loader, so the app ELF
# must be a static-PIE with no program interpreter. Stock `go build` cannot
# produce that for a pure-Go binary: -buildmode=pie always emits an INTERP
# segment requesting /lib64/ld-linux-x86-64.so.2, and a plain static build is
# non-PIE (ET_EXEC) -- both are rejected at boot with
# "ELF executable is not position-independent! ... Exec format error (-8)".
#
# The fix mirrors the Rust path: link statically against musl, which yields a
# static-PIE (ET_DYN, statically linked, no INTERP). We cross-link with the musl
# x86_64 toolchain via CGO so `go build` emits a self-contained app ELF.
#
# --platform=$BUILDPLATFORM keeps the Go toolchain native to the builder and
# lets Go cross-compile to amd64, avoiding the qemu-emulated amd64 toolchain that
# segfaults the assembler. The musl cross-compiler is installed for the external
# static-PIE link step.
#
# A self-check FAILS the build unless /server is a static-PIE with no INTERP, so
# a bad shape can never be published.
FROM --platform=$BUILDPLATFORM golang:1.24 AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
        binutils file curl xz-utils ca-certificates \
    && rm -rf /var/lib/apt/lists/*
# musl.cc prebuilt cross toolchain: x86_64 musl gcc that runs on the (arm64)
# builder and emits x86_64 ELFs, so no qemu emulation of the compiler.
RUN curl -fsSL https://musl.cc/x86_64-linux-musl-cross.tgz -o /tmp/musl.tgz \
    && tar -xzf /tmp/musl.tgz -C /opt && rm /tmp/musl.tgz
ENV PATH="/opt/x86_64-linux-musl-cross/bin:${PATH}"
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC=x86_64-linux-musl-gcc \
    go build -buildmode=pie \
        -ldflags='-s -w -linkmode=external -extldflags "-static-pie"' \
        -o /server ./main.go

# Static-PIE self-check: fail the build unless the binary is ET_DYN (PIE) AND
# statically linked AND has no INTERP segment.
RUN echo "=== readelf -h /server ===" && readelf -h /server && \
    echo "=== file /server ===" && file /server && \
    echo "=== readelf -l (INTERP check) ===" && (readelf -l /server | grep -i interp || echo "no INTERP segment")
RUN readelf -h /server | grep -q 'Type:[[:space:]]*DYN' \
        || (echo "FAIL: binary is not ET_DYN (not PIE)"; exit 1)
RUN if readelf -l /server | grep -qi 'INTERP'; then \
        echo "FAIL: binary has a program interpreter (INTERP segment)"; exit 1; \
    fi
RUN if file /server | grep -q 'dynamically linked'; then \
        echo "FAIL: binary is dynamically linked"; exit 1; \
    fi
RUN echo "OK: /server is a static-PIE ELF (ET_DYN, no INTERP)"

# Stage 2: a minimal rootfs containing only the binary.
FROM scratch
COPY --from=build /server /server
ENTRYPOINT ["/server"]
