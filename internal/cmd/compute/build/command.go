package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

type options struct {
	analyze            bool
	buildArgs          []string
	buildTarget        string
	contextDir         string
	dockerfile         string
	dockerfileExplicit bool
	fix                bool
	kraftfile          string
	lastBuildLog       string
	output             string
	push               bool
	quietBuild         bool
	ref                string
	tmpDir             string
	verbose            bool
}

func Command() *cobra.Command {
	opts := &options{}

	cmd := &cobra.Command{
		Use:     "build [OPTIONS] [CONTEXT]",
		Short:   "Build and publish Compute OCI images",
		Aliases: []string{"b"},
		Args:    cobra.MaximumNArgs(1),
		Long: `Build a Dockerfile into an image that can run on Datum Compute.

Start with the same Dockerfile and build context you use for a normal container.
By default, the command performs a local debug build so you can check whether the
project packages successfully before deciding where to write or publish it.

Use --output to write an OCI archive, write an OCI layout directory, or publish
to a registry reference. Registry-looking outputs ask for confirmation before
pushing; pass --push to skip that confirmation in CI.

For most projects, the default workflow is just a Dockerfile and a build context:

	  datumctl compute build .

By default, build uses Dockerfile.datum from the build context when present,
falling back to Dockerfile. Use -f when your Dockerfile lives somewhere else,
and --build-arg to pass values used by Dockerfile ARG instructions.

For multi-stage Dockerfiles, build packages the final stage by default. Use
--target when you want to package a specific stage, such as production.

The optional --analyze flag checks the built entrypoint before packaging. It can
catch common issues early, such as a binary built for the wrong architecture or
shared libraries that were not copied into the final image.

The optional --fix flag applies safe exact-line fixes to the selected Dockerfile,
then rebuilds from that file.

Advanced users can provide a Kraftfile with --kraftfile (or by placing one in
the build context) to delegate the entire build to the unikraft CLI instead,
which must be installed separately. Most projects do not need one.`,
		Example: `
# Check that the current Dockerfile builds for Compute
datumctl compute build .

# Publish to a registry
datumctl compute build --push --output ghcr.io/acme/api:latest .

# Write an OCI archive
datumctl compute build --output ./compute-image.tar .

# Write an OCI layout directory
datumctl compute build --output ./compute-image .

# Use a Dockerfile in another location
datumctl compute build -f ./deploy/Dockerfile .

# Pass Docker build arguments
datumctl compute build --build-arg VERSION=1.2.3 .

# Package a specific Dockerfile stage
datumctl compute build --target production .

# Check the built app before packaging it
datumctl compute build --analyze .

# Advanced: delegate to unikraft build using an existing Kraftfile
datumctl compute build --kraftfile ./Kraftfile .
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.contextDir = args[0]
			}
			opts.dockerfileExplicit = cmd.Flags().Changed("file")
			return run(cmd.Context(), opts)
		},
	}

	cmd.Flags().BoolVar(&opts.analyze, "analyze", false, "Analyze the Dockerfile output for Compute compatibility before packaging")
	cmd.Flags().StringArrayVar(&opts.buildArgs, "build-arg", nil, "Set build-time variables (KEY=VALUE or KEY to inherit from env)")
	cmd.Flags().StringVar(&opts.buildTarget, "target", "", "Dockerfile stage to build")
	cmd.Flags().StringVarP(&opts.dockerfile, "file", "f", "Dockerfile", "Path to Dockerfile")
	cmd.Flags().BoolVar(&opts.fix, "fix", false, "Apply safe exact-line fixes to the selected Dockerfile and rebuild (implies --analyze)")
	cmd.Flags().StringVar(&opts.kraftfile, "kraftfile", "", "Advanced: delegate the build to the unikraft CLI using this Kraftfile")
	cmd.Flags().StringVarP(&opts.output, "output", "o", "", "Output destination: registry ref, .tar OCI archive, or local OCI layout directory")
	cmd.Flags().BoolVar(&opts.push, "push", false, "Push registry output without confirmation")
	cmd.Flags().BoolVarP(&opts.verbose, "verbose", "v", false, "Show detailed analysis progress instead of a spinner")

	cmd.AddCommand(inspectCommand())

	return cmd
}

func printBuildConfig(opts *options) {
	row := func(label, value string) {
		fmt.Fprintf(os.Stderr, "  %-11s %s\n", label+":", value)
	}

	if opts.output != "" {
		fmt.Fprintf(os.Stderr, "Build %s\n", opts.ref)
	} else {
		fmt.Fprintln(os.Stderr, "Build preview")
	}

	row("Context", displayPath(opts.contextDir))
	dockerfileLabel := displayPath(opts.dockerfile)
	if !opts.dockerfileExplicit && filepath.Base(opts.dockerfile) == "Dockerfile.datum" {
		dockerfileLabel += " (override)"
	}
	row("Dockerfile", dockerfileLabel)
	if opts.buildTarget != "" {
		row("Target", opts.buildTarget)
	}
	if opts.output == "" {
		row("Output", "preview only")
	} else {
		row("Output", opts.output)
	}
	fmt.Fprintln(os.Stderr)
}

func displayPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	cwd, err := os.Getwd()
	if err != nil {
		return abs
	}
	rel, err := filepath.Rel(cwd, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return abs
	}
	return rel
}

func resolveDockerfilePath(contextDir, dockerfile string, explicit bool) (string, error) {
	if explicit {
		if filepath.IsAbs(dockerfile) {
			return dockerfile, nil
		}
		return filepath.Join(contextDir, dockerfile), nil
	}
	datumDockerfile := filepath.Join(contextDir, "Dockerfile.datum")
	if _, err := os.Stat(datumDockerfile); err == nil {
		return datumDockerfile, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("checking Dockerfile.datum: %w", err)
	}
	if filepath.IsAbs(dockerfile) {
		return dockerfile, nil
	}
	return filepath.Join(contextDir, dockerfile), nil
}
