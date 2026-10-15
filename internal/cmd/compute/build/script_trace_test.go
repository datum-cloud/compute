//nolint:goconst // table-driven test cases intentionally repeat literals
package build

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func mapFileReader(files map[string][]byte) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if data, ok := files[strings.TrimPrefix(path, "/")]; ok {
			return data, nil
		}
		return nil, fmt.Errorf("not found: %s", path)
	}
}

func filePathsOf(files map[string][]byte) map[string]struct{} {
	paths := make(map[string]struct{}, len(files))
	for p := range files {
		paths[p] = struct{}{}
	}
	return paths
}

func newScriptContext(entrypoint string, script []byte, args []string, files map[string][]byte) *analysisContext {
	paths := filePathsOf(files)
	return &analysisContext{
		EntrypointChain: []string{entrypoint},
		toolchain:       toolchain{Kind: toolchainShell},
		BinaryData:      script,
		Args:            args,
		rootfs:          &tarFSView{Paths: paths, Open: mapFileReader(files)},
	}
}

// TestResolveScriptEntrypointHandlesShellFeaturesSafely covers shell
// features that mvdan/sh can't sandbox on its own: cd and the "-x" test call
// a real, unsandboxed unix.Access syscall against the host (see
// traceScriptExecTarget's CallHandler), and "-d"/"-f"-style tests need
// directory/executable-bit data from the image, not the host. Every case
// here would fail — either by aborting the trace or resolving to the wrong
// target — without those interceptions.
func TestResolveScriptEntrypointHandlesShellFeaturesSafely(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		files      map[string][]byte
		dirs       map[string]struct{}
		executable map[string]struct{}
		want       string
	}{
		{name: "cd with no args",
			script: "#!/bin/sh\ncd\nexec /server\n",
			files:  map[string][]byte{"server": []byte("fake-elf-binary")},
			want:   "/server"},
		{name: "cd -",
			script: "#!/bin/sh\ncd -\nexec /server\n",
			files:  map[string][]byte{"server": []byte("fake-elf-binary")},
			want:   "/server"},
		{name: "flagged cd (cd -P)",
			script: "#!/bin/sh\ncd -P /app\nexec /app/server\n",
			files:  map[string][]byte{"app/server": []byte("fake-elf-binary")},
			want:   "/app/server"},
		{name: "empty directory visible to -d test",
			script: "#!/bin/sh\nif [ -d /data ]; then exec /server; else exec /missing; fi\n",
			files:  map[string][]byte{"server": []byte("fake-elf-binary")},
			dirs:   map[string]struct{}{"data": {}},
			want:   "/server"},
		{name: "executable bit answers [ -x path ]",
			script: "#!/bin/sh\nif [ -x /app/custom-server ]; then exec /app/custom-server; else exec /app/default-server; fi\n",
			files: map[string][]byte{
				"app/custom-server":  []byte("fake-elf-binary"),
				"app/default-server": []byte("fake-elf-binary"),
			},
			executable: map[string]struct{}{"app/custom-server": {}},
			want:       "/app/custom-server"},
		{name: "executable bit answers test -x path",
			script:     "#!/bin/sh\nif test -x /app/server; then exec /app/server; else exec /missing; fi\n",
			files:      map[string][]byte{"app/server": []byte("fake-elf-binary")},
			executable: map[string]struct{}{"app/server": {}},
			want:       "/app/server"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &analysisContext{
				EntrypointChain: []string{"/start.sh"},
				toolchain:       toolchain{Kind: toolchainShell},
				BinaryData:      []byte(tt.script),
				rootfs: &tarFSView{
					Paths:      filePathsOf(tt.files),
					Dirs:       tt.dirs,
					Executable: tt.executable,
					Open:       mapFileReader(tt.files),
				},
			}
			resolveScriptEntrypoint(ctx)
			if ctx.entrypoint() != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, ctx.entrypoint())
			}
		})
	}
}

func TestResolveScriptEntrypointFollowsExec(t *testing.T) {
	files := map[string][]byte{
		"usr/bin/redis-server": []byte("fake-elf-binary"),
	}
	ctx := newScriptContext("/entrypoint.sh", []byte("#!/bin/sh\nexec /usr/bin/redis-server /etc/redis/redis.conf\n"), nil, files)

	resolveScriptEntrypoint(ctx)

	if ctx.entrypoint() != "/usr/bin/redis-server" {
		t.Fatalf("expected entrypoint /usr/bin/redis-server, got %q", ctx.entrypoint())
	}
	if ctx.toolchain.Kind == toolchainShell {
		t.Fatalf("expected toolchain to no longer be shell, got %#v", ctx.toolchain)
	}
}

func TestResolveScriptEntrypointFollowsMultiHopChain(t *testing.T) {
	files := map[string][]byte{
		"usr/local/bin/docker-entrypoint.sh": []byte("#!/bin/sh\nexec \"$@\"\n"),
		"usr/sbin/mariadbd":                  []byte("fake-elf-binary"),
	}
	// Mirrors mariadb's wrapper.sh: no args forwarded, hardcodes the next hop.
	ctx := newScriptContext("/usr/local/bin/wrapper.sh", []byte("#!/bin/sh\n/usr/local/bin/docker-entrypoint.sh mariadbd\n"), nil, files)

	resolveScriptEntrypoint(ctx)

	if ctx.entrypoint() != "/usr/sbin/mariadbd" {
		t.Fatalf("expected entrypoint /usr/sbin/mariadbd, got %q", ctx.entrypoint())
	}
	if len(ctx.Notes) != 1 || !strings.Contains(ctx.Notes[0], "wrapper.sh") || !strings.Contains(ctx.Notes[0], "docker-entrypoint.sh") {
		t.Fatalf("expected a note describing the hop chain, got %#v", ctx.Notes)
	}
}

