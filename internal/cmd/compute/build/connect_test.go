package build

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	bkappdefaults "github.com/moby/buildkit/util/appdefaults"
	"github.com/moby/moby/api/types/system"
	mobyclient "github.com/moby/moby/client"
)

// connectErr runs connectBuildkit and returns its user-facing error.
func connectErr(t *testing.T, address string) *userError {
	t.Helper()
	_, _, _, err := connectBuildkit(context.Background(), address)
	var ce *userError
	if !errors.As(err, &ce) {
		t.Fatalf("expected a userError, got %v", err)
		return nil
	}
	return ce
}

func assertContains(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("message does not contain %q:\n%s", w, got)
		}
	}
}

// shortTempDir returns a temp dir short enough for unix socket paths, which
// macOS caps at 104 bytes.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func isolateEngineEnv(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(strings.TrimPrefix(bkappdefaults.Address, "unix://")); err == nil {
		t.Skip("a standalone buildkitd would be used before the container engine")
	}
	t.Setenv("BUILDKIT_HOST", "")
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
}

func TestConnectBuildkitEngineNotRunning(t *testing.T) {
	isolateEngineEnv(t)
	missing := "unix://" + filepath.Join(shortTempDir(t), "docker.sock")
	t.Setenv("DOCKER_HOST", missing)

	ce := connectErr(t, "")
	assertContains(t, ce.Error(),
		"your container engine isn't running, so your image can't be built.",
		"DOCKER_HOST points at "+missing+", but nothing is listening there.",
		"unset DOCKER_HOST",
	)
	if !errors.Is(ce, syscall.ENOENT) {
		t.Errorf("expected the cause to be ENOENT, got %v", ce.cause)
	}
}

func TestConnectBuildkitEngineRefused(t *testing.T) {
	isolateEngineEnv(t)
	sock := filepath.Join(shortTempDir(t), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	t.Setenv("DOCKER_HOST", "unix://"+sock)

	assertContains(t, connectErr(t, "").Error(), "isn't running", "nothing is listening there")
}

func TestConnectBuildkitEnginePermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores socket permissions")
	}
	isolateEngineEnv(t)
	sock := filepath.Join(shortTempDir(t), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "unix://"+sock)

	assertContains(t, connectErr(t, "").Error(),
		"your user can't access your container engine (permission denied on unix://"+sock+")",
		"sudo usermod -aG docker $USER",
	)
}

func TestConnectBuildkitUnknownContext(t *testing.T) {
	isolateEngineEnv(t)
	t.Setenv("DOCKER_CONTEXT", "nope")

	assertContains(t, connectErr(t, "").Error(),
		`DOCKER_CONTEXT is set to "nope", but there's no Docker context with that name.`,
		"Unset DOCKER_CONTEXT",
	)
}

func TestConnectBuildkitExplicitAddress(t *testing.T) {
	isolateEngineEnv(t)
	missing := "unix://" + filepath.Join(shortTempDir(t), "buildkitd.sock")

	t.Run("flag", func(t *testing.T) {
		assertContains(t, connectErr(t, missing).Error(),
			"can't reach BuildKit at "+missing+" (from --buildkit-host):\nnothing is listening there.",
			"leave out --buildkit-host",
		)
	})
	t.Run("env", func(t *testing.T) {
		t.Setenv("BUILDKIT_HOST", missing)
		assertContains(t, connectErr(t, "").Error(), "(from BUILDKIT_HOST)", "unset BUILDKIT_HOST")
	})
}

func TestExplicitBuildkitErrorReasons(t *testing.T) {
	tests := []struct {
		address, cause, want string
	}{
		{"docker-container://builder0", "Error response from daemon: No such container: builder0", `there's no container named "builder0".`},
		{"docker-container://builder0?context=x", "container builder0 is not running", `the container "builder0" is stopped.`},
		{"docker-container://builder0", "Cannot connect to the Docker daemon at unix:///var/run/docker.sock", "the container engine that runs it isn't available."},
		{"tcp://127.0.0.1:1234", "dial tcp 127.0.0.1:1234: connect: connection refused", "nothing is listening there."},
		{"tcp://127.0.0.1:1234", "something unexpected", "it didn't respond. Run with --verbose for details."},
	}
	for _, tt := range tests {
		t.Run(tt.cause, func(t *testing.T) {
			got := explicitBuildkitError(tt.address, buildkitHostFlag, errors.New(tt.cause)).Error()
			assertContains(t, got, tt.want)
			if strings.Contains(got, tt.cause) {
				t.Errorf("message leaks the raw cause:\n%s", got)
			}
		})
	}
}

