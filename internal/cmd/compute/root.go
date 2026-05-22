package compute

import (
	"os"

	"github.com/spf13/cobra"

	"go.datum.net/compute/internal/cmd/compute/deploy"
	"go.datum.net/compute/internal/cmd/compute/destroy"
	"go.datum.net/compute/internal/cmd/compute/instances"
	"go.datum.net/compute/internal/cmd/compute/quota"
	"go.datum.net/compute/internal/cmd/compute/restart"
	"go.datum.net/compute/internal/cmd/compute/rollout"
	"go.datum.net/compute/internal/cmd/compute/scale"
	"go.datum.net/compute/internal/cmd/compute/status"
)

func Command() *cobra.Command {
	root := &cobra.Command{
		Use:   "compute",
		Short: "Deploy and manage containerized workloads on Datum Cloud",
	}

	root.PersistentFlags().String("org", os.Getenv("DATUM_ORG"),
		"Datum Cloud organization (defaults to DATUM_ORG injected by datumctl)")
	root.PersistentFlags().String("project", os.Getenv("DATUM_PROJECT"),
		"Datum Cloud project (defaults to DATUM_PROJECT injected by datumctl)")
	root.PersistentFlags().StringP("output", "o", "table",
		"Output format. One of: table|json|yaml")

	root.AddCommand(
		deploy.Command(),
		destroy.Command(),
		instances.Command(),
		quota.Command(),
		restart.Command(),
		rollout.Command(),
		scale.Command(),
		status.Command(),
	)

	return root
}
