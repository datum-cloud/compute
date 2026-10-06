package build

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/docker/cli/cli/config"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/spf13/cobra"
)

func useDockerConfig(t *testing.T, configJSON string) {
	t.Helper()
	dir := t.TempDir()
	if configJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(configJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := config.Dir()
	config.SetDir(dir)
	t.Cleanup(func() { config.SetDir(prev) })
	t.Setenv("DOCKER_AUTH_CONFIG", "")
}

// fakePlatformCredentialHelper puts the platform's default credential helper
// on an otherwise empty PATH, answering every lookup with user/secret.
func fakePlatformCredentialHelper(t *testing.T) {
	t.Helper()
	var helper string
	switch runtime.GOOS {
	case "darwin":
		helper = "docker-credential-osxkeychain"
	case "linux":
		helper = "docker-credential-secretservice"
	default:
		t.Skip("no default credential store on " + runtime.GOOS)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nread server\nprintf '{\"ServerURL\":\"%s\",\"Username\":\"user\",\"Secret\":\"secret\"}' \"$server\"\n"
	if err := os.WriteFile(filepath.Join(dir, helper), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func resolveBasic(t *testing.T, ref string) *authn.AuthConfig {
	t.Helper()
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
		return nil
	}
	auth, err := registryKeychain.Resolve(r.Context())
	if err != nil {
		t.Fatal(err)
		return nil
	}
	cfg, err := auth.Authorization()
	if err != nil {
		t.Fatal(err)
		return nil
	}
	return cfg
}

func TestRegistryKeychainUsesPlatformCredentialStore(t *testing.T) {
	// No credsStore in config.json: docker push still finds credentials in
	// the platform store, and so must we.
	useDockerConfig(t, `{}`)
	fakePlatformCredentialHelper(t)

	for _, ref := range []string{"ghcr.io/acme/api:latest", "acme/api:latest"} {
		cfg := resolveBasic(t, ref)
		if cfg.Username != "user" || cfg.Password != "secret" {
			t.Errorf("%s: got %+v, want user/secret from the credential helper", ref, cfg)
		}
	}
}

func TestRegistryKeychainUsesConfigAuths(t *testing.T) {
	// "dXNlcjpwYXNz" is base64("user:pass").
	useDockerConfig(t, `{"auths": {"ghcr.io": {"auth": "dXNlcjpwYXNz"}}}`)
	t.Setenv("PATH", t.TempDir())

	cfg := resolveBasic(t, "ghcr.io/acme/api:latest")
	if cfg.Username != "user" || cfg.Password != "pass" {
		t.Errorf("got %+v, want user/pass from config.json", cfg)
	}
}

func TestRegistryKeychainAnonymousWithoutCredentials(t *testing.T) {
	useDockerConfig(t, "")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	r, err := name.ParseReference("ghcr.io/acme/api:latest")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := registryKeychain.Resolve(r.Context())
	if err != nil {
		t.Fatal(err)
	}
	if auth != authn.Anonymous {
		t.Errorf("expected anonymous auth, got %#v", auth)
	}
}

// fakeRegistry answers every request after the /v2/ ping with status and,
// when set, a registry error code.
func fakeRegistry(t *testing.T, status int, code string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if code != "" {
			_, _ = fmt.Fprintf(w, `{"errors":[{"code":%q,"message":"nope"}]}`, code)
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func noRegistryCredentials(t *testing.T) {
	t.Helper()
	useDockerConfig(t, "")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
}

func pushErr(t *testing.T, ref string) string {
	t.Helper()
	_, err := pushImage(context.Background(), &Options{Ref: ref}, empty.Image)
	if err == nil {
		t.Fatal("expected push to fail")
		return ""
	}
	return err.Error()
}

func inspectErr(t *testing.T, ref string) string {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runInspect(cmd, ref, inspectOptions{})
	if err == nil {
		t.Fatal("expected inspect to fail")
		return ""
	}
	return err.Error()
}

func TestPushNotSignedIn(t *testing.T) {
	noRegistryCredentials(t)
	host := fakeRegistry(t, http.StatusUnauthorized, "UNAUTHORIZED")
	ref := host + "/acme/api:latest"

	assertContains(t, pushErr(t, ref),
		"you're not signed in to "+host+", so "+ref+" can't be pushed.",
		signInSource,
		"Sign in to "+host+", then run this command again.",
	)
}

func TestPushSignInRejected(t *testing.T) {
	host := fakeRegistry(t, http.StatusUnauthorized, "UNAUTHORIZED")
	useDockerConfig(t, fmt.Sprintf(`{"auths": {%q: {"auth": "dXNlcjpwYXNz"}}}`, host))
	t.Setenv("PATH", t.TempDir())
	ref := host + "/acme/api:latest"

	assertContains(t, pushErr(t, ref),
		host+" didn't accept your saved sign-in, so "+ref+" can't be pushed.",
		"Your sign-in may have expired, or your account can't push to "+host+"/acme/api.",
	)
}

func TestPushDenied(t *testing.T) {
	noRegistryCredentials(t)
	host := fakeRegistry(t, http.StatusForbidden, "DENIED")
	assertContains(t, pushErr(t, host+"/acme/api:latest"),
		"you don't have permission to push to "+host+"/acme/api.",
	)
}

func TestInspectMissingOrPrivate(t *testing.T) {
	noRegistryCredentials(t)
	host := fakeRegistry(t, http.StatusUnauthorized, "UNAUTHORIZED")
	ref := host + "/acme/api:latest"

	assertContains(t, inspectErr(t, ref),
		ref+" doesn't exist, or you need to sign in to "+host+" to see it.",
		"Check the image name, or sign in to "+host,
	)
}

func TestMissingCredentialHelper(t *testing.T) {
	useDockerConfig(t, `{"credsStore": "desktop"}`)
	t.Setenv("PATH", t.TempDir())
	host := fakeRegistry(t, http.StatusUnauthorized, "UNAUTHORIZED")
	ref := host + "/acme/api:latest"

	for name, got := range map[string]string{"push": pushErr(t, ref), "inspect": inspectErr(t, ref)} {
		t.Run(name, func(t *testing.T) {
			assertContains(t, got,
				"your saved registry sign-ins can't be read",
				`says sign-ins are stored by "desktop", but that program isn't installed.`,
				"after uninstalling Docker Desktop",
				`Remove "credsStore" from `,
			)
		})
	}
}
