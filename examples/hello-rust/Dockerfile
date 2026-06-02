# Multi-stage build for the static-PIE Rust runtime proof.
#
# Stage 1 builds against the x86_64-unknown-linux-musl target, which produces a
# fully static AND position-independent (PIE) ELF by default (Rust >= 1.46). The
# app IS the kraftcloud entrypoint, so there is no separate dynamic interpreter
# for the elfloader's PIE gate to reject at boot -- the exact trap the prior
# non-PIE bun image hit. A self-check FAILS the build unless the binary is an
# ET_DYN (PIE) statically-linked ELF with no program interpreter, so a bad shape
# can never be published.
#
# Stage 2 is a FROM scratch rootfs holding just the binary; `kraft cloud deploy`
# turns this rootfs + the base:latest runtime into a kraftcloud unikernel.
FROM rust:1.83-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends binutils file \
    && rm -rf /var/lib/apt/lists/*
RUN rustup target add x86_64-unknown-linux-musl
WORKDIR /src
COPY Cargo.toml ./
COPY src ./src
RUN cargo build --release --target x86_64-unknown-linux-musl
RUN cp target/x86_64-unknown-linux-musl/release/hello-rust /server

# Static-PIE self-check: fail the build unless the binary is ET_DYN (PIE) AND
# statically linked AND has no INTERP segment. A dynamically-linked binary or one
# with a program interpreter would fail to boot on the elfloader (like bun did).
RUN echo "=== readelf -h /server ===" && readelf -h /server && \
    echo "=== file /server ===" && file /server && \
    echo "=== readelf -l (INTERP check) ===" && readelf -l /server | grep -i interp || true
RUN readelf -h /server | grep -q 'Type:[[:space:]]*DYN' \
        || (echo "FAIL: binary is not ET_DYN (not PIE)"; exit 1)
# musl static-PIE binaries are reported by `file` as "static-pie linked"; a real
# dynamic binary says "dynamically linked". Accept the former, reject the latter.
RUN if file /server | grep -q 'dynamically linked'; then \
        echo "FAIL: binary is dynamically linked"; exit 1; \
    fi
RUN file /server | grep -qE 'static-pie linked|statically linked' \
        || (echo "FAIL: binary is not statically linked / static-pie"; exit 1)
RUN if readelf -l /server | grep -qi 'INTERP'; then \
        echo "FAIL: binary has a program interpreter (INTERP segment)"; exit 1; \
    fi
RUN echo "OK: /server is a static-PIE ELF (ET_DYN, static-pie linked, no INTERP)"

FROM scratch
COPY --from=build /server /server
ENTRYPOINT ["/server"]
