package build

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

const (
	maxScriptHops      = 8
	scriptTraceTimeout = 2 * time.Second
)

// startupCommand is a resolved entrypoint: the binary or script to analyze,
// the positional arguments it receives, and any shells needed just to
// dispatch it (e.g. /bin/sh for a shell-form CMD). Shells is only populated
// by resolveStartupArgs; traceScriptExecTarget leaves it empty.
type startupCommand struct {
	Entrypoint string
	Args       []string
	Shells     []string
}

// resolveScriptEntrypointPass traces a shell-script entrypoint to the real
// payload binary it ultimately execs (directly, or through further scripts),
// so architecture/library checks run against the actual application instead
// of stopping at a wrapper script that only sets up the environment.
var resolveScriptEntrypointPass = analysisPass{
	ID:          "resolve-script-entrypoint",
	Description: "tracing startup script",
	Run: func(ctx *analysisContext) ([]finding, error) {
		resolveScriptEntrypoint(ctx)
		return nil, nil
	},
}

// resolveScriptEntrypoint requires ctx.EntrypointChain to already be seeded
// (analyzeRootFSView does this at construction) with at least the current
// entrypoint; it only ever appends further hops onto it.
func resolveScriptEntrypoint(ctx *analysisContext) {
	if ctx.toolchain.Kind != toolchainShell || ctx.rootfs == nil {
		return
	}

	scriptData, args := ctx.BinaryData, ctx.Args

	for range maxScriptHops {
		cmd, ok := traceScriptExecTarget(scriptData, args, ctx.Env, ctx.rootfs)
		if !ok {
			return
		}
		resolved := resolveEntrypointPathWithDirs(cmd.Entrypoint, ctx.rootfs.Paths, envPathDirs(ctx.Env))
		if resolved == "" || slices.Contains(ctx.EntrypointChain, resolved) {
			return
		}
		data, err := ctx.rootfs.Open(strings.TrimPrefix(resolved, "/"))
		if err != nil {
			return
		}
		ctx.EntrypointChain = append(ctx.EntrypointChain, resolved)

		tc, elf := detectToolchain(data)
		if tc.Kind != toolchainShell {
			ctx.BinaryData = data
			ctx.ELF = elf
			ctx.toolchain = tc
			if len(ctx.EntrypointChain) > 1 {
				ctx.Notes = append(ctx.Notes, "resolved startup script through "+strings.Join(ctx.EntrypointChain, " -> "))
			}
			return
		}

		scriptData, args = data, cmd.Args
	}
}

// resolveStartupArgs peels wrapper layers and inline `sh -c` lines from a
// startup command, tracing inline shell with the same sandboxed interpreter
// resolveScriptEntrypoint uses instead of naive word-splitting. rootfs may be
// nil (not yet available). shells excludes our own workdir wrapper, since
// findShell already guarantees its shell exists.
func resolveStartupArgs(args []string, env []string, rootfs *tarFSView) startupCommand {
	var shells []string
	for range maxScriptHops {
		if rest, ok := unwrapWorkdirWrapperArgs(args); ok {
			args = rest
			continue
		}
		switch {
		case len(args) >= 3 && isShell(args[0]) && args[1] == "-c":
			shells = append(shells, args[0])
			cmd, ok := traceScriptExecTarget([]byte(args[2]), args[3:], env, rootfs)
			if !ok {
				return startupCommand{Entrypoint: args[2], Args: args[3:], Shells: shells}
			}
			args = append([]string{cmd.Entrypoint}, cmd.Args...)
		case len(args) >= 2 && isShell(args[0]):
			shells = append(shells, args[0])
			args = args[1:]
		default:
			ep, rest := firstExecutableWord(args)
			return startupCommand{Entrypoint: ep, Args: rest, Shells: shells}
		}
	}
	ep, rest := firstExecutableWord(args)
	return startupCommand{Entrypoint: ep, Args: rest, Shells: shells}
}

