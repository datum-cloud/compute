package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/config"
	cliflags "github.com/docker/cli/cli/flags"
	dockerbuildkit "github.com/docker/docker/client/buildkit"
	bkclient "github.com/moby/buildkit/client"
	bkappdefaults "github.com/moby/buildkit/util/appdefaults"
	mobyclient "github.com/moby/moby/client"
	"github.com/spf13/pflag"
)

const (
	buildkitHostFlag = "--buildkit-host"
	dockerDesktop    = "Docker Desktop"
	buildkitHostEnv  = "BUILDKIT_HOST"

	whyBuildkit = "Datum Compute runs your app in a lightweight virtual machine, built from your\n" +
		"Dockerfile. datumctl uses BuildKit as part of the build process."

	// minEngineMajor is the first Docker release whose BuildKit answers the
	// Info call datumctl makes on connect.
	minEngineMajor = 23
)

// connectError's cause is only shown with --verbose.
type connectError struct {
	message string
	cause   error
}

func (e *connectError) Error() string { return e.message }
func (e *connectError) Unwrap() error { return e.cause }

func paragraphs(p ...string) string { return strings.Join(p, "\n\n") }

// connectBuildkit returns a BuildKit client, a cleanup func (possibly nil),
// and a short name for what it connected to. An explicit address
// (--buildkit-host, then BUILDKIT_HOST) is used as-is; otherwise a running
// standalone buildkitd wins, then the container engine's built-in BuildKit.
func connectBuildkit(ctx context.Context, address string) (*bkclient.Client, func(), string, error) {
	source := buildkitHostFlag
	if address == "" {
		address, source = os.Getenv(buildkitHostEnv), buildkitHostEnv
	}
	if address != "" {
		c, err := dialBuildkit(ctx, address)
		if err != nil {
			return nil, nil, "", explicitBuildkitError(address, source, err)
		}
		return c, nil, "BuildKit at " + address, nil
	}

	// Most machines have no standalone buildkitd, so it's only tried when its
	// socket exists.
	if _, err := os.Stat(strings.TrimPrefix(bkappdefaults.Address, "unix://")); err == nil {
		if c, err := dialBuildkit(ctx, bkappdefaults.Address); err == nil {
			return c, nil, "buildkitd at " + bkappdefaults.Address, nil
		}
	}

	return connectEngineBuildkit(ctx)
}

