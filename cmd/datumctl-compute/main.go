package main

import (
	"errors"
	"fmt"
	"os"

	"go.datum.net/datumctl/plugin"
	"go.datum.net/datumctl/serviceactivation"

	"go.datum.net/compute/internal/cmd/compute"
)

// version is set at build time via ldflags.
var version = "dev"

func main() {
	plugin.ServeManifest(plugin.Manifest{
		Name:          "compute",
		Version:       version,
		Description:   "Deploy and manage containerized workloads on Datum Cloud",
		APIVersion:    1,
		MinAPIVersion: 1,
	})

	err := compute.Command().Execute()
	if err == nil {
		return
	}

	// The activation SDK already wrote its user-facing copy to stderr; only
	// non-SDK errors need printing here (cobra's own printing is silenced).
	var activationErr *serviceactivation.Error
	if !errors.As(err, &activationErr) {
		fmt.Fprintln(os.Stderr, "Error:", err)
	}

	// Plugins are exec'd binaries, so the plugin owns its exit status. Map the
	// SDK's typed errors onto the documented exit-code contract (10–13); any
	// other error exits 1.
	os.Exit(serviceactivation.ExitCodeOf(err))
}