// traceScriptExecTarget interprets script — with args as positional
// parameters and env as the environment — far enough to find the last
// external command it would run: the one it hands off to via exec, or
// otherwise the last one reached before the script ends. It never executes
// anything for real; exec/open/stat/readdir are all sandboxed against rootfs
// instead of the host.
func traceScriptExecTarget(script []byte, args, env []string, rootfs *tarFSView) (startupCommand, bool) {
	file, err := syntax.NewParser().Parse(bytes.NewReader(script), "")
	if err != nil {
		return startupCommand{}, false
	}

	var calls [][]string
	vfs := newVirtualFS(rootfs)
	var runner *interp.Runner
	runner, err = interp.New(
		interp.Params(append([]string{"--"}, args...)...),
		interp.Env(expand.ListEnviron(env...)),
		interp.Dir("/"),
		interp.StdIO(bytes.NewReader(nil), io.Discard, io.Discard),
		// cd and the "-x"/"-r"/"-w" file tests all call a real, unsandboxable
		// unix.Access syscall in mvdan/sh (no pluggable handler as of
		// v3.13.1): cd would always "fail" against the analyzing host and
		// abort the trace under `set -e`; "-x" would always evaluate false
		// regardless of the image's real bit. Intercept both: cd's "<path>"
		// form updates runner.Dir ourselves (other forms — no args, "-",
		// flags — leave it unchanged rather than guess wrong); "-x" is
		// answered from rootfs.Executable. Bash's "[[ -x ]]" form isn't a
		// CallExpr and can't be intercepted this way — accepted gap.
		interp.CallHandler(func(_ context.Context, callArgs []string) ([]string, error) {
			if len(callArgs) > 0 && callArgs[0] == "cd" {
				if args := callArgs[1:]; len(args) == 1 && args[0] != "-" {
					runner.Dir = resolveAgainstDir(runner.Dir, args[0])
				}
				return []string{":"}, nil
			}
			if target, ok := matchExecutableTest(callArgs); ok {
				return []string{boolWord(vfs.isExecutable(resolveAgainstDir(runner.Dir, target)))}, nil
			}
			return callArgs, nil
		}),
		// Never calls next: the chain's terminal handler is
		// interp.DefaultExecHandler, which really forks and runs the
		// command, so every call must be swallowed here.
		interp.ExecHandlers(func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, callArgs []string) error {
				if len(callArgs) > 0 {
					call := slices.Clone(callArgs)
					// Relative *path* references ("./server") resolve against
					// cwd; bare names ("node") are left for the caller to
					// resolve via PATH instead.
					if !path.IsAbs(call[0]) && strings.Contains(call[0], "/") {
						call[0] = path.Join(interp.HandlerCtx(ctx).Dir, call[0])
					}
					calls = append(calls, call)
				}
				return nil
			}
		}),
		interp.OpenHandler(vfs.open),
		interp.StatHandler(vfs.stat),
		interp.ReadDirHandler2(vfs.readDir),
	)
	if err != nil {
		return startupCommand{}, false
	}

	traceCtx, cancel := context.WithTimeout(context.Background(), scriptTraceTimeout)
	defer cancel()
	_ = runner.Run(traceCtx, file) // exit status is irrelevant; we only care which commands ran

	if len(calls) == 0 {
		return startupCommand{}, false
	}
	last := calls[len(calls)-1]
	return startupCommand{Entrypoint: last[0], Args: last[1:]}, true
}

// resolveAgainstDir resolves target against dir the way a shell resolves any
// path argument: absolute paths pass through cleaned, relative ones join dir.
func resolveAgainstDir(dir, target string) string {
	if path.IsAbs(target) {
		return path.Clean(target)
	}
	return path.Join(dir, target)
}

