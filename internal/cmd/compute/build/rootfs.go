package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sirupsen/logrus"
)

const workdirWrapperArg0 = "datum-workdir"

type imageConfig struct {
	Args       []string
	Env        []string
	WorkingDir string
}

type packagingArtifact struct {
	Path   string
	Config imageConfig
}

// rootfsBuildError turns a low-level BuildKit/erofs error into a clearer one
// by matching known substrings from the underlying library's error text —
// fragile against upstream wording changes, but there's no structured error
// type to match on instead.
func rootfsBuildError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "could not create EroFS archive") && strings.Contains(msg, "could not create symlink") {
		return fmt.Errorf("building root filesystem: EROFS packaging failed on duplicate symlink metadata")
	}
	for _, s := range []string{
		"could not connect to buildkit",
		"could not start ephemeral BuildKit container",
		"creating buildkit container",
		"creating container buildkit client",
		"connecting to buildkit client",
	} {
		if strings.Contains(msg, s) {
			return fmt.Errorf("building root filesystem: Docker is not running or is not accessible")
		}
	}
	return fmt.Errorf("building root filesystem: %w", err)
}

func buildFinalStage(ctx context.Context, opts *options) (packagingArtifact, error) {
	var progress io.Writer = os.Stderr
	if opts.quietBuild {
		logPath := filepath.Join(opts.tmpDir, "buildkit.log")
		logFile, err := os.Create(logPath)
		if err != nil {
			return packagingArtifact{}, fmt.Errorf("creating build log: %w", err)
		}
		defer logFile.Close()
		opts.lastBuildLog = logPath
		progress = logFile
	} else {
		opts.lastBuildLog = ""
	}

	rootfsTar := filepath.Join(opts.tmpDir, "rootfs.tar")
	ociTar := filepath.Join(opts.tmpDir, "image.oci.tar")
	var task *task
	if opts.quietBuild {
		task = stderrSpinner.Start("Building Dockerfile")
	} else {
		fmt.Fprintln(os.Stderr, "Building Dockerfile ...")
	}
	result, err := buildDockerfileFinalStageQuietly(ctx, dockerfileFinalStageRequest{
		ContextDir:  opts.contextDir,
		Dockerfile:  opts.dockerfile,
		Target:      opts.buildTarget,
		BuildArgs:   slices.Clone(opts.buildArgs),
		RootFSTar:   rootfsTar,
		OCITar:      ociTar,
		progressOut: progress,
	})
	if task != nil {
		task.Done(err)
	}
	if err != nil {
		return packagingArtifact{}, rootfsBuildError(err)
	}
	return result, nil
}

func packageRootFS(opts *options, build packagingArtifact) (packagingArtifact, error) {
	rootfsPath := filepath.Join(opts.tmpDir, "rootfs.erofs")
	if err := os.MkdirAll(filepath.Dir(rootfsPath), 0o755); err != nil {
		return packagingArtifact{}, err
	}
	if err := withProgress("Packaging root filesystem", func() error { return createErofsFromTar(build.Path, rootfsPath) }); err != nil {
		return packagingArtifact{}, rootfsBuildError(err)
	}
	return packagingArtifact{Path: rootfsPath, Config: build.Config}, nil
}

func buildDockerfileFinalStageQuietly(ctx context.Context, req dockerfileFinalStageRequest) (packagingArtifact, error) {
	prevOut := logrus.StandardLogger().Out
	logrus.SetOutput(io.Discard)
	defer logrus.SetOutput(prevOut)
	return buildDockerfileFinalStage(ctx, req)
}

func normalizeRootFSArgs(args []string, workingDir string, env []string, paths map[string]struct{}) ([]string, error) {
	args = slices.Clone(args)
	if len(args) >= 2 && isInheritedEntrypointWrapper(args[0]) {
		args = args[1:]
	}
	switch {
	case len(args) == 0 || path.IsAbs(args[0]):
		// nothing to resolve
	case strings.Contains(args[0], "/"):
		args[0] = normalizeDockerPath(args[0], workingDir)
	default:
		if resolved := resolveEntrypointPathWithDirs(args[0], paths, envPathDirs(env)); resolved != "" {
			args[0] = resolved
		}
	}
	if workingDir == "" || workingDir == "/" || len(args) == 0 {
		return args, nil
	}
	shell := findShell(paths)
	if shell == "" {
		return nil, fmt.Errorf("image uses WORKDIR %s, but no shell was found in the image to emulate it; use an absolute command that does not rely on WORKDIR, or add /bin/sh to the image", workingDir)
	}
	return wrapArgsWithWorkingDirShell(shell, workingDir, args), nil
}

func findShell(paths map[string]struct{}) string {
	for _, name := range posixShellNames {
		if resolved := resolveEntrypointPathWithDirs(name, paths, nil); resolved != "" {
			return resolved
		}
	}
	return ""
}

func wrapArgsWithWorkingDirShell(shell, workingDir string, args []string) []string {
	return slices.Concat([]string{shell, "-c", "cd " + shellQuote(workingDir) + " && exec \"$@\"", workdirWrapperArg0}, args)
}

// unwrapWorkdirWrapperArgs recognizes our own synthetic workdir wrapper
// (built by wrapArgsWithWorkingDirShell) and returns the real command it
// wraps. wrapArgsWithWorkingDirShell only ever wraps a non-empty args, so a
// marker with nothing after it (len(args) == 4) isn't a real match.
func unwrapWorkdirWrapperArgs(args []string) ([]string, bool) {
	if len(args) >= 5 && args[1] == "-c" && args[3] == workdirWrapperArg0 {
		return args[4:], true
	}
	return nil, false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
