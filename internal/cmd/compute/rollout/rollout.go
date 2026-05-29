package rollout

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	"go.datum.net/compute/internal/cmd/compute/watch"
)

func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollout <workload-name>",
		Short: "Watch live rollout progress for a workload",
		Long: `Watch the live progress of a rollout across all placements.

Pressing Ctrl-C detaches from the watch without canceling the rollout.`,
		Args: cobra.ExactArgs(1),
		Example: `  # Watch live rollout progress
  datumctl compute rollout api`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cmd, args)
		},
		ValidArgsFunction: util.CompleteWorkloadNames,
	}

	return cmd
}

func runWatch(cmd *cobra.Command, args []string) error {
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

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Rolling workload %q\n", workloadName)

	watchCtx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer cancel()
	return watch.Rollout(watchCtx, c, out, project, workload.UID)
}
