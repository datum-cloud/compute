//nolint:goconst // table-driven test cases intentionally repeat literals
package build

import (
	"debug/elf"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeDockerfile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindEntrypointProducerFollowsCopyFromStage(t *testing.T) {
	path := writeDockerfile(t, `FROM golang:1.24 AS builder
WORKDIR /src
RUN go build -o /out/app ./cmd/app

FROM debian:bookworm
RUN gcc -o /tmp/not-entrypoint ./not-entrypoint.c
COPY --from=builder /out/app /usr/local/bin/app
ENTRYPOINT ["/usr/local/bin/app"]
`)
	df, err := parseDockerfileAST(path)
	if err != nil {
		t.Fatal(err)
	}

	node, sourceStage := df.findEntrypointProducer("/usr/local/bin/app", toolchain{Kind: toolchainGo})
	if node == nil {
		t.Fatal("expected producer")
	}
	if node.StartLine != 3 {
		t.Fatalf("expected line 3, got %d", node.StartLine)
	}
	if sourceStage != "builder" {
		t.Fatalf("expected source stage builder, got %q", sourceStage)
	}
}

func TestFindEntrypointCopy(t *testing.T) {
	path := writeDockerfile(t, `FROM build AS build
RUN touch /server
FROM scratch
COPY --from=build /server /server
ENTRYPOINT ["/server"]
`)
	df, err := parseDockerfileAST(path)
	if err != nil {
		t.Fatal(err)
	}
	node := df.findEntrypointCopy("/server")
	if node == nil {
		t.Fatal("expected entrypoint copy")
	}
	if node.StartLine != 4 {
		t.Fatalf("expected line 4, got %d", node.StartLine)
	}
}

func TestFindEntrypointCopySourceStage(t *testing.T) {
	path := writeDockerfile(t, `FROM golang:1.26 AS build
RUN go build -o /test-logger .
FROM scratch
COPY --from=build /test-logger /test-logger
ENTRYPOINT ["/test-logger"]
`)
	df, err := parseDockerfileAST(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := df.findEntrypointCopySourceStage("/test-logger"); got != "build" {
		t.Fatalf("expected build source stage, got %q", got)
	}
}

func TestFindEntrypointProducerIgnoresUnrelatedCompilerRun(t *testing.T) {
	path := writeDockerfile(t, `FROM alpine
RUN gcc -o /tmp/helper ./helper.c
COPY ./app /usr/local/bin/app
ENTRYPOINT ["/usr/local/bin/app"]
`)
	df, err := parseDockerfileAST(path)
	if err != nil {
		t.Fatal(err)
	}

	node, sourceStage := df.findEntrypointProducer("/usr/local/bin/app", toolchain{Kind: toolchainNative})
	if node != nil {
		t.Fatalf("expected no producer, got line %d", node.StartLine)
	}
	if sourceStage != "" {
		t.Fatalf("expected no source stage, got %q", sourceStage)
	}
}

func TestToolchainNativeLabel(t *testing.T) {
	if got := toolchainLabel(toolchain{Kind: toolchainNative}); got != "native binary" {
		t.Fatalf("expected native binary, got %q", got)
	}
}

func TestResolveStartupArgsHandlesCommonDockerLaunchForms(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		want       string
		wantArgs   []string
		wantShells []string
	}{
		{name: "bash shell form",
			args: []string{"/bin/bash", "-c", "exec node server.js"},
			want: "node", wantShells: []string{"/bin/bash"}},
		{name: "leading env assignment",
			args: []string{"/bin/sh", "-c", "NODE_ENV=production node server.js"},
			want: "node", wantShells: []string{"/bin/sh"}},
		{name: "env executable",
			args: []string{"/usr/bin/env", "NODE_ENV=production", "node", "/app/server.js"},
			want: "node"},
		{name: "custom binary named env outside canonical path",
			args: []string{"/app/env", "start"},
			want: "/app/env"},
		{name: "inherited entrypoint wrapper with cmd",
			args: []string{"docker-entrypoint.sh", "postgres"},
			want: "postgres"},
		{name: "shell script exec form",
			args: []string{"/bin/sh", "/entrypoint.sh"},
			want: "/entrypoint.sh", wantShells: []string{"/bin/sh"}},
		{name: "entrypoint script wrapper with cmd",
			args: []string{"/usr/local/bin/docker-entrypoint.sh", "node", "./server.mjs"},
			want: "node"},
		{name: "common passthrough wrapper",
			args: []string{"/usr/bin/wrapper.sh", "/usr/bin/node", "/app/server.js"},
			want: "/usr/bin/node"},
		// Our own workdir wrapper's shell isn't reported in wantShells:
		// findShell already guaranteed it exists before
		// wrapArgsWithWorkingDirShell used it.
		{name: "own workdir shell wrapper",
			args: []string{"/bin/sh", "-c", "cd /app && exec \"$@\"", workdirWrapperArg0, "/usr/local/bin/bun", "run", "start"},
			want: "/usr/local/bin/bun"},
		// Regression test for a real packaging bug: a Dockerfile shell-form
		// CMD ("CMD npm start") compiles to ["/bin/sh","-c","npm start"].
		// Before trace-based resolution this collapsed to a single mangled
		// argv element ["npm start"], which failed to exec at runtime.
		{name: "shell-form CMD splits into entrypoint and args",
			args: []string{"/bin/sh", "-c", "npm start"},
			want: "npm", wantArgs: []string{"start"}, wantShells: []string{"/bin/sh"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := resolveStartupArgs(tt.args, nil, nil)
			if cmd.Entrypoint != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, cmd.Entrypoint)
			}
			if tt.wantArgs != nil && !slices.Equal(cmd.Args, tt.wantArgs) {
				t.Fatalf("expected args %v, got %v", tt.wantArgs, cmd.Args)
			}
			if !slices.Equal(cmd.Shells, tt.wantShells) {
				t.Fatalf("expected shells %v, got %v", tt.wantShells, cmd.Shells)
			}
		})
	}
}