func dialBuildkit(ctx context.Context, address string) (*bkclient.Client, error) {
	c, err := bkclient.New(ctx, address)
	if err != nil {
		return nil, err
	}
	if _, err := c.Info(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// engineSelection records which setting chose the engine endpoint, so
// errors can name it.
type engineSelection struct {
	context string // empty for DOCKER_HOST and the default context
	setBy   string // DOCKER_HOST, DOCKER_CONTEXT, configFileSetting, or empty
}

const configFileSetting = "config"

func currentEngineSelection() engineSelection {
	if os.Getenv(mobyclient.EnvOverrideHost) != "" {
		return engineSelection{setBy: mobyclient.EnvOverrideHost}
	}
	if name := os.Getenv(command.EnvOverrideContext); name != "" && name != command.DefaultContextName {
		return engineSelection{context: name, setBy: command.EnvOverrideContext}
	}
	if name := config.LoadDefaultConfigFile(io.Discard).CurrentContext; name != "" && name != command.DefaultContextName {
		return engineSelection{context: name, setBy: configFileSetting}
	}
	return engineSelection{}
}

func engineName(sel engineSelection, host string) string {
	switch {
	case sel.context == "orbstack" || strings.Contains(host, "/.orbstack/"):
		return "OrbStack"
	case strings.HasPrefix(sel.context, "colima") || strings.Contains(host, "/.colima/"):
		return "Colima"
	case sel.context == "rancher-desktop" || strings.Contains(host, "/.rd/"):
		return "Rancher Desktop"
	case sel.context == "desktop-linux" || strings.Contains(host, "/.docker/run/"):
		return dockerDesktop
	default:
		return ""
	}
}

func connectEngineBuildkit(ctx context.Context) (*bkclient.Client, func(), string, error) {
	sel := currentEngineSelection()
	engine, err := newEngineClient()
	if err != nil {
		return nil, nil, "", engineSetupError(sel, err)
	}
	host := engine.DaemonHost()
	if err := probeEngine(ctx, engine); err != nil {
		_ = engine.Close()
		return nil, nil, "", engineUnreachableError(sel, host, runtime.GOOS, err)
	}
	c, err := bkclient.New(ctx, "", dockerbuildkit.ClientOpts(engine)...)
	if err == nil {
		if _, err = c.Info(ctx); err != nil {
			_ = c.Close()
		}
	}
	if err != nil {
		v, _ := engine.ServerVersion(ctx, mobyclient.ServerVersionOptions{})
		_ = engine.Close()
		return nil, nil, "", noEngineBuildkitError(sel, host, v, runtime.GOOS, err)
	}

	name := engineName(sel, host)
	switch {
	case name != "":
	case sel.context == "" && sel.setBy == "":
		name = "Docker"
	case sel.context != "":
		name = fmt.Sprintf("Docker context %q", sel.context)
	default:
		name = "the container engine at " + host
	}
	return c, func() { _ = engine.Close() }, name, nil
}

// newEngineClient resolves the endpoint exactly as the docker CLI does.
func newEngineClient() (mobyclient.APIClient, error) {
	opts := cliflags.NewClientOptions()
	flags := pflag.NewFlagSet("docker", pflag.ContinueOnError)
	opts.InstallFlags(flags)
	opts.SetDefaultOptions(flags)
	return command.NewAPIClientFromFlags(opts, config.LoadDefaultConfigFile(io.Discard))
}

// probeEngine checks that the engine answers. Unix and TCP endpoints are
// dialed directly first, because the client folds every connection failure
// into one message and the user needs to know which one it was.
func probeEngine(ctx context.Context, engine mobyclient.APIClient) error {
	if network, addr, ok := dialTarget(engine.DaemonHost()); ok {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		if err != nil {
			return err
		}
		_ = conn.Close()
	}
	_, err := engine.Ping(ctx, mobyclient.PingOptions{})
	return err
}

func dialTarget(host string) (network, addr string, ok bool) {
	u, err := url.Parse(host)
	if err != nil {
		return "", "", false
	}
	switch u.Scheme {
	case "unix":
		return "unix", u.Path, true
	case "tcp":
		return "tcp", u.Host, true
	default:
		return "", "", false
	}
}

func installOptions(goos string) string {
	// Standalone BuildKit only runs natively on Linux-like hosts.
	return formatInstallOptions(engineInstallOptions(goos, goos != "darwin" && goos != "windows"))
}

type installOption struct{ name, link string }

func engineInstallOptions(goos string, withBuildKit bool) []installOption {
	type option = installOption
	var opts []option
	switch goos {
	case "darwin":
		opts = []option{
			{dockerDesktop, "https://docs.docker.com/desktop/setup/install/mac-install/"},
			{"OrbStack", "https://orbstack.dev/download"},
			{"Colima", "https://colima.run/docs/installation/"},
		}
	case "linux":
		opts = []option{
			{"Docker Engine", "https://docs.docker.com/engine/install/"},
		}
	case "windows":
		opts = []option{
			{dockerDesktop, "https://docs.docker.com/desktop/setup/install/windows-install/"},
		}
	default:
		opts = []option{
			{"Docker", "https://docs.docker.com/get-started/get-docker/"},
		}
	}
	if withBuildKit {
		opts = append(opts, option{"BuildKit", "https://github.com/moby/buildkit#quick-start"})
	}
	return opts
}

func formatInstallOptions(opts []installOption) string {
	lines := make([]string, len(opts))
	for i, o := range opts {
		lines[i] = fmt.Sprintf("  %-16s %s", o.name, o.link)
	}
	return strings.Join(lines, "\n")
}

func noBuildkitError(goos string, cause error) error {
	return &connectError{
		message: paragraphs(
			"no BuildKit is available, so your image can't be built.",
			whyBuildkit,
			"BuildKit comes with most container engines and can also run on its own.\n"+
				"Install and start one of these, then run this command again.",
			installOptions(goos),
			"Already running BuildKit somewhere else? Pass its address with "+buildkitHostFlag+".",
		),
		cause: cause,
	}
}

func engineSetupError(sel engineSelection, err error) error {
	if sel.context != "" && errors.As(err, new(interface{ NotFound() })) {
		if sel.setBy == command.EnvOverrideContext {
			return &connectError{
				message: paragraphs(
					fmt.Sprintf("DOCKER_CONTEXT is set to %q, but there's no Docker context with that name.", sel.context),
					"Unset DOCKER_CONTEXT to use your default container engine, then run this command again.",
				),
				cause: err,
			}
		}
		return &connectError{
			message: paragraphs(
				fmt.Sprintf("your Docker config selects the context %q, but there's no context with that name.", sel.context),
				fmt.Sprintf("Remove \"currentContext\" from %s\nto use your default container engine, then run this command again.",
					displayPath(filepath.Join(config.Dir(), config.ConfigFileName))),
			),
			cause: err,
		}
	}
	return &connectError{
		message: paragraphs(
			"your container engine settings couldn't be loaded, so your image can't be built.",
			"Run with --verbose to see why.",
		),
		cause: err,
	}
}

func engineUnreachableError(sel engineSelection, host, goos string, err error) error {
	// os.ErrNotExist, not syscall.ENOENT, so a missing Windows named pipe
	// (Docker Desktop not running) counts too.
	notListening := errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
	if notListening && sel.context == "" && sel.setBy == "" {
		return noBuildkitError(goos, err)
	}

	subject := engineName(sel, host)
	if subject == "" {
		subject = "your container engine"
	}
	var origin string
	switch {
	case sel.context != "":
		origin = fmt.Sprintf("Your Docker context %q points at %s", sel.context, host)
	case sel.setBy != "":
		origin = fmt.Sprintf("%s points at %s", sel.setBy, host)
	default:
		origin = "datumctl looked for it at " + host
	}

	switch {
	case notListening:
		fix := "Start " + subject + ", then run this command again."
		if sel.setBy == mobyclient.EnvOverrideHost {
			fix = "Start it, or unset DOCKER_HOST to use your default container engine,\nthen run this command again."
		}
		return &connectError{
			message: paragraphs(
				subject+" isn't running, so your image can't be built.",
				origin+", but nothing is listening there.\n"+fix,
			),
			cause: err,
		}
	case errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM):
		return &connectError{
			message: paragraphs(
				fmt.Sprintf("your user can't access %s (permission denied on %s), so your image can't be built.", subject, host),
				"Add yourself to the docker group, then log out and back in:\n  sudo usermod -aG docker $USER",
			),
			cause: err,
		}
	default:
		return &connectError{
			message: paragraphs(
				fmt.Sprintf("couldn't connect to %s at %s, so your image can't be built.", subject, host),
				"Check that it's running and reachable, then run this command again.\nRun with --verbose for details.",
			),
			cause: err,
		}
	}
}

// podmanEngineComponent is how Podman names itself in its Docker-compatible
// /version response (pkg/api/handlers/compat/version.go); Platform.Name only
// carries the OS and distro.
const podmanEngineComponent = "Podman Engine"

func isPodman(v mobyclient.ServerVersionResult) bool {
	for _, c := range v.Components {
		if c.Name == podmanEngineComponent {
			return true
		}
	}
	return false
}

func noEngineBuildkitError(sel engineSelection, host string, v mobyclient.ServerVersionResult, goos string, err error) error {
	// Podman's Docker-compatible API has no BuildKit endpoints and builds with
	// Buildah instead, so updating it won't help; BuildKit can still run as a
	// Podman container. See https://github.com/containers/podman/issues/17836.
	if isPodman(v) {
		return &userError{
			message: paragraphs(
				"Podman doesn't include BuildKit, so your image can't be built.",
				whyBuildkit,
				"Run BuildKit in a Podman container and pass it with\n"+
					buildkitHostFlag+" podman-container://<container-name>, or install a container\n"+
					"engine that includes BuildKit:",
				formatInstallOptions(engineInstallOptions(goos, true)),
			),
			cause: err,
		}
	}

	subject := engineName(sel, host)
	if subject == "" {
		subject = "your container engine"
	}
	version := v.Version
	if major, err := strconv.Atoi(strings.Split(version, ".")[0]); err == nil && major < minEngineMajor {
		return &connectError{
			message: paragraphs(
				fmt.Sprintf("%s (version %s) is too old to build images.", subject, version),
				whyBuildkit,
				"Update it to a current version, or pass "+buildkitHostFlag+" to use a BuildKit\nyou run yourself.",
			),
			cause: err,
		}
	}
	return &connectError{
		message: paragraphs(
			subject+" is running, but its BuildKit didn't respond, so your image can't be built.",
			"Restart it, then run this command again. Run with --verbose for details.",
		),
		cause: err,
	}
}

func explicitBuildkitError(address, source string, err error) error {
	unset := "leave out " + buildkitHostFlag
	if source == buildkitHostEnv {
		unset = "unset " + buildkitHostEnv
	}
	reason := "it didn't respond. Run with --verbose for details."
	msg := err.Error()
	container, isContainer := strings.CutPrefix(address, "docker-container://")
	container, _, _ = strings.Cut(container, "?")
	switch {
	case isContainer && strings.Contains(msg, "No such container"):
		reason = fmt.Sprintf("there's no container named %q.", container)
	case isContainer && strings.Contains(msg, "is not running"):
		reason = fmt.Sprintf("the container %q is stopped.", container)
	case isContainer && (strings.Contains(msg, "Cannot connect to the Docker daemon") ||
		strings.Contains(msg, "executable file not found")):
		reason = "the container engine that runs it isn't available."
	case strings.Contains(msg, "no such file or directory") || strings.Contains(msg, "connection refused"):
		reason = "nothing is listening there."
	}
	return &connectError{
		message: paragraphs(
			fmt.Sprintf("can't reach BuildKit at %s (from %s):\n%s", address, source, reason),
			"Check the address, or "+unset+" to use your container engine's\nbuilt-in BuildKit.",
		),
		cause: err,
	}
}
