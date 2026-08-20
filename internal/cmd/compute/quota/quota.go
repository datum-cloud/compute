package quota

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"go.datum.net/compute/internal/cmd/compute/util"
)

const resourceTypePrefix = "compute.datumapis.com"

// orderedTypes controls display order in the quota table.
var orderedTypes = []string{
	"compute.datumapis.com/workloads",
	"compute.datumapis.com/instances",
	"compute.datumapis.com/vcpus",
	"compute.datumapis.com/memory",
}

// computeMeta provides display overrides for compute resource types.
// The live ResourceRegistrations use displayUnit "1" so we supply our own.
var computeMeta = map[string]util.QuotaMeta{
	"compute.datumapis.com/workloads": {DisplayName: "Workloads", Unit: "workloads", Divisor: 1},
	"compute.datumapis.com/instances": {DisplayName: "Instances", Unit: "instances", Divisor: 1},
	"compute.datumapis.com/vcpus":     {DisplayName: "vCPUs", Unit: "vCPUs", Divisor: 1000},
	"compute.datumapis.com/memory":    {DisplayName: "Memory", Unit: "MiB", Divisor: 1},
}

func Command() *cobra.Command {
	var constrained bool

	cmd := &cobra.Command{
		Use:   "quota",
		Short: "Show compute quota for the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQuota(cmd, constrained)
		},
	}

	cmd.Flags().BoolVar(&constrained, "constrained", false, "Show only resource types that are at their limit")
	cmd.Flags().StringP("output", "o", "table", "Output format: table, json, yaml")

	_ = cmd.RegisterFlagCompletionFunc("output", util.CompleteOutputFormats("table", "json", "yaml"))

	return cmd
}

const barWidth = 20

func quotaBar(used, limit int64) string {
	if limit <= 0 {
		return "[" + strings.Repeat("-", barWidth) + "]   N/A"
	}
	pct := float64(used) / float64(limit)
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * barWidth)
	bar := strings.Repeat("#", filled) + strings.Repeat("-", barWidth-filled)
	return fmt.Sprintf("[%s] %3.0f%%", bar, pct*100)
}

func runQuota(cmd *cobra.Command, constrained bool) error {
	project := util.ProjectFromCmd(cmd)
	outputFlag, _ := cmd.Flags().GetString("output")

	projectClient, err := util.NewClient(project)
	if err != nil {
		return err
	}

	platformClient, err := util.NewPlatformClient()
	if err != nil {
		return err
	}

	ctx := context.Background()

	rows, err := util.ListServiceQuota(ctx, projectClient, platformClient, resourceTypePrefix, computeMeta, orderedTypes)
	if err != nil {
		return fmt.Errorf("listing quota: %w", err)
	}

	if constrained {
		filtered := rows[:0]
		for _, r := range rows {
			if r.Available == 0 {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	out := cmd.OutOrStdout()

	switch util.OutputFormat(outputFlag) {
	case util.OutputJSON:
		return util.PrintJSON(out, rows)
	case util.OutputYAML:
		return util.PrintYAML(out, rows)
	}

	if len(rows) == 0 {
		if constrained {
			_, _ = fmt.Fprintln(out, "No resource types are at their limit.")
		} else {
			_, _ = fmt.Fprintln(out, "No quota configured for this project.")
		}
		return nil
	}

	_, _ = fmt.Fprintf(out, "Quota for project %s\n\n", project)

	tw := util.NewTabWriter(out)
	_, _ = fmt.Fprintf(tw, "RESOURCE\tUNIT\tLIMIT\tUSED\tAVAILABLE\tUSAGE\n")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n",
			r.DisplayName, r.Unit, r.Limit, r.Used, r.Available, quotaBar(r.Used, r.Limit))
	}
	_ = tw.Flush()

	return nil
}