// TestUnwrapWorkdirWrapperArgsRequiresRealCommand is a regression test:
// wrapArgsWithWorkingDirShell only ever wraps a non-empty args, so a marker
// with nothing after it isn't a real match — both resolveStartupArgs and
// inspect.go's unwrapWorkdirCommand shared this pattern independently, with
// thresholds that had drifted out of sync (>=5 vs >=4) before being unified.
func TestUnwrapWorkdirWrapperArgsRequiresRealCommand(t *testing.T) {
	_, ok := unwrapWorkdirWrapperArgs([]string{"/bin/sh", "-c", "cd /app && exec \"$@\"", workdirWrapperArg0})
	if ok {
		t.Fatal("expected no match for a marker with no real command after it")
	}
}

func TestUnwrapWorkdirWrapperArgsUnwrapsRealCommand(t *testing.T) {
	rest, ok := unwrapWorkdirWrapperArgs([]string{"/bin/sh", "-c", "cd /app && exec \"$@\"", workdirWrapperArg0, "node", "server.js"})
	if !ok {
		t.Fatal("expected a match")
	}
	if !slices.Equal(rest, []string{"node", "server.js"}) {
		t.Fatalf("expected [node server.js], got %v", rest)
	}
}

func TestBuildAnalysisResultFromViewRelativeScriptGuard(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantRelScript bool
	}{
		{name: "bare relative script alone is rejected",
			args: []string{"start.sh"}, wantRelScript: true},
		{name: "absolute script path is allowed through",
			args: []string{"/start.sh"}},
		// Not a recognized wrapper name, so trailing args don't save it —
		// firstExecutableWord only ever skips recognized wrapper names.
		{name: "bare relative script with trailing args is still rejected",
			args: []string{"start.sh", "--verbose"}, wantRelScript: true},
		// A recognized wrapper name *with* something after it resolves past
		// the wrapper (firstExecutableWord skips it), so it's fine.
		{name: "recognized wrapper name with a real command is allowed through",
			args: []string{"entrypoint.sh", "node", "server.js"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := &tarFSView{Paths: map[string]struct{}{}}
			_, err := buildAnalysisResultFromView(&options{}, view, tt.args, nil, nil, nil)
			if err == nil {
				t.Fatal("expected an error (either relative-script or entrypoint-not-found)")
			}
			if gotRelScript := strings.Contains(err.Error(), "relative script"); gotRelScript != tt.wantRelScript {
				t.Fatalf("expected relative-script error = %v, got: %v", tt.wantRelScript, err)
			}
		})
	}
}

func TestCandidateEntrypointPathsSearchesCommonPathDirs(t *testing.T) {
	candidates := candidateEntrypointPaths("node")
	if _, ok := candidates["usr/local/bin/node"]; !ok {
		t.Fatalf("expected usr/local/bin/node candidate, got %#v", candidates)
	}
	if _, ok := candidates["usr/bin/node"]; !ok {
		t.Fatalf("expected usr/bin/node candidate, got %#v", candidates)
	}
}

func TestCandidateEntrypointPathsKeepsExplicitPath(t *testing.T) {
	candidates := candidateEntrypointPaths("/usr/bin/node")
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %#v", candidates)
	}
	if _, ok := candidates["usr/bin/node"]; !ok {
		t.Fatalf("expected usr/bin/node candidate, got %#v", candidates)
	}
}

func TestResolveEntrypointPathUsesImagePathDirs(t *testing.T) {
	got := resolveEntrypointPathWithDirs("java", map[string]struct{}{"opt/java/openjdk/bin/java": {}}, []string{"/opt/java/openjdk/bin"})
	if got != "/opt/java/openjdk/bin/java" {
		t.Fatalf("expected /opt/java/openjdk/bin/java, got %q", got)
	}
}

