package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func run(ctx context.Context, opts *options) error {
	if opts.contextDir == "" {
		opts.contextDir = "."
	}
	contextDir, err := filepath.Abs(opts.contextDir)
	if err != nil {
		return fmt.Errorf("resolving build context: %w", err)
	}
	opts.contextDir = contextDir

	if opts.kraftfile == "" {
		opts.kraftfile = findKraftfile(opts.contextDir)
	}
	if opts.kraftfile != "" {
		return runKraftBuild(ctx, opts)
	}

	output := parseOutput(opts.output)
	if err := validateOutputOptions(opts, output); err != nil {
		return err
	}
	if output.kind == outputRegistry {
		opts.ref = output.value
	} else {
		opts.ref = "localhost/datumctl-build:debug"
	}
	if opts.fix {
		opts.analyze = true
	}
	opts.dockerfile, err = resolveDockerfilePath(opts.contextDir, opts.dockerfile, opts.dockerfileExplicit)
	if err != nil {
		return err
	}

	printBuildConfig(opts)

	tmpDir, err := os.MkdirTemp("", "datumctl-build-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	opts.tmpDir = tmpDir

	stage, result, err := buildAndAnalyze(ctx, opts)
	if err != nil {
		return err
	}
	if opts.fix && result != nil && !result.OK() {
		// More than one round can be necessary: some checks (e.g. NSS modules)
		// deliberately stay silent about an entrypoint until an earlier fix
		// (e.g. missing-libs copying libc in) has been applied and rebuilt, so
		// a fixable finding can only surface after a prior round's rebuild.
		const maxFixRounds = 5
		for round := 1; result != nil && !result.OK(); round++ {
			if round > maxFixRounds {
				printAnalysisResult(result)
				return fmt.Errorf("%d compatibility issue(s) remain after %d fix attempts", len(result.Findings), maxFixRounds)
			}
			applied, err := applyExactLineFixes(result)
			if err != nil {
				return err
			}
			if applied == 0 {
				printAnalysisResult(result)
				return fmt.Errorf("%d compatibility issue(s) found; --fix had no exact line edits to apply", len(result.Findings))
			}
			fmt.Fprintln(os.Stderr, "Rebuilding after fixes")
			opts.quietBuild = true
			stage, result, err = buildAndAnalyze(ctx, opts)
			if err != nil {
				replayBuildLog(opts.lastBuildLog)
				return err
			}
		}
		opts.quietBuild = false
	}

	build, err := packageRootFS(opts, stage)
	if err != nil {
		return err
	}
	img, err := assembleComputeImage(tmpDir, build)
	if err != nil {
		return err
	}

	return handleOutput(ctx, opts, output, img)
}

// buildAndAnalyze builds the final stage and, if requested, analyzes it —
// but does not package a rootfs, since intermediate --fix rounds discard
// their build entirely once a rebuild supersedes it; the caller packages
// once, after settling on the build it's actually going to use.
func buildAndAnalyze(ctx context.Context, opts *options) (packagingArtifact, *analysisResult, error) {
	stage, err := buildFinalStage(ctx, opts)
	if err != nil {
		return packagingArtifact{}, nil, err
	}
	view, err := openTarFSView(stage.Path)
	if err != nil {
		return packagingArtifact{}, nil, err
	}
	stage.Config.Args, err = normalizeRootFSArgs(stage.Config.Args, stage.Config.WorkingDir, stage.Config.Env, view.Paths)
	if err != nil {
		return packagingArtifact{}, nil, err
	}
	if !opts.analyze {
		return stage, nil, nil
	}
	result, err := analyzeBuild(ctx, opts, view, stage.Config)
	if err != nil {
		return packagingArtifact{}, nil, err
	}
	return stage, result, nil
}

func buildStageRootFS(ctx context.Context, opts *options, stage string, entrypoint string, progress statusFunc) (*tarFSView, error) {
	progress = normalizeProgress(progress)
	rootfsTar := filepath.Join(opts.tmpDir, "source-stage-"+sanitizeFilename(stage)+".tar")
	ociTar := filepath.Join(opts.tmpDir, "source-stage-"+sanitizeFilename(stage)+".oci.tar")
	stageOpts := *opts
	stageOpts.buildTarget = stage
	progress("searching stage %q for runtime files", stage)
	if _, err := buildDockerfileFinalStageQuietly(ctx, dockerfileFinalStageRequest{
		ContextDir: stageOpts.contextDir,
		Dockerfile: stageOpts.dockerfile,
		Target:     stageOpts.buildTarget,
		BuildArgs:  slices.Clone(stageOpts.buildArgs),
		RootFSTar:  rootfsTar,
		OCITar:     ociTar,
	}); err != nil {
		return nil, rootfsBuildError(err)
	}
	view, err := openTarFSView(rootfsTar)
	if err != nil {
		return nil, err
	}
	progress("indexing files for %s", entrypoint)
	addSymlinkPathAliases(view.Paths, view.Symlinks)
	progress("indexed %d files", len(view.Paths))
	return view, nil
}

func runtimeDependencyResolverPass(ctx context.Context, opts *options) analysisPass {
	return analysisPass{ID: "runtime-file-resolver", Run: func(analysis *analysisContext) ([]finding, error) {
		var view *tarFSView
		analysis.RuntimeFileView = func() (*tarFSView, error) {
			if view != nil {
				return view, nil
			}
			if analysis.Dockerfile == nil || analysis.entrypoint() == "" {
				return nil, nil
			}
			stage := analysis.Dockerfile.findEntrypointCopySourceStage(analysis.entrypoint())
			if stage == "" {
				return nil, nil
			}
			var err error
			view, err = buildStageRootFS(ctx, opts, stage, analysis.entrypoint(), analysis.status)
			if err != nil {
				return nil, err
			}
			return view, nil
		}
		return nil, nil
	}}
}

func workdirWrapNotePass(workingDir string) analysisPass {
	return analysisPass{ID: "workdir-wrap-note", Run: func(ctx *analysisContext) ([]finding, error) {
		ctx.Notes = append(ctx.Notes, "wrapping the startup command to cd into "+workingDir+" first (your WORKDIR)")
		return nil, nil
	}}
}

func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "stage"
	}
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func replayBuildLog(path string) {
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, "Build output:")
	_, _ = os.Stderr.Write(data)
	if data[len(data)-1] != '\n' {
		fmt.Fprintln(os.Stderr)
	}
}

func analyzeBuild(ctx context.Context, opts *options, view *tarFSView, config imageConfig) (*analysisResult, error) {
	var task *task
	progress := noopProgress
	if !opts.verbose {
		task = stderrSpinner.Start("Analyzing image filesystem")
		progress = func(format string, args ...any) {
			if task != nil {
				task.Update(fmt.Sprintf(format, args...))
			}
		}
	}
	passes := []analysisPass{runtimeDependencyResolverPass(ctx, opts)}
	if config.WorkingDir != "" && config.WorkingDir != "/" {
		passes = append(passes, workdirWrapNotePass(config.WorkingDir))
	}
	result, err := buildAnalysisResultFromView(opts, view, config.Args, config.Env, passes, progress)
	if task != nil {
		task.Done(err)
	}
	if err != nil {
		return nil, err
	}
	if opts.fix {
		printFixAnalysisSummary(result)
	} else {
		printAnalysisResult(result)
	}
	if !result.OK() && !opts.fix {
		return nil, fmt.Errorf("%d compatibility issue(s) found", len(result.Findings))
	}
	return result, nil
}
