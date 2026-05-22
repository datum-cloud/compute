package main

import (
	"encoding/json"
	"fmt"
	"os"

	"go.datum.net/compute/internal/cmd/compute"
)

// version is set at build time via ldflags.
var version = "dev"

type pluginManifest struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Description   string `json:"description"`
	APIVersion    int    `json:"api_version"`
	MinAPIVersion int    `json:"min_api_version,omitempty"`
}

func main() {
	// Respond to --plugin-manifest before cobra runs so datumctl can discover
	// the plugin's metadata without full command initialization.
	for _, arg := range os.Args[1:] {
		if arg == "--plugin-manifest" {
			m := pluginManifest{
				Name:          "compute",
				Version:       version,
				Description:   "Deploy and manage containerized workloads on Datum Cloud",
				APIVersion:    1,
				MinAPIVersion: 1,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(m); err != nil {
				fmt.Fprintf(os.Stderr, "plugin manifest encode error: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}

	if err := compute.Command().Execute(); err != nil {
		os.Exit(1)
	}
}
