package destroy

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
)

func Command() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "destroy <workload-name>",
		Short: "Delete a workload and all its instances",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDestroy(cmd, args, yes)
		},
		ValidArgsFunction: util.CompleteWorkloadNames,
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")

	return cmd
}

func runDestroy(cmd *cobra.Command, args []string, yes bool) error {
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

	// Summarize placements.
	var allCityCodes []string
	var totalMin int32
	for _, p := range workload.Spec.Placements {
		allCityCodes = append(allCityCodes, p.CityCodes...)
		totalMin += p.ScaleSettings.MinReplicas
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Workload:     %s\nPlacements:   %d  Cities: %s\nMin replicas: %d\n\n",
		workloadName,
		len(workload.Spec.Placements),
		strings.Join(allCityCodes, ", "),
		totalMin,
	)

	// Prompt unless --yes or non-interactive.
	if !yes && term.IsTerminal(int(os.Stdin.Fd())) {
		_, _ = fmt.Fprint(out, "This will delete workload and all its instances. Continue? (y/N): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		line = strings.TrimSpace(line)
		if line != "y" && line != "Y" {
			_, _ = fmt.Fprintln(out, "Aborted.")
			return nil
		}
	}

	if err := c.Delete(ctx, &workload); err != nil {
		return fmt.Errorf("deleting workload: %w", err)
	}

	fmt.Fprintf(out, "workload/%s deleted.\n", workloadName)
	return nil
}
