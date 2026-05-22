package rollout

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	"go.datum.net/datumctl/plugin"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/revision"
	"go.datum.net/compute/internal/cmd/compute/util"
	"go.datum.net/compute/internal/cmd/compute/watch"
)

func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollout <workload-name>",
		Short: "Watch or manage a workload rollout",
		Long: `Watch the live progress of a rollout across all placements, or inspect and
revert to a previous revision.

Pressing Ctrl-C detaches from the watch without canceling the rollout.`,
		Args:    cobra.ExactArgs(1),
		Example: `  # Watch live rollout progress
  datumctl compute rollout api

  # Show revision history
  datumctl compute rollout history api

  # Roll back to a specific revision
  datumctl compute rollout undo api --to-revision=7`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cmd, args)
		},
	}

	cmd.AddCommand(historyCommand(), undoCommand())

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
	if err := c.Get(ctx, types.NamespacedName{Namespace: project, Name: workloadName}, &workload); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("workload %q not found in project %s", workloadName, project)
		}
		return fmt.Errorf("getting workload: %w", err)
	}

	entries, currentRev, err := revision.ReadEntries(ctx, c, project, workloadName)
	if err != nil {
		return fmt.Errorf("reading revision history: %w", err)
	}

	var revLabel string
	switch {
	case currentRev == 0:
		revLabel = "rev #1"
	case len(entries) >= 2:
		revLabel = fmt.Sprintf("rev #%d → #%d", entries[1].Rev, entries[0].Rev)
	default:
		revLabel = fmt.Sprintf("rev #%d", currentRev)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Rolling workload %q  %s\n", workloadName, revLabel)

	watchCtx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer cancel()
	return watch.Rollout(watchCtx, c, out, project, workload.UID)
}

func historyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "history <workload-name>",
		Short: "Show the rollout history for a workload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHistory(cmd, args)
		},
	}
}

func runHistory(cmd *cobra.Command, args []string) error {
	project := util.ProjectFromCmd(cmd)

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	ctx := context.Background()
	workloadName := args[0]

	var workload computev1alpha.Workload
	if err := c.Get(ctx, types.NamespacedName{Namespace: project, Name: workloadName}, &workload); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("workload %q not found in project %s", workloadName, project)
		}
		return fmt.Errorf("getting workload: %w", err)
	}

	entries, currentRev, err := revision.ReadEntries(ctx, c, project, workloadName)
	if err != nil {
		return fmt.Errorf("reading revision history: %w", err)
	}

	out := cmd.OutOrStdout()

	if len(entries) == 0 {
		fmt.Fprintf(out, "No revision history found for workload %q.\n", workloadName)
		return nil
	}

	tw := util.NewTabWriter(out)
	fmt.Fprintln(tw, "REV\tWHEN\tIMAGE\tCHANGES\tBY\tSTATUS")

	for _, e := range entries {
		when := "—"
		if e.Timestamp != "" {
			t, err := time.Parse(time.RFC3339, e.Timestamp)
			if err == nil {
				when = util.RelativeAgeVerbose(metav1.Time{Time: t})
			}
		}

		status := "—"
		if e.Rev == currentRev {
			status = "active"
		}

		fmt.Fprintf(tw, "#%d\t%s\t%s\t%s\t%s\t%s\n",
			e.Rev, when, e.Image, e.Changes, e.Actor, status)
	}

	tw.Flush()
	return nil
}

func undoCommand() *cobra.Command {
	var toRevision int32

	cmd := &cobra.Command{
		Use:   "undo <workload-name>",
		Short: "Roll back a workload to a previous revision",
		Long: `Creates a new revision that is a copy of the target revision.
Rollbacks do not rewrite history.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUndo(cmd, args, toRevision)
		},
	}

	cmd.Flags().Int32Var(&toRevision, "to-revision", 0, "Revision number to roll back to (0 = previous)")

	return cmd
}

func runUndo(cmd *cobra.Command, args []string, toRevision int32) error {
	project := util.ProjectFromCmd(cmd)

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	ctx := context.Background()
	workloadName := args[0]

	var workload computev1alpha.Workload
	if err := c.Get(ctx, types.NamespacedName{Namespace: project, Name: workloadName}, &workload); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("workload %q not found in project %s", workloadName, project)
		}
		return fmt.Errorf("getting workload: %w", err)
	}

	entries, currentRev, err := revision.ReadEntries(ctx, c, project, workloadName)
	if err != nil {
		return fmt.Errorf("reading revision history: %w", err)
	}

	if len(entries) == 0 {
		return fmt.Errorf("no revision history for workload %q; cannot undo", workloadName)
	}

	if currentRev == 1 {
		return fmt.Errorf("no previous revision to roll back to")
	}

	var target int
	if toRevision == 0 {
		target = currentRev - 1
	} else {
		target = int(toRevision)
	}

	if target == currentRev {
		return fmt.Errorf("workload is already at revision #%d", currentRev)
	}

	if target < 1 {
		return fmt.Errorf("no previous revision to roll back to")
	}

	var targetEntry *revision.Entry
	for i := range entries {
		if entries[i].Rev == target {
			targetEntry = &entries[i]
			break
		}
	}
	if targetEntry == nil {
		return fmt.Errorf("revision #%d not found; run 'datumctl compute rollout history %s'", target, workloadName)
	}

	// Unmarshal the stored spec.
	var targetSpec computev1alpha.WorkloadSpec
	if err := json.Unmarshal([]byte(targetEntry.SpecJSON), &targetSpec); err != nil {
		return fmt.Errorf("decoding stored spec for revision #%d: %w", target, err)
	}

	out := cmd.OutOrStdout()
	newRev := currentRev + 1
	fmt.Fprintf(out, "Creating revision #%d (copy of #%d)...\n", newRev, target)

	workload.Spec = targetSpec
	if err := c.Update(ctx, &workload); err != nil {
		return fmt.Errorf("updating workload: %w", err)
	}

	// Determine actor from plugin context.
	actor := ""
	if pluginCtx := plugin.Context(); pluginCtx.Org != "" {
		actor = pluginCtx.Org
	}

	newSpecJSON, _ := json.Marshal(workload.Spec)
	entry := revision.Entry{
		Rev:      newRev,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Image:    targetEntry.Image,
		Changes:  fmt.Sprintf("rollback to rev #%d", target),
		Actor:    actor,
		SpecJSON: string(newSpecJSON),
	}
	if err := revision.WriteEntry(ctx, c, project, workloadName, entry); err != nil {
		fmt.Fprintf(out, "  warning: could not write revision history: %v\n", err)
	}

	fmt.Fprintf(out, "Rollout started. Run 'datumctl compute rollout %s' to watch progress.\n", workloadName)
	return nil
}
