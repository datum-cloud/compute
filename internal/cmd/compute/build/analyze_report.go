package build

import (
	"fmt"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/fatih/color"
)

var (
	cError   = color.New(color.FgRed, color.Bold)
	cGutter  = color.New(color.FgBlue, color.Bold)
	cSuccess = color.New(color.FgGreen, color.Bold)
)

func printAnalysisResult(r *analysisResult) {
	fmt.Fprintf(os.Stderr, "Analyzing %s  (%s)\n\n", r.Entrypoint, toolchainLabel(r.toolchain))
	printAnalysisNotes(r)

	if r.OK() {
		cSuccess.Fprintln(os.Stderr, "✓ No compatibility issues found")
		return
	}

	for _, f := range r.Findings {
		printFinding(f)
	}
	if hasExactLineFixes(r) {
		fmt.Fprintln(os.Stderr, "tip: rerun with --fix to apply the suggested Dockerfile edits automatically")
		fmt.Fprintln(os.Stderr)
	}

	n := len(r.Findings)
	if n == 1 {
		fmt.Fprintf(os.Stderr, "%s: aborting due to previous error\n", cError.Sprint("error"))
	} else {
		fmt.Fprintf(os.Stderr, "%s: aborting due to %d previous errors\n", cError.Sprint("error"), n)
	}
}

func hasExactLineFixes(result *analysisResult) bool {
	return slices.ContainsFunc(result.Findings, func(f finding) bool {
		return slices.ContainsFunc(f.Edits, isExactLineEdit)
	})
}

func printAnalysisNotes(r *analysisResult) {
	for _, note := range r.Notes {
		fmt.Fprintf(os.Stderr, "note: %s\n", note)
	}
	if len(r.Notes) > 0 {
		fmt.Fprintln(os.Stderr)
	}
}

func printFinding(f finding) {
	errWord := cError.Sprint("error")
	checkTag := ""
	if f.check != "" {
		checkTag = color.New(color.Bold).Sprintf("[%s]", f.check)
	}
	if f.Message != "" {
		fmt.Fprintf(os.Stderr, "%s%s: %s\n", errWord, checkTag, f.Message)
	} else {
		fmt.Fprintf(os.Stderr, "%s%s:\n", errWord, checkTag)
	}

	if len(f.Edits) > 0 {
		for _, edit := range f.Edits {
			printEdit(edit)
		}
	}
	help := f.Help
	if help != "" && len(f.Edits) == 0 {
		printHelp(help)
	}
	fmt.Fprintln(os.Stderr)
}

func printEdit(edit sourceEdit) {
	line := edit.Line
	if line <= 0 {
		line = 1
	}
	lineNumWidth := len(fmt.Sprintf("%d", line))
	pad := strings.Repeat(" ", lineNumWidth)
	pipe := cGutter.Sprint("|")
	arrow := cGutter.Sprint("-->")
	fmt.Fprintf(os.Stderr, "%s%s %s:%d\n", pad, arrow, displayPath(edit.File), line)
	if edit.Description != "" {
		fmt.Fprintf(os.Stderr, "%s %s %s\n", pad, pipe, edit.Description)
	}
	for _, line := range strings.Split(edit.Old, "\n") {
		fmt.Fprintf(os.Stderr, "%s %s %s\n", pad, pipe, cError.Sprint("- "+line))
	}
	for _, line := range strings.Split(edit.New, "\n") {
		fmt.Fprintf(os.Stderr, "%s %s %s\n", pad, pipe, cSuccess.Sprint("+ "+line))
	}
}

func printHelp(help string) {
	lines := strings.Split(help, "\n")
	marker := color.New(color.Bold).Sprint("=")
	for i, line := range lines {
		if i == 0 {
			fmt.Fprintf(os.Stderr, "  %s help: %s\n", marker, line)
			continue
		}
		fmt.Fprintf(os.Stderr, "          %s\n", line)
	}
}

func toolchainLabel(tc toolchain) string {
	switch tc.Kind {
	case toolchainGo:
		if tc.Version != "" {
			return tc.Version
		}
		return "Go"
	case toolchainNative:
		return "native binary"
	case toolchainShell:
		return "shell script"
	default:
		return "unknown toolchain"
	}
}

func buildAnalysisResultFromView(opts *Options, view *tarFSView, args []string, env []string, extraPasses []analysisPass, progress statusFunc) (*analysisResult, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("analysis failed: no entrypoint found in root filesystem")
	}
	cmd := resolveStartupArgs(args, env, view)
	// Checked on the resolved entrypoint, not the raw args[0]: PATH search,
	// wrapper-name skipping, and our own WORKDIR-wrapper unwrapping may all
	// have already turned a relative name into an absolute one by this point.
	if isRelativeEntrypointScript(cmd.Entrypoint) {
		return nil, fmt.Errorf(
			"analysis failed: image starts through relative script %q; use an absolute script path that is copied into the final image, or start the app directly with an absolute CMD such as [\"/usr/bin/node\", \"/app/server.mjs\"]",
			cmd.Entrypoint,
		)
	}

	analysisProgressFn := normalizeProgress(progress)
	if opts.Verbose {
		analysisProgressFn = analysisProgress
	}
	result, err := analyzeRootFSView(analysisRequest{
		RootFS:         view,
		Entrypoint:     cmd.Entrypoint,
		Args:           cmd.Args,
		RequiredShells: cmd.Shells,
		Env:            env,
		DockerfilePath: opts.Dockerfile,
		ExtraPasses:    extraPasses,
		Progress:       analysisProgressFn,
	})
	if err != nil {
		return nil, fmt.Errorf("analysis failed: %w", err)
	}
	return result, nil
}

func analysisProgress(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func isAnalysisEntrypointWrapper(arg string) bool {
	base := path.Base(arg)
	return isInheritedEntrypointWrapper(arg) || base == "entrypoint.sh" || base == "wrapper.sh"
}

func isInheritedEntrypointWrapper(arg string) bool {
	base := path.Base(arg)
	return base == "docker-entrypoint.sh" || base == "container-entrypoint.sh"
}

func isRelativeEntrypointScript(arg string) bool {
	if strings.HasPrefix(arg, "/") || strings.Contains(arg, "/") {
		return false
	}
	base := path.Base(arg)
	return strings.HasSuffix(base, ".sh") || strings.Contains(base, "entrypoint")
}
