package build

import (
	"debug/elf"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

const checkIDStartupScript = "startup-script"

// defaultChecks are run when no checks are specified.
var defaultChecks = []checkDefinition{
	{
		ID:          "shell-form",
		Description: "startup shell",
		Run:         checkShellForm,
	},
	{
		ID:          checkIDStartupScript,
		Description: "startup script",
		Run:         checkStartupScript,
	},
	{
		ID:          "arch",
		Description: "architecture",
		Run:         checkELFArchitecture,
	},
	{
		ID:          "missing-libs",
		Description: "runtime libraries",
		Run:         checkMissingLibraries,
	},
	{
		ID:          "nss-modules",
		Description: "NSS modules",
		Run:         checkNSSModules,
	},
	{
		ID:          "entrypoint-executable",
		Description: "entrypoint permissions",
		Run:         checkEntrypointExecutable,
	},
}

func formatBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	units := []string{"KiB", "MiB", "GiB"}
	value := float64(size)
	unit := "B"
	for _, u := range units {
		value /= 1024
		unit = u
		if math.Abs(value) < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

func checkELFArchitecture(ctx *checkContext) []finding {
	if ctx.ELF == nil || ctx.ELF.Machine == elf.EM_X86_64 {
		return nil
	}
	return []finding{{
		check:   "arch",
		Message: fmt.Sprintf("%s is built for %s, but Datum Compute currently expects x86_64", ctx.entrypoint(), ctx.ELF.Machine),
		Help:    "Build the final image for linux/amd64, or compile the entrypoint with GOARCH=amd64 / an x86_64 target toolchain.",
	}}
}

func formatMB(size int64) string {
	return fmt.Sprintf("%d MB", (size+500_000)/1_000_000)
}

// checkShellForm verifies that any shell needed to dispatch a shell-form
// startup command (e.g. "CMD npm start" compiles to ["/bin/sh","-c","npm
// start"]) is present in the image. The shell itself is only a dispatcher —
// checkMissingLibraries/checkELFArchitecture check whatever it execs — so
// nothing else in this package verifies it exists.
func checkShellForm(ctx *checkContext) []finding {
	var findings []finding
	for _, shell := range ctx.RequiredShells {
		if ctx.rootfs.HasPath(shell) {
			continue
		}
		findings = append(findings, finding{
			check:   "shell-form",
			Message: fmt.Sprintf("startup command requires %s to run, but it is not present in the image", shell),
			Help:    fmt.Sprintf("Copy %s into the final image, or use exec-form CMD/ENTRYPOINT to avoid needing a shell (e.g. CMD [\"npm\", \"start\"]).", shell),
		})
	}
	return findings
}

func checkStartupScript(ctx *checkContext) []finding {
	if ctx.toolchain.Kind != toolchainShell || len(ctx.BinaryData) == 0 {
		return nil
	}
	line, _, _ := strings.Cut(string(ctx.BinaryData), "\n")
	interp := []string{"/bin/sh"} //nolint:goconst // other occurrences are unrelated test literals
	if !strings.HasPrefix(line, "#!") {
		// Match common shell behavior: scripts without shebang are run by /bin/sh.
	} else if interp = strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "#!"))); len(interp) == 0 {
		return []finding{{
			check:   checkIDStartupScript,
			Message: fmt.Sprintf("startup script %s has an empty shebang", ctx.entrypoint()),
			Help:    "Use a shebang such as #!/bin/sh, or make sure /bin/sh exists in the final image.",
		}}
	}
	// #!/usr/bin/env bash: the kernel execs env verbatim, but env itself resolves
	// its target (bash) via PATH, so the two need to be checked separately.
	if isEnvDispatchPath(interp[0]) && !ctx.rootfs.HasPath(interp[0]) {
		return []finding{missingInterpreterFinding(ctx.entrypoint(), interp[0])}
	}

	target, _ := firstExecutableWord(interp)
	if target == "" {
		return []finding{{
			check:   checkIDStartupScript,
			Message: fmt.Sprintf("startup script %s has a shebang that names no interpreter", ctx.entrypoint()),
			Help:    "Use a shebang such as #!/bin/sh, or make sure /bin/sh exists in the final image.",
		}}
	}
	if resolveEntrypointPathWithDirs(target, ctx.rootfs.Paths, envPathDirs(ctx.Env)) != "" {
		return nil
	}
	return []finding{missingInterpreterFinding(ctx.entrypoint(), target)}
}

