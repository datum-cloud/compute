package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// kraftfileNames are the filenames the unikraft CLI recognizes as a
// Kraftfile, in the order it checks them.
var kraftfileNames = []string{
	"kraft.yaml",
	"kraft.yml",
	"Kraftfile.yml",
	"Kraftfile.yaml",
	"Kraftfile",
}

// FindKraftfile returns the path of the first Kraftfile found in dir, or ""
// if none exist.
func FindKraftfile(dir string) string {
	for _, name := range kraftfileNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// runKraftBuild delegates the entire build to unikraft's own CLI
// (https://github.com/unikraft/cli) when a Kraftfile is present, instead of
// reimplementing Kraftfile semantics (rootfs source/format, cmd, ...) here.
//
// unikraft build takes an input directory, not an explicit Kraftfile path —
// it auto-discovers one of kraftfileNames within it — so opts.Kraftfile must
// live directly in that directory under one of those names.
func runKraftBuild(ctx context.Context, opts *Options) error {
	if !slices.Contains(kraftfileNames, filepath.Base(opts.Kraftfile)) {
		return fmt.Errorf(
			"--kraftfile %s has a name the unikraft CLI won't auto-discover; rename it to one of %v",
			displayPath(opts.Kraftfile), kraftfileNames,
		)
	}
	inputDir, err := filepath.Abs(filepath.Dir(opts.Kraftfile))
	if err != nil {
		return fmt.Errorf("resolving Kraftfile directory: %w", err)
	}

	unikraftPath, err := exec.LookPath("unikraft")
	if err != nil {
		return fmt.Errorf(
			"found a Kraftfile at %s, but the unikraft CLI is not installed; install it from https://github.com/unikraft/cli and re-run, or remove the Kraftfile to use the default Dockerfile-based build",
			displayPath(opts.Kraftfile),
		)
	}

	args := []string{"build", inputDir} //nolint:goconst
	for _, arg := range opts.BuildArgs {
		args = append(args, "--build-arg", arg)
	}
	if opts.Output != "" {
		args = append(args, "--output", opts.Output)
	}

	fmt.Fprintf(os.Stderr, "Kraftfile found at %s: this build is entirely delegated to the unikraft CLI, not datumctl.\n", displayPath(opts.Kraftfile))
	fmt.Fprintf(os.Stderr, "Running: %s\n", formatCommand(unikraftPath, args))

	cmd := exec.CommandContext(ctx, unikraftPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("unikraft build: %w", err)
	}
	return nil
}

// formatCommand renders path and args as a copy-pasteable shell command,
// quoting only the arguments that actually need it for readability.
func formatCommand(path string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, path)
	for _, arg := range args {
		parts = append(parts, shellQuoteIfNeeded(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuoteIfNeeded(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`*?[]{}()<>|&;~") {
		return s
	}
	return shellQuote(s)
}