// matchExecutableTest recognizes a POSIX "-x" file test, in either its
// "[ -x path ]" or "test -x path" form, and returns the tested path.
func matchExecutableTest(callArgs []string) (target string, ok bool) {
	switch {
	case len(callArgs) == 4 && callArgs[0] == "[" && callArgs[1] == "-x" && callArgs[3] == "]":
		return callArgs[2], true
	case len(callArgs) == 3 && callArgs[0] == "test" && callArgs[1] == "-x":
		return callArgs[2], true
	}
	return "", false
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// virtualFS backs the interpreter's file-access handlers with the analyzed
// rootfs instead of the host filesystem: exec/open/stat/readdir must never
// touch real host state, since scripts here come from untrusted Dockerfiles.
type virtualFS struct {
	rootfs *tarFSView
	dirs   map[string]struct{}
}

func newVirtualFS(rootfs *tarFSView) *virtualFS {
	var paths, explicitDirs map[string]struct{}
	if rootfs != nil {
		paths, explicitDirs = rootfs.Paths, rootfs.Dirs
	}
	return &virtualFS{rootfs: rootfs, dirs: impliedDirs(paths, explicitDirs)}
}

// impliedDirs unions explicit tar directory entries (real, possibly empty)
// with directories inferred from file paths, since paths only records
// regular files and symlinks — a file's ancestors aren't otherwise implied.
func impliedDirs(paths map[string]struct{}, explicitDirs map[string]struct{}) map[string]struct{} {
	dirs := map[string]struct{}{"": {}}
	for d := range explicitDirs {
		dirs[vfsNormalize(d)] = struct{}{}
	}
	for p := range paths {
		for {
			i := strings.LastIndexByte(p, '/')
			if i < 0 {
				break
			}
			p = p[:i]
			if _, ok := dirs[p]; ok {
				break
			}
			dirs[p] = struct{}{}
		}
	}
	return dirs
}

func vfsNormalize(p string) string {
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

func (v *virtualFS) isExecutable(p string) bool {
	return v.rootfs.IsExecutable(vfsNormalize(p))
}

func (v *virtualFS) stat(_ context.Context, p string, _ bool) (fs.FileInfo, error) {
	np := vfsNormalize(p)
	if _, ok := v.dirs[np]; ok {
		return virtualFileInfo{name: np, isDir: true}, nil
	}
	if v.rootfs.HasPath(np) {
		return virtualFileInfo{name: np, executable: v.rootfs.IsExecutable(np)}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
}

func (v *virtualFS) open(_ context.Context, p string, flag int, _ os.FileMode) (io.ReadWriteCloser, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
		return discardReadWriteCloser{}, nil
	}
	if v.rootfs == nil || v.rootfs.Open == nil {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	data, err := v.rootfs.Open(vfsNormalize(p))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return readOnlyFile{Reader: bytes.NewReader(data)}, nil
}

// readDir treats every directory as empty rather than reading the host
// filesystem. None of the scripts this is meant to trace rely on glob
// expansion to build their exec target, so this is a deliberate scope cut.
func (v *virtualFS) readDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, nil
}

type virtualFileInfo struct {
	name       string
	isDir      bool
	executable bool
}

func (v virtualFileInfo) Name() string { return path.Base("/" + v.name) }
func (v virtualFileInfo) Size() int64  { return 0 }
func (v virtualFileInfo) Mode() fs.FileMode {
	if v.isDir {
		return fs.ModeDir | 0o755
	}
	if v.executable {
		return 0o755
	}
	return 0o644
}
func (v virtualFileInfo) ModTime() time.Time { return time.Time{} }
func (v virtualFileInfo) IsDir() bool        { return v.isDir }
func (v virtualFileInfo) Sys() any           { return nil }

type discardReadWriteCloser struct{}

func (discardReadWriteCloser) Read([]byte) (int, error)    { return 0, io.EOF }
func (discardReadWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardReadWriteCloser) Close() error                { return nil }

type readOnlyFile struct{ *bytes.Reader }

func (readOnlyFile) Write([]byte) (int, error) { return 0, fs.ErrPermission }
func (readOnlyFile) Close() error              { return nil }
