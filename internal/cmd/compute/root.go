package compute

import (
	"github.com/spf13/cobra"
	"go.datum.net/datumctl/plugin"

	"go.datum.net/compute/internal/cmd/compute/access"
	"go.datum.net/compute/internal/cmd/compute/build"
	"go.datum.net/compute/internal/cmd/compute/deploy"
	"go.datum.net/compute/internal/cmd/compute/destroy"
	"go.datum.net/compute/internal/cmd/compute/instances"
	"go.datum.net/compute/internal/cmd/compute/quota"
	"go.datum.net/compute/internal/cmd/compute/restart"
	"go.datum.net/compute/internal/cmd/compute/rollout"
	"go.datum.net/compute/internal/cmd/compute/scale"
	"go.datum.net/compute/internal/cmd/compute/util"
	"go.datum.net/compute/internal/cmd/compute/workloads"
)

func Command() *cobra.Command {
	root := plugin.NewRootCmd("compute", "Deploy and manage containerized workloads on Datum Cloud")
	root.SilenceUsage = true
	// The activation SDK writes its own user-facing copy (including any "Error:"
	// prefix) and returns a typed error carrying the exit code; the plugin entry
	// point prints and maps it. Silence cobra's default error printing so those
	// messages are not duplicated.
	root.SilenceErrors = true

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if util.GateExempt(cmd) {
			return nil
		}
		return util.RunActivationGate(cmd)
	}

	root.AddCommand(
		access.Command(),
		deploy.Command(),
		destroy.Command(),
		instances.Command(),
		quota.Command(),
		restart.Command(),
		rollout.Command(),
		scale.Command(),
		workloads.Command(),
		build.Command(),
	)

	return root
}
