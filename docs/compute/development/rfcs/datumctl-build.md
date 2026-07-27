---
status: proposed
---

# `datumctl build` — Dockerfile to bootable unikernel OCI image

> Tracks [datum-cloud/compute#174](https://github.com/datum-cloud/compute/issues/174).

## Table of Contents

- [Summary](#summary)
- [Problem](#problem)
- [Design](#design)
  - [Input resolution](#input-resolution)
  - [Dockerfile path](#dockerfile-path)
  - [Lightweight binary analysis](#lightweight-binary-analysis)
  - [Kraftfile generation](#kraftfile-generation)
  - [Packaging and push](#packaging-and-push)
- [Alternatives](#alternatives)
- [Failure modes](#failure-modes)
- [What gets built](#what-gets-built)
- [Decisions](#decisions)
- [Open questions](#open-questions)

---

## Summary

Datum compute runs on Unikraft (`kraftlet`) and requires unikernel OCI images. Standard `docker build` output does not boot. Building a unikernel image today requires the Unikraft toolchain, a hand-authored Kraftfile, and a multi-step `kraft pkg` / relabel / push workflow that is entirely outside `datumctl`. This RFC proposes `datumctl build`: a command that takes a Dockerfile (or an optional Kraftfile for advanced cases) and produces a bootable unikernel OCI image pushed to any OCI-compatible registry.

## Problem

The full current workflow is captured in the [unikraft-provider onboarding PR](https://github.com/datum-cloud/unikraft-provider/pull/75): a Dockerfile, a hand-authored Kraftfile, and a chain of `kraft` and `crane` commands. Two failure modes are common and both present after boot, not at build time:

- **Non-PIE entrypoint.** Unikraft's loader requires a static-PIE binary. The default `go build` output — and `go build -buildmode=pie` without `-ldflags='-extldflags=-static'` — records an `INTERP` ELF section pointing to a dynamic linker that does not exist in the unikernel environment. The VM dies ~1.6 seconds after boot.
- **Missing shared libraries.** Unikraft includes only what is explicitly packaged in the initrd. Binaries that depend on shared libraries need those libraries copied into the final Dockerfile stage. Users building unikernel images discover this empirically — and the fix, once understood, is always the same pattern:

  ```dockerfile
  COPY --from=build /app /app
  COPY --from=build /lib/x86_64-linux-gnu/libc.so.6     /lib/x86_64-linux-gnu/libc.so.6
  COPY --from=build /lib/x86_64-linux-gnu/libm.so.6     /lib/x86_64-linux-gnu/libm.so.6
  COPY --from=build /lib64/ld-linux-x86-64.so.2         /lib64/ld-linux-x86-64.so.2
  ```

  ([mariadb example](https://github.com/unikraft-cloud/examples/tree/main/mariadb), and most other Unikraft examples.) Getting this right today means reading ELF headers manually or iterating through boot failures.

Both failures are detectable at build time by reading the entrypoint binary's ELF headers. `datumctl build` makes that analysis automatic.

## Design

### Input resolution

`datumctl build` is invoked from a directory containing either a `Dockerfile` or a `Kraftfile`. If a `Kraftfile` is present it is used directly (escape hatch). Otherwise a `Dockerfile` is required and a Kraftfile is generated ephemerally.

### Dockerfile path

When no `Kraftfile` is present:

1. Build the Dockerfile using the local BuildKit daemon, producing a standard rootfs.
2. Extract the final layer and run lightweight binary analysis on the entrypoint (see below).
3. Generate an ephemeral Kraftfile for the build (see below).
4. Package the rootfs into a unikernel OCI image using the kraftkit `initrd` + `oci` packagers and a Unikraft base runtime.
5. Push to the registry specified by `--push`, relabeling the OCI index with the `kraftcloud/x86_64` platform entry required by `kraftlet`.

### Lightweight binary analysis

After BuildKit exports the rootfs tarball, `datumctl build` extracts the entrypoint binary path from the OCI image config and reads its ELF headers directly from the tarball using Go's `debug/elf` package. No subprocess or external tooling is involved. Two checks run:

**PIE check.** Read the ELF file header's `Type` field and program headers. A static-PIE binary has type `ET_DYN` and no `PT_INTERP` program header. If `PT_INTERP` is present or the type is `ET_EXEC`, the build fails. On failure we attempt to identify the toolchain from binary metadata (Go embeds build info readable via `debug/buildinfo`; other toolchains may be detectable from ELF notes or `.comment` sections), locate the corresponding `RUN` step in the Dockerfile, and suggest a corrected invocation. If toolchain detection or Dockerfile tracing fails — the compiler may be invoked through a script or Makefile — we fall back to generic per-runtime guidance.

**Shared library check.** Read `DT_NEEDED` entries from the binary's `.dynamic` ELF section and cross-reference them against the final stage rootfs. If any are missing, the build fails. To locate each missing library we re-solve each named Dockerfile stage via `FrontendAttrs["target"]` — these are cache hits after the initial build and return near-instantly — then search the exported filesystem for the library. If found, we emit the exact `COPY --from=<stage> <src> <dst>` line. If a library cannot be located in any stage, we fall back to reporting the library name and its conventional destination path. This is the same `COPY` pattern seen throughout [Unikraft's own examples](https://github.com/unikraft-cloud/examples).

Both checks are deliberately narrow. Libraries opened at runtime via `dlopen` are not listed in `DT_NEEDED` and will not be caught. Syscall compatibility, port bindings, and other runtime constraints are out of scope entirely. Users should expect that a build that passes these checks may still require iteration to behave correctly under Unikraft — the goal is to eliminate the two most common silent boot failures, not to certify the image.

### Kraftfile generation

The Kraftfile format is largely uniform across Unikraft builds — entrypoint path, architecture, and the Unikraft base runtime version are the only fields that vary between most applications. `datumctl build` generates this file ephemerally from the Dockerfile's `ENTRYPOINT`/`CMD` and the target architecture.

`datumctl build write-kraftfile` writes the generated Kraftfile to disk for inspection or customization. Once committed, subsequent `datumctl build` invocations use it via the Kraftfile path. Users who hit build configurations the automatic path cannot handle (custom initrd, non-standard base, specific Unikraft library flags) use the Kraftfile escape hatch from that point forward.

### Packaging and push

Packaging uses the kraftkit Go libraries (`kraftkit.sh/initrd`, `kraftkit.sh/oci`) embedded directly as dependencies rather than shelling out to the `kraft` CLI. This avoids a runtime toolchain dependency — users do not need `kraft` installed — and gives `datumctl build` stable control over the output format.

Push uses Docker credential helpers already present in `~/.docker/config.json`. No Datum-specific registry is assumed or required. The command is non-interactive and usable in CI.

## Alternatives

**Shell out to the `kraft` CLI.** Simpler initial implementation, but creates a hard runtime dependency on an external binary, introduces version drift across user environments, and does not meaningfully reduce the toolchain burden for users who do not already have `kraft` installed. Rejected in favor of embedded libraries.

**Shell out to `unikraft-cloud/cli`.** Unikraft's newer CLI ([unikraft-cloud/cli](https://github.com/unikraft-cloud/cli)) implements a similar Dockerfile-to-unikernel pipeline, but its build logic lives entirely in `internal/` — not usable as a library. Shelling out to it has the same version-drift and external-dependency problems as shelling out to `kraft`.

**Drive BuildKit directly without kraftkit.** Assemble the OCI index by hand, pulling Unikraft base layers and constructing the manifest with the correct platform annotation. Gives maximum control but requires reimplementing what kraftkit already does and means we must track Unikraft's OCI format changes manually. Rejected.

## Failure modes

**Non-PIE binary.** Detected at analysis time; build fails with the corrected compiler invocation before packaging runs.

**Missing shared libraries.** Detected at analysis time; build fails with the exact `COPY` lines needed. The check is limited to statically-listed `DT_NEEDED` entries — libraries resolved at runtime via `dlopen` are not detected.

**BuildKit not running.** Detected before build; actionable message.

**Registry push failure.** Registry error is passed through verbatim.

## What gets built

- `datumctl build [--push <image>]`: Dockerfile or Kraftfile → unikernel OCI image → push
- `datumctl build write-kraftfile`: write the generated Kraftfile to disk
- Lightweight ELF analysis with actionable error messages (PIE check + DT_NEEDED)
- Embedded kraftkit Go library dependency; no `kraft` CLI required at runtime
- CI-compatible (non-interactive)

Out of scope: remote build service, multi-architecture, base-runtime credential management.

## Decisions

- **Build engine**: embed kraftkit Go libraries (`initrd`, `oci` packagers); do not shell out to `kraft`.
- **Kraftfile**: generated ephemerally from Dockerfile metadata; mostly uniform across builds; escape hatch for non-standard configurations.
- **Analysis**: lightweight ELF header inspection only; explicitly not a complete ABI validator; two checks (PIE, DT_NEEDED) targeting the two most common boot-time failure modes.
- **Registry**: any OCI-compatible registry via Docker credential config; no Datum registry assumed.

## Open questions

1. **Enterprise base runtime access.** Images built against the community base boot but instances stay `Pending` indefinitely; the enterprise base is required for a healthy deployment. Access to `unikraft.io/base` (enterprise) is currently access-controlled on Unikraft's registry. How users acquire that access — and whether `datumctl build` needs to do anything about it beyond failing with a clear message — is a commercial and access-control question not resolved here.

2. **Unikraft CLI transition.** Unikraft has shipped a new CLI ([unikraft-cloud/cli](https://github.com/unikraft-cloud/cli)) alongside the existing `kraftkit`. Its build pipeline is `internal/` so it cannot be used as a library, but its existence raises the question of whether `kraftkit` is on a deprecation path. We should discuss the intended long-term split with the Unikraft team before committing to the kraftkit library dependency.
