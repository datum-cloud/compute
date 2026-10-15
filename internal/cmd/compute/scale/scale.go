package scale

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
)

func Command() *cobra.Command {
	var min, max, cpuPercent, memoryPercent int32

	cmd := &cobra.Command{
		Use:   "scale <workload-name>",
		Short: "Adjust replica counts or autoscaling settings for a workload",
		Args:  cobra.ExactArgs(1),
		Example: `  datumctl compute scale api --min=4
  datumctl compute scale api --max=10 --cpu-percent=70
  datumctl compute scale api --max=0   # disable autoscaling`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScale(cmd, args, min, max, cpuPercent, memoryPercent)
		},
		ValidArgsFunction: util.CompleteWorkloadNames,
	}

	util.AddScaleFlags(cmd, &min, &max, &cpuPercent, &memoryPercent, 0)

	return cmd
}

func runScale(cmd *cobra.Command, args []string, min, max, cpuPercent, memoryPercent int32) error {
	flags := cmd.Flags()
	if !flags.Changed("min") && !flags.Changed("max") && !flags.Changed("cpu-percent") && !flags.Changed("memory-percent") {
		return fmt.Errorf("at least one of --min, --max, --cpu-percent, or --memory-percent must be set")
	}
	if flags.Changed("min") && min <= 0 {
		return fmt.Errorf("min replicas must be at least 1")
	}

	project := util.ProjectFromCmd(cmd)

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	ctx := context.Background()
	workloadName := args[0]

	var workload computev1alpha.Workload
	if err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: workloadName}, &workload); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("workload %q not found in project %s", workloadName, project)
		}
		return fmt.Errorf("getting workload: %w", err)
	}

	if len(workload.Spec.Placements) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "workload has no placements; nothing to scale")
		return nil
	}

	for i := range workload.Spec.Placements {
		placement := &workload.Spec.Placements[i]

		merged, err := util.MergeScaleSettings(cmd, placement.ScaleSettings, min, max, cpuPercent, memoryPercent)
		if err != nil {
			return fmt.Errorf("placement %q: %w", placement.Name, err)
		}

		placement.ScaleSettings = merged
	}

	if err := c.Update(ctx, &workload); err != nil {
		return fmt.Errorf("updating workload: %w", err)
	}

	first := workload.Spec.Placements[0].ScaleSettings
	fmt.Fprintf(cmd.OutOrStdout(),
		"Scaled workload %q — %s across %d placement(s).\nRun 'datumctl compute rollout %s' to watch progress.\n",
		workloadName, util.FormatScaleSettings(first), len(workload.Spec.Placements), workloadName,
	)

	return nil
}