func missingInterpreterFinding(entrypoint, interpreter string) finding {
	return finding{
		check:   checkIDStartupScript,
		Message: fmt.Sprintf("startup script %s uses interpreter %s, but it is not present in the image", entrypoint, interpreter),
		Help:    fmt.Sprintf("Copy %s into the final image, or use a script interpreter that is already present.", interpreter),
	}
}

// checkMissingLibraries verifies that every runtime file listed by the ELF
// entrypoint is present in the image filesystem. This includes the dynamic
// loader from PT_INTERP and shared libraries from DT_NEEDED.
func checkMissingLibraries(ctx *checkContext) []finding {
	if ctx.ELF == nil {
		return nil
	}

	var missing []string
	var unresolved []string

	interp := elfInterp(ctx.ELF)
	interpMissing := interp != "" && !ctx.rootfs.HasPath(interp)

	libDirs := append(envLibraryDirs(ctx.Env), elfLibraryDirs(ctx.ELF, ctx.entrypoint())...)
	libs, err := ctx.ELF.ImportedLibraries()
	if err == nil {
		for _, lib := range libs {
			if !hasLibrary(lib, ctx.rootfs.Paths, libDirs) {
				unresolved = append(unresolved, lib)
			}
		}
	}
	if (interpMissing || len(unresolved) > 0) && ctx.RuntimeFileView != nil {
		view, err := ctx.RuntimeFileView()
		if err != nil {
			ctx.Notes = append(ctx.Notes, fmt.Sprintf("could not inspect runtime files for dependency fixes: %v", err))
		} else if view != nil {
			// The loader path is exact (unlike DT_NEEDED libs, found by
			// basename search) — only auto-fix once it's confirmed to exist
			// in the stage we'd copy it from.
			if interpMissing && hasExactPath(interp, view.Paths) {
				missing = append(missing, interp)
				interpMissing = false
			}
			remaining := unresolved[:0]
			for _, lib := range unresolved {
				if path := findLibraryPath(lib, view.Paths, libDirs); path != "" {
					missing = append(missing, "/"+path)
				} else {
					remaining = append(remaining, lib)
				}
			}
			unresolved = remaining
		}
	}
	if interpMissing {
		unresolved = append([]string{interp}, unresolved...)
	}
	if len(missing) == 0 && len(unresolved) == 0 {
		return nil
	}

	entrypointCopy, sourceStage := ctx.entrypointProvenance()
	f := finding{
		check:   "missing-libs",
		Message: missingRuntimeMessage(ctx.entrypoint(), len(missing)+len(unresolved)),
		Help:    missingRuntimeHelp(missing, unresolved, sourceStage),
	}
	if len(missing) > 0 && len(unresolved) == 0 && ctx.Dockerfile != nil && entrypointCopy != nil {
		f.File = ctx.Dockerfile.path
		f.Line = entrypointCopy.StartLine
		raw := ctx.Dockerfile.rawInstruction(entrypointCopy.StartLine)
		f.Edits = []sourceEdit{{
			File:        f.File,
			Line:        f.Line,
			Old:         raw,
			New:         raw + "\n" + missingRuntimeFilesFix(missing, sourceStage),
			Description: "copy missing runtime files into the final image",
		}}
	}
	return []finding{f}
}

