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
	var min int32

	cmd := &cobra.Command{
		Use:     "scale <workload-name>",
		Short:   "Adjust the minimum replica count for a workload",
		Args:    cobra.ExactArgs(1),
		Example: `  datumctl compute scale api --min=4`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScale(cmd, args, min)
		},
	}

	cmd.Flags().Int32Var(&min, "min", 0, "Minimum number of instances per city")
	_ = cmd.MarkFlagRequired("min")

	return cmd
}

func runScale(cmd *cobra.Command, args []string, min int32) error {
	if min <= 0 {
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
		workload.Spec.Placements[i].ScaleSettings.MinReplicas = min
	}

	if err := c.Update(ctx, &workload); err != nil {
		return fmt.Errorf("updating workload: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"Scaled workload %q — min replicas set to %d across %d placement(s).\nRun 'datumctl compute rollout %s' to watch progress.\n",
		workloadName, min, len(workload.Spec.Placements), workloadName,
	)

	return nil
}
