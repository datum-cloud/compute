package compute

import (
	"github.com/spf13/cobra"
	"go.datum.net/datumctl/plugin"

	"go.datum.net/compute/internal/cmd/compute/deploy"
	"go.datum.net/compute/internal/cmd/compute/destroy"
	"go.datum.net/compute/internal/cmd/compute/instances"
	"go.datum.net/compute/internal/cmd/compute/quota"
	"go.datum.net/compute/internal/cmd/compute/restart"
	"go.datum.net/compute/internal/cmd/compute/rollout"
	"go.datum.net/compute/internal/cmd/compute/scale"
	"go.datum.net/compute/internal/cmd/compute/workloads"
)

func Command() *cobra.Command {
	root := plugin.NewRootCmd("compute", "Deploy and manage containerized workloads on Datum Cloud")

	root.AddCommand(
		deploy.Command(),
		destroy.Command(),
		instances.Command(),
		quota.Command(),
		restart.Command(),
		rollout.Command(),
		scale.Command(),
		workloads.Command(),
	)

	return root
}