// nssDNSSymbols and nssUserSymbols are libc functions that dlopen a Name
// Service Switch module at runtime, based on /etc/nsswitch.conf, rather than
// linking it as a DT_NEEDED dependency — invisible to checkMissingLibraries.
//
//nolint:goconst // other occurrences are tests
var (
	nssDNSSymbols  = []string{"getaddrinfo", "gethostbyname", "gethostbyname2", "gethostbyname_r", "gethostbyname2_r", "getnameinfo"}
	nssUserSymbols = []string{"getpwnam", "getpwnam_r", "getpwuid", "getpwuid_r", "getgrnam", "getgrnam_r", "getgrgid", "getgrgid_r"}
)

// checkNSSModules verifies that glibc's NSS modules are present when the
// entrypoint imports a libc function that dlopens one at runtime: hostname
// resolution (libnss_dns.so.2) or user/group lookups (libnss_files.so.2). A
// binary can pass checkMissingLibraries cleanly and still silently fail
// every DNS lookup at runtime, since these are never DT_NEEDED entries.
// musl (Alpine) is unaffected — its resolver has no NSS plugin architecture.
func checkNSSModules(ctx *checkContext) []finding {
	if ctx.ELF == nil || !strings.Contains(elfInterp(ctx.ELF), "ld-linux") {
		return nil
	}
	syms, err := ctx.ELF.ImportedSymbols()
	if err != nil {
		return nil
	}
	names := make(map[string]struct{}, len(syms))
	for _, s := range syms {
		names[s.Name] = struct{}{}
	}
	entrypointCopy, sourceStage := ctx.entrypointProvenance()
	return nssModuleFindings(nssModuleInputs{
		Entrypoint:      ctx.entrypoint(),
		ImportedSymbols: names,
		FilePaths:       ctx.rootfs.Paths,
		LibDirs:         append(envLibraryDirs(ctx.Env), elfLibraryDirs(ctx.ELF, ctx.entrypoint())...),
		Dockerfile:      ctx.Dockerfile,
		EntrypointCopy:  entrypointCopy,
		SourceStage:     sourceStage,
	})
}

// nssModuleInputs is what nssModuleFindings needs, decoupled from
// checkContext so the policy logic can be tested with a fake symbol map
// instead of a real glibc-linked binary. Dockerfile/EntrypointCopy/
// SourceStage may be left zero — no exact-line fix is generated then.
type nssModuleInputs struct {
	Entrypoint      string
	ImportedSymbols map[string]struct{}
	FilePaths       map[string]struct{}
	LibDirs         []string
	Dockerfile      *dockerfileDoc
	EntrypointCopy  *parser.Node
	SourceStage     string
}

// nssModuleFindings is the policy half of checkNSSModules.
func nssModuleFindings(in nssModuleInputs) []finding {
	// If libc itself is missing, checkMissingLibraries already reports it;
	// piling on here would just be a confusing second error about the same cause.
	libcPath := findLibraryPath("libc.so.6", in.FilePaths, in.LibDirs)
	if libcPath == "" {
		return nil
	}
	dir := path.Dir("/" + libcPath)

	var missing []string
	var purposes []string
	if importsAny(in.ImportedSymbols, nssDNSSymbols) {
		if module := path.Join(dir, "libnss_dns.so.2"); !hasExactPath(module, in.FilePaths) {
			missing = append(missing, module)
			purposes = append(purposes, "resolve hostnames")
		}
	}
	if importsAny(in.ImportedSymbols, nssUserSymbols) {
		if module := path.Join(dir, "libnss_files.so.2"); !hasExactPath(module, in.FilePaths) {
			missing = append(missing, module)
			purposes = append(purposes, "look up users or groups")
		}
	}
	if len(missing) == 0 {
		return nil
	}

	f := finding{
		check:   "nss-modules",
		Message: fmt.Sprintf("%s may need to %s at runtime", in.Entrypoint, strings.Join(purposes, " and ")),
		Help:    missingRuntimeFilesFix(missing, in.SourceStage),
	}
	if in.Dockerfile != nil && in.EntrypointCopy != nil {
		f.File = in.Dockerfile.path
		f.Line = in.EntrypointCopy.StartLine
		raw := in.Dockerfile.rawInstruction(in.EntrypointCopy.StartLine)
		f.Edits = []sourceEdit{{
			File:        f.File,
			Line:        f.Line,
			Old:         raw,
			New:         raw + "\n" + missingRuntimeFilesFix(missing, in.SourceStage),
			Description: "copy missing libraries into the final image",
		}}
	}
	return []finding{f}
}

