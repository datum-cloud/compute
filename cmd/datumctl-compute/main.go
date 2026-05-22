package main

import (
	"os"

	"go.datum.net/datumctl/plugin"

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

	if err := compute.Command().Execute(); err != nil {
		os.Exit(1)
	}
}
