package build

import (
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

type check func(*checkContext) []finding

type analysisPass struct {
	ID          string
	Description string
	Describe    func(*analysisContext) string
	Run         func(*analysisContext) ([]finding, error)
}

type statusFunc func(string, ...any)

type checkDefinition struct {
	ID          string
	Description string
	Run         check
}

type toolchainKind int

const (
	toolchainUnknown toolchainKind = iota
	toolchainGo
	toolchainNative
	toolchainShell
)

type toolchain struct {
	Kind    toolchainKind
	Version string // e.g. "go1.22.0"; empty if unknown
}

// analysisContext is the shared state accumulated by analysis passes.
type analysisContext struct {
	rootfs    *tarFSView
	toolchain toolchain
	status    statusFunc

	// Args are the arguments following the resolved entrypoint (e.g. the
	// rest of a Kraftfile/Docker CMD array); used as positional parameters
	// when tracing a script entrypoint to the binary it execs.
	Args []string
	Env  []string

	// RequiredShells are shell binaries the startup command needs present
	// just to dispatch (e.g. /bin/sh for a shell-form CMD), separate from
	// Entrypoint since that's whatever the shell ultimately execs.
	RequiredShells []string

	// EntrypointChain is every path exec'd to reach entrypoint() (see
	// below): the declared entrypoint plus each script hop
	// resolveScriptEntrypoint traced. Seeded in analyzeRootFSView.
	EntrypointChain []string

	// ELF is nil if the entrypoint is not ELF (e.g. a shell script).
	ELF *elf.File
	// BinaryData is the current entrypoint's raw bytes, handed from
	// scanTarFSViewEntrypointPass to detectEntrypointPass
	BinaryData []byte

	// RuntimeFileView lazily resolves a view of the stage that produced the
	// entrypoint (expensive: rebuilds that stage), memoized internally by the
	// closure itself. Nil if no such stage is known.
	RuntimeFileView func() (*tarFSView, error)

	// Dockerfile AST; nil if no Dockerfile path was provided.
	Dockerfile     *dockerfileDoc
	DockerfilePath string

	Notes []string
}

// checkContext is kept as an alias for existing check implementations.
type checkContext = analysisContext

// entrypoint is the path to analyze right now: the last element of
// EntrypointChain, i.e. either the originally declared entrypoint or, once
// resolveScriptEntrypoint has traced through it, the real payload binary/
// script it ultimately execs.
func (ctx *analysisContext) entrypoint() string {
	return ctx.EntrypointChain[len(ctx.EntrypointChain)-1]
}

// entrypointProvenance resolves, on demand, the Dockerfile COPY/ADD
// instruction that placed the current entrypoint and the stage that produced
// it. Returns zero values if no Dockerfile was parsed.
func (ctx *analysisContext) entrypointProvenance() (entrypointCopy *parser.Node, sourceStage string) {
	if ctx.Dockerfile == nil {
		return nil, ""
	}
	entrypointCopy = ctx.Dockerfile.findEntrypointCopy(ctx.entrypoint())
	_, sourceStage = ctx.Dockerfile.findEntrypointProducer(ctx.entrypoint(), ctx.toolchain)
	return entrypointCopy, sourceStage
}

// finding is a single compatibility issue, formatted as a compiler diagnostic.
type finding struct {
	// Source location in the Dockerfile, if the offending instruction was traced.
	File string // Dockerfile path; empty if location is unknown
	Line int    // 1-based line number; 0 if unknown

	check   string // check name, e.g. "missing-libraries"
	Message string // full diagnostic; shown in the header when no inline annotation

	// Help is prose guidance when there is no safe source edit.
	Help string
	// Edits are source changes the user can apply to fix the finding.
	Edits []sourceEdit
}

type sourceEdit struct {
	File        string
	Line        int
	Old         string
	New         string
	Description string
}

// analysisResult holds all findings from an image analysis run.
type analysisResult struct {
	Entrypoint string
	toolchain  toolchain
	Findings   []finding
	Notes      []string
}

func (r *analysisResult) OK() bool { return len(r.Findings) == 0 }

// analysisRequest is everything analyzeRootFSView needs to run: the rootfs
// to analyze, the startup command that was resolved against it, and the
// checks/passes to run.
type analysisRequest struct {
	RootFS         *tarFSView
	Entrypoint     string
	Args           []string
	RequiredShells []string
	Env            []string
	DockerfilePath string
	ExtraPasses    []analysisPass
	Progress       statusFunc
}

func analyzeRootFSView(req analysisRequest) (*analysisResult, error) {
	ctx := &analysisContext{
		rootfs:          req.RootFS,
		EntrypointChain: []string{req.Entrypoint},
		DockerfilePath:  req.DockerfilePath,
		Env:             req.Env,
		Args:            req.Args,
		RequiredShells:  req.RequiredShells,
	}
	passes := slices.Concat(
		[]analysisPass{scanTarFSViewEntrypointPass, detectEntrypointPass, resolveScriptEntrypointPass, readDockerfileHintsPass},
		req.ExtraPasses,
		compatibilityPasses(defaultChecks),
	)
	return runAnalysisPasses(ctx, passes, req.Progress)
}

func noopProgress(string, ...any) {}

func normalizeProgress(progress statusFunc) statusFunc {
	if progress == nil {
		return noopProgress
	}
	return progress
}

func runChecks(ctx *analysisContext, checks []checkDefinition, progress statusFunc) (*analysisResult, error) {
	return runAnalysisPasses(ctx, compatibilityPasses(checks), progress)
}

func runAnalysisPasses(ctx *analysisContext, passes []analysisPass, progress statusFunc) (*analysisResult, error) {
	progress = normalizeProgress(progress)
	ctx.status = progress
	result := &analysisResult{}
	for _, pass := range passes {
		label := pass.Description
		if pass.Describe != nil {
			label = pass.Describe(ctx)
		}
		if label != "" {
			progress("%s", label)
		}
		findings, err := pass.Run(ctx)
		if err != nil {
			return nil, err
		}
		result.Findings = append(result.Findings, findings...)
	}
	result.Entrypoint = ctx.entrypoint()
	result.toolchain = ctx.toolchain
	result.Notes = ctx.Notes
	return result, nil
}

var scanTarFSViewEntrypointPass = analysisPass{ID: "find-entrypoint", Description: "finding entrypoint", Describe: func(ctx *analysisContext) string {
	return fmt.Sprintf("finding entrypoint %s", ctx.entrypoint())
}, Run: func(ctx *analysisContext) ([]finding, error) {
	binaryData, err := scanTarFSView(ctx.rootfs, ctx.entrypoint())
	if err != nil {
		return nil, fmt.Errorf("scanning root filesystem: %w", err)
	}
	if binaryData == nil {
		return nil, fmt.Errorf("entrypoint %q not found in root filesystem", ctx.entrypoint())
	}
	ctx.BinaryData = binaryData
	return nil, nil
}}

var detectEntrypointPass = analysisPass{ID: "detect-entrypoint", Description: "detecting entrypoint type", Run: func(ctx *analysisContext) ([]finding, error) {
	ctx.toolchain, ctx.ELF = detectToolchain(ctx.BinaryData)
	return nil, nil
}}

var readDockerfileHintsPass = analysisPass{ID: "read-dockerfile-hints", Description: "reading Dockerfile hints", Run: func(ctx *analysisContext) ([]finding, error) {
	if ctx.DockerfilePath == "" {
		return nil, nil
	}
	df, err := parseDockerfileAST(ctx.DockerfilePath)
	if err != nil {
		ctx.Notes = append(ctx.Notes, fmt.Sprintf("could not parse Dockerfile for source hints: %v", err))
		return nil, nil
	}
	ctx.Dockerfile = df
	return nil, nil
}}

func compatibilityPasses(checks []checkDefinition) []analysisPass {
	passes := make([]analysisPass, 0, len(checks)+1)
	passes = append(passes, analysisPass{
		ID:          "check-compatibility",
		Description: "checking compatibility",
		Run:         func(*analysisContext) ([]finding, error) { return nil, nil }},
	)
	for _, check := range checks {
		passes = append(passes, analysisPass{
			ID:          check.ID,
			Description: "running " + check.Description + " check",
			Run: func(ctx *analysisContext) ([]finding, error) {
				return check.Run(ctx), nil
			},
		})
	}
	return passes
}

// firstExecutableWord scans words for the first one that looks like a real
// command, skipping leading env assignments, "exec"/"command", entrypoint
// wrapper names (when followed by another word), and env(1) dispatch. It
// returns the remaining words after the one it picked.
func firstExecutableWord(words []string) (string, []string) {
	for i := 0; i < len(words); i++ {
		word := strings.Trim(words[i], " ;&|")
		switch {
		case word == "", isEnvAssignment(word):
			continue
		case word == "exec" || word == "command":
			continue
		case isAnalysisEntrypointWrapper(word) && i+1 < len(words):
			continue
		case isEnvDispatchPath(word):
			i = skipEnvArgs(words, i)
			continue
		default:
			return word, words[i+1:]
		}
	}
	return "", nil
}

var posixShellNames = []string{"sh", "bash", "dash", "ash", "zsh", "ksh", "mksh"}

func isShell(arg string) bool {
	return slices.Contains(posixShellNames, path.Base(arg))
}

func isEnvAssignment(word string) bool {
	i := strings.IndexByte(word, '=')
	return i > 0 && !strings.Contains(word[:i], "/")
}

// isEnvDispatchPath reports whether word looks like the standard env(1)
// dispatcher (as in #!/usr/bin/env bash) rather than a user binary that
// happens to be named "env". The #!/usr/bin/env convention only works
// portably because env lives somewhere every script on the system can find
// it, so a binary named "env" outside those canonical locations is treated
// as a literal executable instead of chased for a "real" target.
func isEnvDispatchPath(word string) bool {
	if path.Base(word) != "env" {
		return false
	}
	switch dir := strings.TrimSuffix(strings.TrimSuffix(word, "env"), "/"); dir {
	case "", "/bin", "/usr/bin", "/usr/local/bin", "/sbin", "/usr/sbin":
		return true
	default:
		return false
	}
}

func skipEnvArgs(words []string, envIndex int) int {
	for i := envIndex + 1; i < len(words); i++ {
		word := strings.Trim(words[i], " ;&|")
		if word == "" || isEnvAssignment(word) {
			continue
		}
		if word == "-i" || word == "-0" || word == "--ignore-environment" || word == "--null" {
			continue
		}
		if (word == "-u" || word == "--unset" || word == "-C" || word == "--chdir") && i+1 < len(words) {
			i++
			continue
		}
		return i - 1
	}
	return len(words) - 1
}

func detectToolchain(data []byte) (toolchain, *elf.File) {
	if bytes.HasPrefix(data, []byte("#!")) {
		return toolchain{Kind: toolchainShell}, nil
	}

	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return toolchain{Kind: toolchainUnknown}, nil
	}

	if bi, err := buildinfo.Read(bytes.NewReader(data)); err == nil && bi.GoVersion != "" {
		return toolchain{Kind: toolchainGo, Version: bi.GoVersion}, f
	}

	return toolchain{Kind: toolchainNative}, f
}