func importsAny(names map[string]struct{}, symbols []string) bool {
	for _, s := range symbols {
		if _, ok := names[s]; ok {
			return true
		}
	}
	return false
}

// checkEntrypointExecutable verifies that every hop in EntrypointChain has an
// execute bit set. The container runtime execve()s each of these paths in
// turn (the declared entrypoint, then whatever it in turn execs via further
// script hops); a missing +x anywhere in that chain fails with EACCES before
// the application ever runs, regardless of what every other check concludes.
func checkEntrypointExecutable(ctx *checkContext) []finding {
	var findings []finding
	for _, hop := range ctx.EntrypointChain {
		if !ctx.rootfs.HasPath(hop) || ctx.rootfs.IsExecutable(hop) {
			continue
		}
		f := finding{
			check:   "entrypoint-executable",
			Message: fmt.Sprintf("%s is not marked executable in the image; the container runtime's exec will fail with a permission error", hop),
			Help:    fmt.Sprintf("Make %s executable, e.g. by adding --chmod=755 to the COPY/ADD instruction that places it.", hop),
		}
		if ctx.Dockerfile != nil {
			if node := ctx.Dockerfile.findEntrypointCopy(hop); node != nil {
				raw := ctx.Dockerfile.rawInstruction(node.StartLine)
				f.File = ctx.Dockerfile.path
				f.Line = node.StartLine
				f.Edits = []sourceEdit{{
					File:        f.File,
					Line:        f.Line,
					Old:         raw,
					New:         withChmodFlag(raw),
					Description: "mark the copied file executable",
				}}
			}
		}
		findings = append(findings, f)
	}
	return findings
}

// withChmodFlag returns raw (a COPY/ADD instruction line) with --chmod=755
// added, or an existing --chmod flag's value replaced with 755.
func withChmodFlag(raw string) string {
	if i := strings.Index(raw, "--chmod="); i >= 0 {
		end := i + len("--chmod=")
		for end < len(raw) && !unicode.IsSpace(rune(raw[end])) {
			end++
		}
		return raw[:i] + "--chmod=755" + raw[end:]
	}
	trimmed := strings.TrimLeft(raw, " \t")
	indent := raw[:len(raw)-len(trimmed)]
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return raw
	}
	rest := strings.TrimPrefix(trimmed, fields[0])
	return indent + fields[0] + " --chmod=755" + rest
}

func missingRuntimeMessage(entrypoint string, count int) string {
	if count == 1 {
		return fmt.Sprintf("%s requires a runtime file that is not present in the image", entrypoint)
	}
	return fmt.Sprintf("%s requires %d runtime files that are not present in the image", entrypoint, count)
}

func elfInterp(f *elf.File) string {
	for _, prog := range f.Progs {
		if prog.Type != elf.PT_INTERP {
			continue
		}
		data := make([]byte, prog.Filesz)
		if _, err := prog.ReadAt(data, 0); err != nil {
			return ""
		}
		return strings.TrimRight(string(data), "\x00")
	}
	return ""
}

func hasExactPath(path string, paths map[string]struct{}) bool {
	_, ok := paths[strings.TrimPrefix(path, "/")]
	return ok
}

func hasLibrary(lib string, paths map[string]struct{}, extraDirs []string) bool {
	for p := range paths {
		if path.Base(p) == lib && isLibraryPath(p, extraDirs) {
			return true
		}
	}
	return false
}

