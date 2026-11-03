package build

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/types"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// registryKeychain finds registry credentials the way `docker push` does.
// authn.DefaultKeychain skips the platform credential store (e.g. macOS
// Keychain) unless config.json names it, so it stays only as a fallback for
// the Podman auth files it also reads.
var registryKeychain = authn.NewMultiKeychain(dockerConfigKeychain{}, authn.DefaultKeychain)

type dockerConfigKeychain struct{}

func (dockerConfigKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	cf := config.LoadDefaultConfigFile(io.Discard)
	for _, key := range []string{target.String(), target.RegistryStr()} {
		if key == name.DefaultRegistry {
			key = authn.DefaultAuthKey
		}
		cfg, err := cf.GetAuthConfig(key)
		if err != nil {
			return nil, err
		}
		// GetAuthConfig always fills in ServerAddress, so it can't count
		// toward finding credentials.
		cfg.ServerAddress = ""
		if cfg != (types.AuthConfig{}) {
			return authn.FromConfig(authn.AuthConfig{
				Username:      cfg.Username,
				Password:      cfg.Password,
				Auth:          cfg.Auth,
				IdentityToken: cfg.IdentityToken,
				RegistryToken: cfg.RegistryToken,
			}), nil
		}
	}
	return authn.Anonymous, nil
}

const signInSource = "datumctl uses the registry sign-ins saved on this machine, for example by\n" +
	"Docker Desktop or docker login."

func registryDisplayName(registry string) string {
	if registry == name.DefaultRegistry {
		return "Docker Hub"
	}
	return registry
}

// registryError rewords a push or inspect failure that comes down to
// credentials or permissions. Other failures are returned unchanged.
func registryError(ref name.Reference, pushing bool, err error) error {
	registry := registryDisplayName(ref.Context().RegistryStr())
	image := ref.String()

	auth, authErr := registryKeychain.Resolve(ref.Context())
	if authErr != nil {
		outcome := image + " can't be inspected"
		if pushing {
			outcome = image + " can't be pushed"
		}
		return credentialStoreError(ref.Context().RegistryStr(), outcome, pushing, authErr)
	}
	signedIn := auth != authn.Anonymous

	var terr *transport.Error
	if !errors.As(err, &terr) {
		return err
	}
	denied := terr.StatusCode == http.StatusForbidden
	for _, d := range terr.Errors {
		if d.Code == transport.DeniedErrorCode {
			denied = true
		}
	}
	unauthorized := terr.StatusCode == http.StatusUnauthorized

	if !pushing {
		// Registries answer 401 for repositories that don't exist, so a
		// failed inspect can't tell "missing" from "private".
		switch {
		case unauthorized && !signedIn:
			return &userError{
				message: paragraphs(
					fmt.Sprintf("%s doesn't exist, or you need to sign in to %s to see it.", image, registry),
					signInSource+fmt.Sprintf("\nCheck the image name, or sign in to %s, then run this command again.", registry),
				),
				cause: err,
			}
		case unauthorized || denied:
			return &userError{
				message: paragraphs(
					fmt.Sprintf("%s doesn't exist, or your account can't see it.", image),
					"Check the image name and that your account can access it.",
				),
				cause: err,
			}
		}
		return err
	}

	switch {
	case denied:
		return &userError{
			message: paragraphs(
				fmt.Sprintf("you don't have permission to push to %s.", ref.Context()),
				"Check that the repository name is right and that your account can push to it.",
			),
			cause: err,
		}
	case unauthorized && !signedIn:
		return &userError{
			message: paragraphs(
				fmt.Sprintf("you're not signed in to %s, so %s can't be pushed.", registry, image),
				signInSource+fmt.Sprintf(" Sign in to %s, then run this command again.", registry),
			),
			cause: err,
		}
	case unauthorized:
		return &userError{
			message: paragraphs(
				fmt.Sprintf("%s didn't accept your saved sign-in, so %s can't be pushed.", registry, image),
				fmt.Sprintf("Your sign-in may have expired, or your account can't push to %s.\n"+
					"Sign in to %s again, then run this command again.", ref.Context(), registry),
			),
			cause: err,
		}
	}
	return err
}

// credentialStoreError explains a credential helper failure. registry may be
// empty when the failing lookup's registry isn't known.
func credentialStoreError(registry, outcome string, hasVerbose bool, err error) error {
	headline := fmt.Sprintf("your saved registry sign-ins can't be read, so %s.", outcome)

	cf := config.LoadDefaultConfigFile(io.Discard)
	helper, setting := cf.CredentialsStore, `"credsStore"`
	if h, ok := cf.CredentialHelpers[registry]; ok && registry != "" {
		helper, setting = h, fmt.Sprintf("the %q entry in \"credHelpers\"", registry)
	}
	if m := credentialHelperName.FindStringSubmatch(err.Error()); m != nil {
		helper = m[1]
	}
	if helper == "" || !isMissingCredentialHelper(err) {
		// inspect has no --verbose, so it shows the cause right away.
		next := "Run with --verbose for details."
		if !hasVerbose {
			next = "Details: " + err.Error()
		}
		return &userError{message: paragraphs(headline, next), cause: err}
	}

	explain := fmt.Sprintf("Your Docker config says sign-ins are stored by %q, but that program isn't installed.", helper)
	if helper == "desktop" {
		explain += "\nThis usually happens after uninstalling Docker Desktop."
	}
	signIn := "sign in to your registries again"
	if registry != "" {
		signIn = "sign in to " + registryDisplayName(registry) + " again"
	}
	return &userError{
		message: paragraphs(
			headline,
			explain+fmt.Sprintf("\nRemove %s from %s, then %s.", setting, homePath(filepath.Join(config.Dir(), config.ConfigFileName)), signIn),
		),
		cause: err,
	}
}

var credentialHelperName = regexp.MustCompile(`docker-credential-([\w.-]+)`)

func isMissingCredentialHelper(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found")
}

// homePath shortens paths under the home directory to ~/...
func homePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if rel, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + filepath.ToSlash(rel)
	}
	return path
}