func TestEngineUnreachableErrorDefaultEngineExplainsBuildkit(t *testing.T) {
	err := fmt.Errorf("dial: %w", syscall.ENOENT)
	for goos, wantLink := range map[string]string{
		"darwin":  "https://orbstack.dev/download",
		"linux":   "https://docs.docker.com/engine/install/",
		"windows": "https://docs.docker.com/desktop/setup/install/windows-install/",
	} {
		t.Run(goos, func(t *testing.T) {
			got := engineUnreachableError(engineSelection{}, "unix:///var/run/docker.sock", goos, err).Error()
			assertContains(t, got, "no BuildKit is available", whyBuildkit, wantLink, "--buildkit-host")
		})
	}
}

func TestEngineUnreachableErrorWindowsPipeMissing(t *testing.T) {
	// Shaped like the Docker client's error when Docker Desktop's pipe
	// doesn't exist: its connection error wrapping the dial's PathError.
	err := fmt.Errorf("failed to connect to the docker API at npipe:////./pipe/docker_engine; "+
		"check if the path is correct and if the daemon is running: %w",
		&os.PathError{Op: "open", Path: `\\.\pipe\docker_engine`, Err: os.ErrNotExist})

	got := engineUnreachableError(engineSelection{}, "npipe:////./pipe/docker_engine", "windows", err).Error()
	assertContains(t, got,
		"no BuildKit is available",
		"https://docs.docker.com/desktop/setup/install/windows-install/",
	)
}

func TestEngineUnreachableErrorNamesEngine(t *testing.T) {
	err := fmt.Errorf("dial: %w", syscall.ECONNREFUSED)
	got := engineUnreachableError(
		engineSelection{context: "colima", setBy: configFileSetting},
		"unix:///Users/me/.colima/default/docker.sock", "darwin", err,
	).Error()
	assertContains(t, got,
		"Colima isn't running, so your image can't be built.",
		"Your Docker context \"colima\" points at unix:///Users/me/.colima/default/docker.sock, but nothing is listening there.",
		"Start Colima, then run this command again.",
	)
}

func TestNoEngineBuildkitError(t *testing.T) {
	const host = "unix:///var/run/docker.sock"
	docker := func(version string) mobyclient.ServerVersionResult {
		return mobyclient.ServerVersionResult{
			Version:    version,
			Components: []system.ComponentVersion{{Name: "Engine", Version: version}},
		}
	}

	old := noEngineBuildkitError(engineSelection{}, host, docker("20.10.24"), "", errors.New("x")).Error()
	assertContains(t, old, "your container engine (version 20.10.24) is too old to build images.", "Update it")

	current := noEngineBuildkitError(engineSelection{}, host, docker("27.3.1"), "", errors.New("x")).Error()
	assertContains(t, current, "its BuildKit didn't respond", "--verbose")
	if strings.Contains(current, "too old") {
		t.Errorf("current engine reported as too old:\n%s", current)
	}
}

func TestNoEngineBuildkitErrorPodman(t *testing.T) {
	// Shaped like Podman's compat /version response
	// (pkg/api/handlers/compat/version.go): its own version, and an engine
	// component named "Podman Engine".
	podman := mobyclient.ServerVersionResult{
		Platform: mobyclient.PlatformInfo{Name: "linux/arm64/fedora-40"},
		Version:  "5.2.0",
		Components: []system.ComponentVersion{
			{Name: "Podman Engine", Version: "5.2.0"},
			{Name: "Conmon", Version: "conmon version 2.1.12"},
			{Name: "OCI Runtime (crun)", Version: "crun version 1.17"},
		},
	}

	got := noEngineBuildkitError(engineSelection{}, "unix:///run/podman/podman.sock", podman, "darwin", errors.New("x")).Error()
	assertContains(t, got,
		"Podman doesn't include BuildKit, so your image can't be built.",
		whyBuildkit,
		"--buildkit-host podman-container://<container-name>",
		"https://orbstack.dev/download",
		"https://github.com/moby/buildkit#quick-start",
	)
	if strings.Contains(got, "too old") {
		t.Errorf("Podman reported as too old:\n%s", got)
	}
}

func TestEngineName(t *testing.T) {
	tests := []struct {
		sel  engineSelection
		host string
		want string
	}{
		{engineSelection{context: "orbstack", setBy: configFileSetting}, "unix:///Users/me/.orbstack/run/docker.sock", "OrbStack"},
		{engineSelection{context: "colima-dev", setBy: configFileSetting}, "unix:///x", "Colima"},
		{engineSelection{context: "desktop-linux", setBy: configFileSetting}, "unix:///x", "Docker Desktop"},
		{engineSelection{setBy: "DOCKER_HOST"}, "unix:///Users/me/.rd/docker.sock", "Rancher Desktop"},
		{engineSelection{}, "unix:///var/run/docker.sock", ""},
		{engineSelection{context: "remote", setBy: configFileSetting}, "tcp://10.0.0.1:2376", ""},
	}
	for _, tt := range tests {
		if got := engineName(tt.sel, tt.host); got != tt.want {
			t.Errorf("engineName(%+v, %q) = %q, want %q", tt.sel, tt.host, got, tt.want)
		}
	}
}