func TestNormalizeRootFSArgsDropsInheritedEntrypointWrapper(t *testing.T) {
	got, err := normalizeRootFSArgs([]string{"/usr/local/bin/docker-entrypoint.sh", "node", "./server.mjs"}, "/", nil, nil)
	if err != nil {
		t.Fatalf("normalizeRootFSArgs returned error: %v", err)
	}
	want := []string{"node", "./server.mjs"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestFindShellPrefersBinSh(t *testing.T) {
	got := findShell(map[string]struct{}{
		"usr/bin/bash": {},
		"bin/sh":       {},
	})
	if got != "/bin/sh" {
		t.Fatalf("expected /bin/sh, got %q", got)
	}
}

func TestWrapArgsWithWorkingDirShell(t *testing.T) {
	got := wrapArgsWithWorkingDirShell("/bin/sh", "/app's dir", []string{"/usr/local/bin/bun", "run", "start"})
	want := []string{"/bin/sh", "-c", "cd '/app'\\''s dir' && exec \"$@\"", "datum-workdir", "/usr/local/bin/bun", "run", "start"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestHasLibraryRequiresPlausibleLibraryPath(t *testing.T) {
	if hasLibrary("libc.so.6", map[string]struct{}{"usr/share/doc/libc.so.6": {}}, nil) {
		t.Fatal("expected doc path not to satisfy library")
	}
	if hasLibrary("libc.so.6", map[string]struct{}{"app/lib/libc.so.6": {}}, nil) {
		t.Fatal("expected app-local lib path not to satisfy library without loader metadata")
	}
	if !hasLibrary("libc.so.6", map[string]struct{}{"lib/x86_64-linux-gnu/libc.so.6": {}}, nil) {
		t.Fatal("expected lib path to satisfy library")
	}
	if hasLibrary("libjli.so", map[string]struct{}{"opt/java/openjdk/lib/libjli.so": {}}, nil) {
		t.Fatal("expected runtime lib path to require loader metadata")
	}
	if !hasLibrary("libjli.so", map[string]struct{}{"opt/java/openjdk/lib/libjli.so": {}}, []string{"/opt/java/openjdk/lib"}) {
		t.Fatal("expected LD_LIBRARY_PATH runtime lib path to satisfy library")
	}
	if !hasLibrary("libjli.so", map[string]struct{}{"opt/java/openjdk/lib/libjli.so": {}}, []string{"opt/java/openjdk/bin/../lib"}) {
		t.Fatal("expected RUNPATH runtime lib path to satisfy library")
	}
}

func TestExpandELFOrigin(t *testing.T) {
	got := expandELFOrigin("$ORIGIN/../lib", "/opt/java/openjdk/bin/java")
	if got != "opt/java/openjdk/lib" {
		t.Fatalf("expected opt/java/openjdk/lib, got %q", got)
	}
}

func TestFindLibraryPathRequiresUniquePlausiblePath(t *testing.T) {
	if got := findLibraryPath("libc.musl-x86_64.so.1", map[string]struct{}{"lib/libc.musl-x86_64.so.1": {}}, nil); got != "lib/libc.musl-x86_64.so.1" {
		t.Fatalf("expected musl libc path, got %q", got)
	}
	if got := findLibraryPath("libfoo.so", map[string]struct{}{"lib/libfoo.so": {}, "usr/lib/libfoo.so": {}}, nil); got != "lib/libfoo.so" {
		t.Fatalf("expected loader-order library path, got %q", got)
	}
	if got := findLibraryPath("libfoo.so", map[string]struct{}{"app/lib/libfoo.so": {}}, nil); got != "" {
		t.Fatalf("expected implausible library path to be unresolved, got %q", got)
	}
}

func TestHasExactPath(t *testing.T) {
	paths := map[string]struct{}{"lib64/ld-linux-x86-64.so.2": {}}
	if !hasExactPath("/lib64/ld-linux-x86-64.so.2", paths) {
		t.Fatal("expected exact interpreter path to be found")
	}
	if hasExactPath("/lib/ld-musl-x86_64.so.1", paths) {
		t.Fatal("expected different interpreter path not to be found")
	}
}

func TestSymlinkPathAliasesResolveDirectorySymlink(t *testing.T) {
	paths := map[string]struct{}{
		"lib64":                          {},
		"usr/lib64/ld-linux-x86-64.so.2": {},
	}
	symlinks := map[string]string{"lib64": "usr/lib64"}
	addSymlinkPathAliases(paths, symlinks)

	if !hasExactPath("/lib64/ld-linux-x86-64.so.2", paths) {
		t.Fatalf("expected loader under symlinked /lib64 to be found, paths: %#v", paths)
	}
}

func TestResolvePathThroughSymlinks(t *testing.T) {
	symlinks := map[string]string{
		"usr/local/bin/python":  "python3",
		"usr/local/bin/python3": "python3.13",
		"bin":                   "usr/bin",
	}
	if got := resolvePathThroughSymlinks("/usr/local/bin/python", symlinks); got != "usr/local/bin/python3.13" {
		t.Fatalf("expected usr/local/bin/python3.13, got %q", got)
	}
	if got := resolvePathThroughSymlinks("/bin/sh", symlinks); got != "usr/bin/sh" {
		t.Fatalf("expected usr/bin/sh, got %q", got)
	}
}

func TestCheckMissingLibrariesReportsDynamicLoader(t *testing.T) {
	data := buildLinuxGoBinary(t, "loader-test", "-buildmode=pie")
	_, elfFile := detectToolchain(data)
	if elfFile == nil {
		t.Fatal("expected ELF")
	}
	if interp := elfInterp(elfFile); interp == "" {
		t.Skip("test toolchain produced no PT_INTERP")
	}
	findings := checkMissingLibraries(&checkContext{
		EntrypointChain: []string{"/server"},
		ELF:             elfFile,
		rootfs:          &tarFSView{Paths: map[string]struct{}{}},
	})
	if len(findings) == 0 {
		t.Fatal("expected missing loader finding")
	}
	if !strings.Contains(findings[0].Message, "runtime file") {
		t.Fatalf("unexpected finding: %#v", findings[0])
	}
}

func TestCheckELFArchitectureAcceptsX8664(t *testing.T) {
	data := buildLinuxGoBinary(t, "arch-test", "-buildmode=pie")
	_, elfFile := detectToolchain(data)
	if elfFile == nil {
		t.Fatal("expected ELF")
	}
	findings := checkELFArchitecture(&checkContext{EntrypointChain: []string{"/server"}, ELF: elfFile})
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckELFArchitectureReportsNonX8664(t *testing.T) {
	findings := checkELFArchitecture(&checkContext{
		EntrypointChain: []string{"/server"},
		ELF:             &elf.File{FileHeader: elf.FileHeader{Machine: elf.EM_AARCH64}},
	})
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %#v", findings)
	}
	if !strings.Contains(findings[0].Message, "x86_64") {
		t.Fatalf("unexpected finding: %#v", findings[0])
	}
}

func TestCheckMissingLibrariesGroupsRuntimeFiles(t *testing.T) {
	data := buildLinuxGoBinary(t, "group-missing-test", "-buildmode=pie")
	tc, elfFile := detectToolchain(data)
	if elfFile == nil || elfInterp(elfFile) == "" {
		t.Skip("test toolchain produced no dynamic ELF")
	}
	path := writeDockerfile(t, `FROM golang AS build
RUN go build -o /server .
FROM scratch
COPY --from=build /server /server
ENTRYPOINT ["/server"]
`)
	df, err := parseDockerfileAST(path)
	if err != nil {
		t.Fatal(err)
	}
	findings := checkMissingLibraries(&checkContext{
		EntrypointChain: []string{"/server"},
		ELF:             elfFile,
		toolchain:       tc,
		rootfs:          &tarFSView{Paths: map[string]struct{}{}},
		Dockerfile:      df,
		RuntimeFileView: func() (*tarFSView, error) {
			// The source stage has the loader at the same exact path the
			// binary's PT_INTERP names, so it's verified as auto-fixable.
			return &tarFSView{Paths: map[string]struct{}{strings.TrimPrefix(elfInterp(elfFile), "/"): {}}}, nil
		},
	})
	if len(findings) != 1 {
		t.Fatalf("expected one grouped finding, got %#v", findings)
	}
	if !strings.Contains(findings[0].Help, "COPY --from=build") {
		t.Fatalf("expected COPY help, got %q", findings[0].Help)
	}
}

// TestCheckMissingLibrariesDoesNotAutoFixUnverifiedInterp is a regression test:
// the dynamic loader path must only be treated as auto-fixable once the exact
// same path is confirmed to exist in the stage the fix would copy it from —
// otherwise the generated COPY line could reference a path that isn't there.
func TestCheckMissingLibrariesDoesNotAutoFixUnverifiedInterp(t *testing.T) {
	data := buildLinuxGoBinary(t, "unverified-interp-test", "-buildmode=pie")
	tc, elfFile := detectToolchain(data)
	if elfFile == nil || elfInterp(elfFile) == "" {
		t.Skip("test toolchain produced no dynamic ELF")
	}
	path := writeDockerfile(t, `FROM golang AS build
RUN go build -o /server .
FROM scratch
COPY --from=build /server /server
ENTRYPOINT ["/server"]
`)
	df, err := parseDockerfileAST(path)
	if err != nil {
		t.Fatal(err)
	}
	findings := checkMissingLibraries(&checkContext{
		EntrypointChain: []string{"/server"},
		ELF:             elfFile,
		toolchain:       tc,
		rootfs:          &tarFSView{Paths: map[string]struct{}{}},
		Dockerfile:      df,
		RuntimeFileView: func() (*tarFSView, error) {
			// The source stage does NOT have the loader at the expected path.
			return &tarFSView{Paths: map[string]struct{}{}}, nil
		},
	})
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %#v", findings)
	}
	if strings.Contains(findings[0].Help, "COPY --from=build "+elfInterp(elfFile)) {
		t.Fatalf("expected no auto-fix COPY line for the unverified interp, got %q", findings[0].Help)
	}
	if !strings.Contains(findings[0].Help, elfInterp(elfFile)) {
		t.Fatalf("expected the interp path named in prose help, got %q", findings[0].Help)
	}
}

func TestCheckMissingLibrariesDoesNotResolveRuntimeFilesWhenComplete(t *testing.T) {
	data := buildLinuxGoBinary(t, "complete-libs-test", "-buildmode=pie")
	_, elfFile := detectToolchain(data)
	if elfFile == nil || elfInterp(elfFile) == "" {
		t.Skip("test toolchain produced no dynamic ELF")
	}
	filePaths := map[string]struct{}{}
	filePaths[strings.TrimPrefix(elfInterp(elfFile), "/")] = struct{}{}
	libs, err := elfFile.ImportedLibraries()
	if err != nil {
		t.Fatal(err)
	}
	for _, lib := range libs {
		filePaths["lib/x86_64-linux-gnu/"+lib] = struct{}{}
	}
	called := false
	findings := checkMissingLibraries(&checkContext{
		EntrypointChain: []string{"/server"},
		ELF:             elfFile,
		rootfs:          &tarFSView{Paths: filePaths},
		RuntimeFileView: func() (*tarFSView, error) {
			called = true
			return nil, nil
		},
	})
	if called {
		t.Fatal("expected runtime file resolver not to be called")
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckMissingLibrariesResolvesRuntimeFilesLazily(t *testing.T) {
	data := buildLinuxGoBinary(t, "lazy-libs-test", "-buildmode=pie")
	_, elfFile := detectToolchain(data)
	if elfFile == nil || elfInterp(elfFile) == "" {
		t.Skip("test toolchain produced no dynamic ELF")
	}
	libs, err := elfFile.ImportedLibraries()
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) == 0 {
		t.Skip("test toolchain produced no imported libraries")
	}
	path := writeDockerfile(t, `FROM golang AS build
RUN go build -o /server .
FROM scratch
COPY --from=build /server /server
ENTRYPOINT ["/server"]
`)
	df, err := parseDockerfileAST(path)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	findings := checkMissingLibraries(&checkContext{
		EntrypointChain: []string{"/server"},
		ELF:             elfFile,
		toolchain:       toolchain{Kind: toolchainGo},
		rootfs:          &tarFSView{Paths: map[string]struct{}{}},
		Dockerfile:      df,
		RuntimeFileView: func() (*tarFSView, error) {
			called = true
			return &tarFSView{Paths: map[string]struct{}{"lib/x86_64-linux-gnu/" + libs[0]: {}}}, nil
		},
	})
	if !called {
		t.Fatal("expected runtime file resolver to be called")
	}
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %#v", findings)
	}
	if !strings.Contains(findings[0].Help, "/lib/x86_64-linux-gnu/"+libs[0]) {
		t.Fatalf("expected resolved library path in help, got %q", findings[0].Help)
	}
}

func TestMissingRuntimeFilesFixFormatsMultipleCopyCommands(t *testing.T) {
	got := missingRuntimeFilesFix([]string{
		"/lib64/ld-linux-x86-64.so.2",
		"/lib/x86_64-linux-gnu/libc.so.6",
	}, "build")
	if !strings.Contains(got, "\n") {
		t.Fatalf("expected multi-line fix, got %q", got)
	}
	for _, want := range []string{
		"COPY --from=build /lib64/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2",
		"COPY --from=build /lib/x86_64-linux-gnu/libc.so.6 /lib/x86_64-linux-gnu/libc.so.6",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %q", want, got)
		}
	}
}

func TestNSSModuleFindings(t *testing.T) {
	tests := []struct {
		name        string
		symbols     map[string]struct{}
		filePaths   map[string]struct{}
		wantMessage string // if empty, expect no findings
		wantHelp    string
	}{
		{name: "missing DNS module",
			symbols:     map[string]struct{}{"getaddrinfo": {}},
			filePaths:   map[string]struct{}{"lib/x86_64-linux-gnu/libc.so.6": {}},
			wantMessage: "resolve hostnames", wantHelp: "/lib/x86_64-linux-gnu/libnss_dns.so.2"},
		{name: "missing user/group module",
			symbols:     map[string]struct{}{"getpwnam": {}},
			filePaths:   map[string]struct{}{"lib/x86_64-linux-gnu/libc.so.6": {}},
			wantMessage: "look up users or groups", wantHelp: "/lib/x86_64-linux-gnu/libnss_files.so.2"},
		{name: "present modules accepted",
			symbols: map[string]struct{}{"getaddrinfo": {}, "getpwnam": {}},
			filePaths: map[string]struct{}{
				"lib/x86_64-linux-gnu/libc.so.6":         {},
				"lib/x86_64-linux-gnu/libnss_dns.so.2":   {},
				"lib/x86_64-linux-gnu/libnss_files.so.2": {},
			}},
		{name: "unrelated symbols ignored",
			symbols:   map[string]struct{}{"malloc": {}, "printf": {}},
			filePaths: map[string]struct{}{"lib/x86_64-linux-gnu/libc.so.6": {}}},
		// Avoids piling a second, confusing finding on top of what
		// checkMissingLibraries already reports for the same cause.
		{name: "skipped when libc itself is missing",
			symbols:   map[string]struct{}{"getaddrinfo": {}},
			filePaths: map[string]struct{}{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := nssModuleFindings(nssModuleInputs{
				Entrypoint:      "/server",
				ImportedSymbols: tt.symbols,
				FilePaths:       tt.filePaths,
			})
			if tt.wantMessage == "" {
				if len(findings) != 0 {
					t.Fatalf("expected no findings, got %#v", findings)
				}
				return
			}
			if len(findings) != 1 {
				t.Fatalf("expected one finding, got %#v", findings)
			}
			if !strings.Contains(findings[0].Message, tt.wantMessage) {
				t.Fatalf("unexpected finding: %#v", findings[0])
			}
			if !strings.Contains(findings[0].Help, tt.wantHelp) {
				t.Fatalf("unexpected help: %#v", findings[0])
			}
		})
	}
}

// TestNSSModuleFindingsProducesExactLineFix is a regression test for --fix:
// it needs a real edit, not just Help prose, the same way checkMissingLibraries
// appends COPY lines after the entrypoint's own COPY instruction.
func TestNSSModuleFindingsProducesExactLineFix(t *testing.T) {
	path := writeDockerfile(t, `FROM build AS build
RUN touch /server
FROM scratch
COPY --from=build /server /server
ENTRYPOINT ["/server"]
`)
	df, err := parseDockerfileAST(path)
	if err != nil {
		t.Fatal(err)
	}
	entrypointCopy := df.findEntrypointCopy("/server")
	if entrypointCopy == nil {
		t.Fatal("expected entrypoint copy")
	}

	findings := nssModuleFindings(nssModuleInputs{
		Entrypoint:      "/server",
		ImportedSymbols: map[string]struct{}{"getaddrinfo": {}},
		FilePaths:       map[string]struct{}{"lib/x86_64-linux-gnu/libc.so.6": {}},
		Dockerfile:      df,
		EntrypointCopy:  entrypointCopy,
		SourceStage:     "build",
	})
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %#v", findings)
	}
	if len(findings[0].Edits) != 1 {
		t.Fatalf("expected an exact-line edit, got %#v", findings[0])
	}
	edit := findings[0].Edits[0]
	if !isExactLineEdit(edit) {
		t.Fatalf("expected an exact-line edit, got %#v", edit)
	}
	want := "COPY --from=build /server /server\nCOPY --from=build /lib/x86_64-linux-gnu/libnss_dns.so.2 /lib/x86_64-linux-gnu/libnss_dns.so.2"
	if edit.New != want {
		t.Fatalf("expected %q, got %q", want, edit.New)
	}
}

// TestCheckNSSModulesSkipsStaticBinaries is a light integration test for the
// ELF-introspection half of the check (nssModuleFindings above covers the
// policy half hermetically): a CGO_ENABLED=0 binary has no PT_INTERP, so the
// check must not even attempt symbol introspection.
func TestCheckNSSModulesSkipsStaticBinaries(t *testing.T) {
	data := buildLinuxGoBinary(t, "nss-static-test")
	_, elfFile := detectToolchain(data)
	if elfFile == nil {
		t.Fatal("expected ELF")
	}
	if interp := elfInterp(elfFile); interp != "" {
		t.Skip("test toolchain produced a dynamically-linked binary")
	}
	findings := checkNSSModules(&checkContext{
		EntrypointChain: []string{"/server"},
		ELF:             elfFile,
		rootfs:          &tarFSView{Paths: map[string]struct{}{}},
	})
	if len(findings) != 0 {
		t.Fatalf("expected no findings for a static binary, got %#v", findings)
	}
}

func TestCheckShellForm(t *testing.T) {
	tests := []struct {
		name    string
		paths   map[string]struct{}
		wantErr bool
	}{
		{name: "missing shell", paths: map[string]struct{}{}, wantErr: true},
		{name: "present shell", paths: map[string]struct{}{"bin/sh": {}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := checkShellForm(&checkContext{
				RequiredShells: []string{"/bin/sh"},
				rootfs:         &tarFSView{Paths: tt.paths},
			})
			if !tt.wantErr {
				if len(findings) != 0 {
					t.Fatalf("expected no findings, got %#v", findings)
				}
				return
			}
			if len(findings) != 1 {
				t.Fatalf("expected one finding, got %#v", findings)
			}
			if !strings.Contains(findings[0].Message, "/bin/sh") {
				t.Fatalf("unexpected finding: %#v", findings[0])
			}
		})
	}
}

func TestCheckEntrypointExecutable(t *testing.T) {
	tests := []struct {
		name       string
		chain      []string
		paths      map[string]struct{}
		executable map[string]struct{}
		wantIn     string // if empty, expect no findings
	}{
		{name: "missing exec bit",
			chain:  []string{"/app/start.sh"},
			paths:  map[string]struct{}{"app/start.sh": {}},
			wantIn: "/app/start.sh"},
		{name: "executable file accepted",
			chain:      []string{"/app/start.sh"},
			paths:      map[string]struct{}{"app/start.sh": {}},
			executable: map[string]struct{}{"app/start.sh": {}}},
		// Each hop is exec'd separately at runtime, so a missing +x on an
		// inner hop is just as fatal as on the first, even though checks like
		// missing-libs/arch only ever look at the last (real binary) hop.
		{name: "every hop in a multi-hop chain is checked",
			chain:      []string{"/app/start.sh", "/app/inner.sh", "/app/server"},
			paths:      map[string]struct{}{"app/start.sh": {}, "app/inner.sh": {}, "app/server": {}},
			executable: map[string]struct{}{"app/start.sh": {}, "app/server": {}}, // inner.sh missing on purpose
			wantIn:     "/app/inner.sh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := checkEntrypointExecutable(&checkContext{
				EntrypointChain: tt.chain,
				rootfs:          &tarFSView{Paths: tt.paths, Executable: tt.executable},
			})
			if tt.wantIn == "" {
				if len(findings) != 0 {
					t.Fatalf("expected no findings, got %#v", findings)
				}
				return
			}
			if len(findings) != 1 {
				t.Fatalf("expected one finding, got %#v", findings)
			}
			if !strings.Contains(findings[0].Message, tt.wantIn) {
				t.Fatalf("unexpected finding: %#v", findings[0])
			}
		})
	}
}