func TestResolveScriptEntrypointResolvesPositionalArgs(t *testing.T) {
	files := map[string][]byte{
		"usr/bin/myapp": []byte("fake-elf-binary"),
	}
	ctx := newScriptContext("/start.sh", []byte("#!/bin/sh\nexec \"$@\"\n"), []string{"myapp", "--flag"}, files)

	resolveScriptEntrypoint(ctx)

	if ctx.entrypoint() != "/usr/bin/myapp" {
		t.Fatalf("expected entrypoint /usr/bin/myapp, got %q", ctx.entrypoint())
	}
}

func TestResolveScriptEntrypointStopsOnCycle(t *testing.T) {
	files := map[string][]byte{
		"start.sh": []byte("#!/bin/sh\nexec /start.sh\n"),
	}
	ctx := newScriptContext("/start.sh", files["start.sh"], nil, files)

	resolveScriptEntrypoint(ctx)

	if ctx.toolchain.Kind != toolchainShell || ctx.entrypoint() != "/start.sh" {
		t.Fatalf("expected cycle to leave context unresolved, got entrypoint=%q toolchain=%#v", ctx.entrypoint(), ctx.toolchain)
	}
}

func TestResolveScriptEntrypointLeavesUnresolvedTargetAlone(t *testing.T) {
	ctx := newScriptContext("/start.sh", []byte("#!/bin/sh\nexec /does/not/exist\n"), nil, map[string][]byte{})

	resolveScriptEntrypoint(ctx)

	if ctx.toolchain.Kind != toolchainShell || ctx.entrypoint() != "/start.sh" {
		t.Fatalf("expected unresolved target to leave context unchanged, got entrypoint=%q toolchain=%#v", ctx.entrypoint(), ctx.toolchain)
	}
}

// TestResolveScriptEntrypointDoesNotLeakHostEnv proves the interpreter never
// falls back to the real process environment: a script branching on an env
// var set in the test process (but absent from ctx.Env) must take the
// "not set" branch.
func TestResolveScriptEntrypointDoesNotLeakHostEnv(t *testing.T) {
	t.Setenv("DATUM_TEST_HOST_LEAK", "1")

	files := map[string][]byte{
		"safe": []byte("fake-elf-binary"),
	}
	script := []byte("#!/bin/sh\nif [ -n \"$DATUM_TEST_HOST_LEAK\" ]; then exec /leaked; else exec /safe; fi\n")
	ctx := newScriptContext("/start.sh", script, nil, files)

	resolveScriptEntrypoint(ctx)

	if ctx.entrypoint() != "/safe" {
		t.Fatalf("expected the interpreter to see an empty env, resolved to %q instead", ctx.entrypoint())
	}
}

// TestResolveScriptEntrypointExposesRealBinaryToChecks confirms the point of
// this whole mechanism: once resolution reaches the real binary, the
// existing checks run against it instead of bailing on a nil ctx.ELF.
func TestResolveScriptEntrypointExposesRealBinaryToChecks(t *testing.T) {
	binary := buildLinuxGoBinary(t, "hopserver", "-buildmode=pie")
	files := map[string][]byte{"usr/bin/server": binary}
	ctx := newScriptContext("/entrypoint.sh", []byte("#!/bin/sh\nexec /usr/bin/server\n"), nil, files)

	resolveScriptEntrypoint(ctx)

	if ctx.entrypoint() != "/usr/bin/server" {
		t.Fatalf("expected entrypoint /usr/bin/server, got %q", ctx.entrypoint())
	}
	if ctx.ELF == nil {
		t.Fatal("expected the resolved binary's ELF to be parsed")
	}
	if interp := elfInterp(ctx.ELF); interp == "" {
		t.Skip("test toolchain produced no PT_INTERP")
	}

	if findings := checkMissingLibraries(ctx); len(findings) == 0 {
		t.Fatal("expected a missing-libraries finding against the real resolved binary, not the wrapper script")
	}
}

// TestResolveScriptEntrypointDoesNotTouchHostFilesystem proves stat/open are
// sandboxed against the analyzed rootfs: a script testing for a file that
// certainly exists on the host (but not in FilePaths) must take the
// "not found" branch, and any write must never land on real disk.
func TestResolveScriptEntrypointDoesNotTouchHostFilesystem(t *testing.T) {
	marker := t.TempDir() + "/should-not-be-created"
	files := map[string][]byte{
		"safe": []byte("fake-elf-binary"),
	}
	script := []byte(fmt.Sprintf("#!/bin/sh\necho hi > %s\nif [ -f /etc/passwd ]; then exec /found-real-etc-passwd; else exec /safe; fi\n", marker))
	ctx := newScriptContext("/start.sh", script, nil, files)

	resolveScriptEntrypoint(ctx)

	if ctx.entrypoint() != "/safe" {
		t.Fatalf("expected /etc/passwd lookup to miss the sandboxed filesystem, resolved to %q instead", ctx.entrypoint())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("expected redirect to be a no-op, but %s exists (err=%v)", marker, err)
	}
}