func findLibraryPath(lib string, paths map[string]struct{}, extraDirs []string) string {
	var matches []string
	for p := range paths {
		if path.Base(p) == lib && isLibraryPath(p, extraDirs) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		return ""
	}
	slices.SortFunc(matches, func(a, b string) int {
		return libraryPathRank(a, extraDirs) - libraryPathRank(b, extraDirs)
	})
	return matches[0]
}

func libraryPathRank(p string, extraDirs []string) int {
	p = strings.TrimPrefix(p, "/")
	for i, dir := range []string{"lib", "lib64", "usr/lib", "usr/local/lib"} { //nolint:goconst // single-use list, not duplicated elsewhere in production code
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return i
		}
	}
	for i, dir := range extraDirs {
		dir = cleanRootfsPath(strings.TrimSpace(dir))
		if dir != "" && (p == dir || strings.HasPrefix(p, dir+"/")) {
			return 100 + i
		}
	}
	return 1000
}

func isLibraryPath(p string, extraDirs []string) bool {
	p = strings.TrimPrefix(p, "/")
	if strings.HasPrefix(p, "lib/") ||
		strings.HasPrefix(p, "lib64/") ||
		strings.HasPrefix(p, "usr/lib/") ||
		strings.HasPrefix(p, "usr/local/lib/") {
		return true
	}
	for _, dir := range extraDirs {
		dir = cleanRootfsPath(strings.TrimSpace(dir))
		if dir != "" && (p == dir || strings.HasPrefix(p, dir+"/")) {
			return true
		}
	}
	return false
}

func envLibraryDirs(env []string) []string {
	for _, item := range env {
		value, ok := strings.CutPrefix(item, "LD_LIBRARY_PATH=")
		if !ok {
			continue
		}
		dirs := strings.Split(value, ":")
		out := dirs[:0]
		for _, dir := range dirs {
			if strings.TrimSpace(dir) != "" {
				out = append(out, dir)
			}
		}
		return out
	}
	return nil
}

func elfLibraryDirs(f *elf.File, entrypoint string) []string {
	if f == nil {
		return nil
	}
	var dirs []string
	for _, tag := range []elf.DynTag{elf.DT_RUNPATH, elf.DT_RPATH} {
		values, err := f.DynString(tag)
		if err != nil {
			continue
		}
		for _, value := range values {
			for _, dir := range strings.Split(value, ":") {
				dir = strings.TrimSpace(dir)
				if dir == "" {
					continue
				}
				dirs = append(dirs, expandELFOrigin(dir, entrypoint))
			}
		}
	}
	return dirs
}

func expandELFOrigin(dir, entrypoint string) string {
	origin := path.Dir(entrypoint)
	dir = strings.ReplaceAll(dir, "${ORIGIN}", origin)
	dir = strings.ReplaceAll(dir, "$ORIGIN", origin)
	return cleanRootfsPath(dir)
}

func cleanRootfsPath(p string) string {
	p = normalizeTarPath(p)
	if p == "" {
		return ""
	}
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

func missingRuntimeHelp(paths []string, unresolved []string, sourceStage string) string {
	var parts []string
	if len(paths) > 0 {
		parts = append(parts, missingRuntimeFilesFix(paths, sourceStage))
	}
	for _, lib := range unresolved {
		parts = append(parts, fmt.Sprintf("copy %s from %s into a runtime library path, or add the directory containing it to LD_LIBRARY_PATH", lib, sourceStageOrPlaceholder(sourceStage)))
	}
	return strings.Join(parts, "\n")
}

func sourceStageOrPlaceholder(sourceStage string) string {
	if sourceStage != "" {
		return sourceStage
	}
	return "<stage>"
}

func missingRuntimeFilesFix(paths []string, sourceStage string) string {
	stage := sourceStageOrPlaceholder(sourceStage)
	lines := make([]string, 0, len(paths))
	for _, path := range paths {
		lines = append(lines, fmt.Sprintf("COPY --from=%s %s %s", stage, path, path))
	}
	return strings.Join(lines, "\n")
}