// TestCheckEntrypointExecutableProducesExactLineFix is a regression test for
// --fix: it needs a real edit, not just Help prose, the same way
// checkMissingLibraries/nssModuleFindings append/modify a COPY line.
func TestCheckEntrypointExecutableProducesExactLineFix(t *testing.T) {
	path := writeDockerfile(t, `FROM build AS build
RUN touch /start.sh
FROM scratch
COPY --from=build /start.sh /start.sh
ENTRYPOINT ["/start.sh"]
`)
	df, err := parseDockerfileAST(path)
	if err != nil {
		t.Fatal(err)
	}

	findings := checkEntrypointExecutable(&checkContext{
		EntrypointChain: []string{"/start.sh"},
		rootfs:          &tarFSView{Paths: map[string]struct{}{"start.sh": {}}},
		Dockerfile:      df,
	})
	if len(findings) != 1 || len(findings[0].Edits) != 1 {
		t.Fatalf("expected one finding with one edit, got %#v", findings)
	}
	edit := findings[0].Edits[0]
	if !isExactLineEdit(edit) {
		t.Fatalf("expected an exact-line edit, got %#v", edit)
	}
	want := "COPY --chmod=755 --from=build /start.sh /start.sh"
	if edit.New != want {
		t.Fatalf("expected %q, got %q", want, edit.New)
	}
}

func TestWithChmodFlagReplacesExistingValue(t *testing.T) {
	got := withChmodFlag("COPY --chmod=644 --from=build /start.sh /start.sh")
	want := "COPY --chmod=755 --from=build /start.sh /start.sh"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestCheckStartupScript(t *testing.T) {
	tests := []struct {
		name       string
		entrypoint string
		script     string
		paths      map[string]struct{}
		wantIn     string // if empty, expect no findings
		wantNotIn  string
	}{
		{name: "missing /bin/sh interpreter",
			entrypoint: "/start.sh", script: "#!/bin/sh\nexec /app/server\n",
			paths: map[string]struct{}{}, wantIn: "/bin/sh"},
		{name: "present /bin/sh interpreter",
			entrypoint: "/start.sh", script: "#!/bin/sh\nexec /app/server\n",
			paths: map[string]struct{}{"bin/sh": {}}},
		{name: "no shebang assumes /bin/sh, missing",
			entrypoint: "/start.sh", script: "exec /app/server\n",
			paths: map[string]struct{}{}, wantIn: "/bin/sh"},
		{name: "no shebang assumes /bin/sh, present",
			entrypoint: "/start.sh", script: "exec /app/server\n",
			paths: map[string]struct{}{"bin/sh": {}}},
		{name: "env dispatch resolves real interpreter, present",
			entrypoint: "/usr/local/bin/wrapper.sh", script: "#!/usr/bin/env bash\nexec \"$@\"\n",
			paths: map[string]struct{}{"usr/bin/env": {}, "bin/bash": {}}},
		{name: "env dispatch missing env itself",
			entrypoint: "/usr/local/bin/wrapper.sh", script: "#!/usr/bin/env bash\nexec \"$@\"\n",
			paths: map[string]struct{}{"bin/bash": {}}, wantIn: "/usr/bin/env"},
		{name: "env dispatch missing target, not env itself",
			entrypoint: "/usr/local/bin/wrapper.sh", script: "#!/usr/bin/env bash\nexec \"$@\"\n",
			paths: map[string]struct{}{"usr/bin/env": {}}, wantIn: "bash", wantNotIn: "/usr/bin/env"},
		// A user binary named "env" outside canonical system bin dirs must
		// not be mistaken for env(1) dispatch.
		{name: "custom /app/env binary treated as literal interpreter",
			entrypoint: "/start.sh", script: "#!/app/env start\n",
			paths: map[string]struct{}{"app/env": {}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := checkStartupScript(&checkContext{
				EntrypointChain: []string{tt.entrypoint},
				toolchain:       toolchain{Kind: toolchainShell},
				BinaryData:      []byte(tt.script),
				rootfs:          &tarFSView{Paths: tt.paths},
			})
			if tt.wantIn == "" {
				if len(findings) != 0 {
					t.Fatalf("expected no findings, got %#v", findings)
				}
				return
			}
			if len(findings) != 1 {
				t.Fatalf("expected one finding, got %#v", findings)
			}
			if !strings.Contains(findings[0].Message, tt.wantIn) {
				t.Fatalf("expected message to contain %q, got %#v", tt.wantIn, findings[0])
			}
			if tt.wantNotIn != "" && strings.Contains(findings[0].Message, tt.wantNotIn) {
				t.Fatalf("expected message not to contain %q, got %#v", tt.wantNotIn, findings[0])
			}
		})
	}
}

func TestDefaultChecksAllowStaticETEXECEntrypoint(t *testing.T) {
	data := buildLinuxGoBinary(t, "et-exec-test")
	tc, elfFile := detectToolchain(data)
	if elfFile == nil {
		t.Fatal("expected ELF")
	}
	if elfFile.Type == elf.ET_DYN {
		t.Skip("test toolchain produced PIE by default")
	}
	result, err := runChecks(&analysisContext{
		EntrypointChain: []string{"/server"},
		toolchain:       tc,
		ELF:             elfFile,
		rootfs: &tarFSView{
			Paths:      map[string]struct{}{"server": {}},
			Executable: map[string]struct{}{"server": {}},
		},
	}, defaultChecks, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("expected no default findings, got %#v", result.Findings)
	}
}

func buildLinuxGoBinary(t *testing.T, module string, flags ...string) []byte {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/"+module+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "server")
	args := append([]string{"build"}, flags...)
	args = append(args, "-o", out, ".")
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestUnwrapWorkdirCommandUnwrapsRealCommand(t *testing.T) {
	got := unwrapWorkdirCommand([]string{"/bin/sh", "-c", "cd /app && exec \"$@\"", workdirWrapperArg0, "node", "server.js"})
	if !slices.Equal(got, []string{"node", "server.js"}) {
		t.Fatalf("expected [node server.js], got %v", got)
	}
}

func TestUnwrapWorkdirCommandLeavesNonWrapperArgsAlone(t *testing.T) {
	args := []string{"node", "server.js"}
	got := unwrapWorkdirCommand(args)
	if !slices.Equal(got, args) {
		t.Fatalf("expected unchanged %v, got %v", args, got)
	}
}

func TestRootfsBuildError(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string // exact match, if set
		wantIn    string // substring, checked when want is empty
		wantNotIn string
	}{
		{name: "keeps Dockerfile build failures as-is",
			input:  `process "/bin/sh -c go build ./..." did not complete successfully: exit code: 1`,
			wantIn: "go build", wantNotIn: "Docker is not running"},
		{name: "simplifies Docker availability failures",
			input: "could not connect to buildkit: could not start ephemeral BuildKit container",
			want:  "building root filesystem: Docker is not running or is not accessible"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rootfsBuildError(errors.New(tt.input)).Error()
			if tt.want != "" {
				if got != tt.want {
					t.Fatalf("expected %q, got %q", tt.want, got)
				}
				return
			}
			if !strings.Contains(got, tt.wantIn) {
				t.Fatalf("expected error to contain %q, got %q", tt.wantIn, got)
			}
			if tt.wantNotIn != "" && strings.Contains(got, tt.wantNotIn) {
				t.Fatalf("expected error not to contain %q, got %q", tt.wantNotIn, got)
			}
		})
	}
}
