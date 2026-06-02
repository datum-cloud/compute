package workloads

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
)

// Command returns the top-level "workloads" command group.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workloads",
		Short: "List or inspect workloads",
		Long: `List all workloads in the project, optionally filtered by health or city.
Use the describe subcommand for a unified config + health view of a single workload.`,
		Example: `  # List all workloads
  datumctl compute workloads

  # Filter by health
  datumctl compute workloads --health=degraded

  # Filter by city
  datumctl compute workloads --city=DFW

  # Machine-readable output
  datumctl compute workloads -o json

  # Describe a single workload
  datumctl compute workloads describe api`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd, args)
		},
	}

	// List flags.
	cmd.Flags().String("health", "", "Filter by health: available, degraded, progressing, unknown")
	cmd.Flags().String("city", "", "Filter to workloads with a placement in this city")
	cmd.Flags().StringP("output", "o", "table", "Output format: table, wide, json, yaml")
	cmd.Flags().Bool("no-headers", false, "Omit the table header row (table and wide only)")

	_ = cmd.RegisterFlagCompletionFunc("health", util.CompleteOutputFormats("available", "degraded", "progressing", "unknown"))
	_ = cmd.RegisterFlagCompletionFunc("city", util.CompleteCityCodes)
	_ = cmd.RegisterFlagCompletionFunc("output", util.CompleteOutputFormats("table", "wide", "json", "yaml"))

	cmd.AddCommand(describeCommand())

	return cmd
}

// -----------------------------------------------------------------------
// workloads list
// -----------------------------------------------------------------------

func runList(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()
	project := util.ProjectFromCmd(cmd)

	outputFlag, _ := cmd.Flags().GetString("output")
	healthFilter, _ := cmd.Flags().GetString("health")
	cityFilter, _ := cmd.Flags().GetString("city")
	noHeaders, _ := cmd.Flags().GetBool("no-headers")

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	var wlList computev1alpha.WorkloadList
	if err := c.List(ctx, &wlList, client.InNamespace(util.ResourceNamespace)); err != nil {
		return fmt.Errorf("listing workloads: %w", err)
	}

	// JSON/YAML: emit raw API resource and return early.
	switch util.OutputFormat(outputFlag) {
	case util.OutputJSON:
		return util.PrintJSON(cmd.OutOrStdout(), &wlList)
	case util.OutputYAML:
		return util.PrintYAML(cmd.OutOrStdout(), &wlList)
	}

	// For table output we need deployment data to compute READY counts.
	var deployList computev1alpha.WorkloadDeploymentList
	if err := c.List(ctx, &deployList, client.InNamespace(util.ResourceNamespace)); err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}

	// Build map: workloadUID → []WorkloadDeployment.
	deploysByWorkload := make(map[string][]computev1alpha.WorkloadDeployment)
	for _, d := range deployList.Items {
		wUID := d.Labels[computev1alpha.WorkloadUIDLabel]
		deploysByWorkload[wUID] = append(deploysByWorkload[wUID], d)
	}

	// City filter: collect the set of workload UIDs that have a deployment in
	// the requested city code.
	cityFilteredUIDs := map[string]bool{}
	if cityFilter != "" {
		for _, d := range deployList.Items {
			if d.Spec.CityCode == cityFilter {
				wUID := d.Labels[computev1alpha.WorkloadUIDLabel]
				cityFilteredUIDs[wUID] = true
			}
		}
	}

	type workloadRow struct {
		name        string
		health      string
		healthShort string // first word, for filter comparison
		readyStr    string
		placements  string
		image       string
		age         string
		instType    string // wide only
	}

	wide := util.OutputFormat(outputFlag) == util.OutputWide

	var rows []workloadRow

	for _, wl := range wlList.Items {
		wUID := string(wl.UID)

		// City filter.
		if cityFilter != "" && !cityFilteredUIDs[wUID] {
			continue
		}

		deps := deploysByWorkload[wUID]
		var totalReady, totalDesired int32
		for _, d := range deps {
			totalReady += d.Status.ReadyReplicas
			totalDesired += d.Status.DesiredReplicas
		}

		health := util.WorkloadHealth(wl.Status.Conditions, totalReady, totalDesired)
		healthShort := strings.SplitN(health, " ", 2)[0] // e.g. "Available", "Degraded"

		// Health filter.
		if healthFilter != "" && !strings.EqualFold(healthShort, healthFilter) {
			continue
		}

		// Placement names.
		var placementNames []string
		for _, p := range wl.Spec.Placements {
			placementNames = append(placementNames, p.Name)
		}
		placements := strings.Join(placementNames, ", ")
		if placements == "" {
			placements = "(none)"
		}

		// Image from first container.
		image := "(vm)"
		if wl.Spec.Template.Spec.Runtime.Sandbox != nil &&
			len(wl.Spec.Template.Spec.Runtime.Sandbox.Containers) > 0 {
			image = truncateImage(wl.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image)
		}

		readyStr := fmt.Sprintf("%d/%d", totalReady, totalDesired)
		instType := wl.Spec.Template.Spec.Runtime.Resources.InstanceType

		rows = append(rows, workloadRow{
			name:        wl.Name,
			health:      health,
			healthShort: healthShort,
			readyStr:    readyStr,
			placements:  placements,
			image:       image,
			age:         util.RelativeAge(wl.CreationTimestamp),
			instType:    instType,
		})
	}

	// Tally health counts from the filtered rows (W9: count after filtering).
	healthCounts := map[string]int{
		"Available":   0,
		"Degraded":    0,
		"Unavailable": 0,
		"Unknown":     0,
	}
	for _, r := range rows {
		switch r.healthShort {
		case "Available":
			healthCounts["Available"]++
		case "Degraded":
			healthCounts["Degraded"]++
		case "Unavailable":
			healthCounts["Unavailable"]++
		default:
			healthCounts["Unknown"]++
		}
	}

	out := cmd.OutOrStdout()

	if len(rows) == 0 {
		if healthFilter != "" {
			fmt.Fprintf(out, "No workloads in project %s match health=%s.\n", project, healthFilter)
		} else if cityFilter != "" {
			fmt.Fprintf(out, "No workloads in project %s have a placement in city %s.\n", project, cityFilter)
		} else {
			fmt.Fprintf(out, "No workloads found in project %s.\n\n", project)
			fmt.Fprintf(out, "Get started:\n")
			fmt.Fprintf(out, "  datumctl compute deploy --name=api --image=ghcr.io/acme/api:v1.0.0 --city=DFW\n")
		}
		return nil
	}

	tw := util.NewTabWriter(out)
	if !noHeaders {
		if wide {
			fmt.Fprintf(tw, "NAME\tHEALTH\tREADY\tPLACEMENTS\tIMAGE\tAGE\tINSTANCE TYPE\n")
		} else {
			fmt.Fprintf(tw, "NAME\tHEALTH\tREADY\tPLACEMENTS\tIMAGE\tAGE\n")
		}
	}
	for _, r := range rows {
		if wide {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.name, r.healthShort, r.readyStr, r.placements, r.image, r.age, r.instType)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				r.name, r.healthShort, r.readyStr, r.placements, r.image, r.age)
		}
	}
	_ = tw.Flush()

	// Footer summary.
	fmt.Fprintf(out, "\n%d workloads — %d Available, %d Degraded, %d Unavailable, %d Unknown\n",
		len(rows),
		healthCounts["Available"],
		healthCounts["Degraded"],
		healthCounts["Unavailable"],
		healthCounts["Unknown"],
	)

	return nil
}

// truncateImage strips the registry host from an image reference so the table
// column stays compact.  "ghcr.io/acme/api:v1" → "acme/api:v1".
func truncateImage(image string) string {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 2 {
		// Only strip the first component if it looks like a registry host
		// (contains a '.' or ':' — as opposed to a Docker Hub org name).
		host := parts[0]
		if strings.ContainsAny(host, ".:") {
			return parts[1]
		}
	}
	return image
}

// -----------------------------------------------------------------------
// workloads describe
// -----------------------------------------------------------------------

func describeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "describe <name>",
		Short: "Show config and health for a single workload",
		Long: `Display a unified view of workload configuration (container spec, scale settings)
and runtime health (per-city ready/desired counts). Replaces 'datumctl compute status'.`,
		Args:    cobra.ExactArgs(1),
		Example: `  datumctl compute workloads describe api`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDescribe(cmd, args)
		},
		ValidArgsFunction: util.CompleteWorkloadNames,
	}

	cmd.Flags().StringP("output", "o", "wide", "Output format: wide, json, yaml")
	_ = cmd.RegisterFlagCompletionFunc("output", util.CompleteOutputFormats("wide", "json", "yaml"))

	return cmd
}

func runDescribe(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	project := util.ProjectFromCmd(cmd)
	outputFlag, _ := cmd.Flags().GetString("output")

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	workloadName := args[0]

	var wl computev1alpha.Workload
	if err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: workloadName}, &wl); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("workload %q not found in project %s", workloadName, project)
		}
		return fmt.Errorf("getting workload: %w", err)
	}

	// JSON / YAML: emit the raw resource.
	switch util.OutputFormat(outputFlag) {
	case util.OutputJSON:
		return util.PrintJSON(cmd.OutOrStdout(), &wl)
	case util.OutputYAML:
		return util.PrintYAML(cmd.OutOrStdout(), &wl)
	}

	// List deployments for this workload.
	selector := labels.SelectorFromSet(labels.Set{computev1alpha.WorkloadUIDLabel: string(wl.UID)})
	var deployList computev1alpha.WorkloadDeploymentList
	if err := c.List(ctx, &deployList, client.InNamespace(util.ResourceNamespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}

	// Compute totals for health.
	var totalDesired, totalReady int32
	for _, d := range deployList.Items {
		totalDesired += d.Status.DesiredReplicas
		totalReady += d.Status.ReadyReplicas
	}

	health := util.WorkloadHealth(wl.Status.Conditions, totalReady, totalDesired)

	// Determine type label.
	typeLabel := "virtual-machine"
	if wl.Spec.Template.Spec.Runtime.Sandbox != nil {
		instType := wl.Spec.Template.Spec.Runtime.Resources.InstanceType
		if instType != "" {
			typeLabel = "sandbox/" + instType
		} else {
			typeLabel = "sandbox"
		}
	}

	age := util.RelativeAgeVerbose(wl.CreationTimestamp)

	out := cmd.OutOrStdout()

	// Header block.
	fmt.Fprintf(out, "%-12s %-31s project: %s\n", "Workload", workloadName, project)
	fmt.Fprintf(out, "%-12s %s\n", "Type", typeLabel)
	fmt.Fprintf(out, "%-12s %s\n", "Updated", age)
	fmt.Fprintf(out, "\n")
	fmt.Fprintf(out, "%-12s %s\n", "Health", health)
	fmt.Fprintf(out, "\n")

	// Placements block.
	fmt.Fprintf(out, "Placements\n")
	if len(wl.Spec.Placements) == 0 {
		fmt.Fprintf(out, "  (none configured — workload will not run anywhere)\n")
	} else {
		// Build a map: placementName → []WorkloadDeployment.
		deplsByPlacement := make(map[string][]computev1alpha.WorkloadDeployment)
		for _, d := range deployList.Items {
			deplsByPlacement[d.Spec.PlacementName] = append(deplsByPlacement[d.Spec.PlacementName], d)
		}

		for _, p := range wl.Spec.Placements {
			// Placement header line.
			maxStr := "∞"
			if p.ScaleSettings.MaxReplicas != nil {
				maxStr = fmt.Sprintf("%d", *p.ScaleSettings.MaxReplicas)
			}
			cityCodes := strings.Join(p.CityCodes, ", ")
			fmt.Fprintf(out, "  %-10s cities: %-24s scale: %d..%s\n",
				p.Name, cityCodes, p.ScaleSettings.MinReplicas, maxStr)

			// Per-city lines from deployments.
			for _, d := range deplsByPlacement[p.Name] {
				readyStr := fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, d.Status.DesiredReplicas)
				annotation := ""
				if d.Status.ReadyReplicas < d.Status.DesiredReplicas {
					// Read blocking reason from the deployment's own condition.
					annotation = degradedAnnotation(ctx, c, d)
				}
				if annotation != "" {
					fmt.Fprintf(out, "    %-8s ready: %-10s %s\n", d.Spec.CityCode, readyStr, annotation)
				} else {
					fmt.Fprintf(out, "    %-8s ready: %s\n", d.Spec.CityCode, readyStr)
				}
			}
		}
	}
	fmt.Fprintf(out, "\n")

	// Container block (sandbox only).
	if wl.Spec.Template.Spec.Runtime.Sandbox != nil && len(wl.Spec.Template.Spec.Runtime.Sandbox.Containers) > 0 {
		ctr := wl.Spec.Template.Spec.Runtime.Sandbox.Containers[0]
		fmt.Fprintf(out, "Container\n")
		fmt.Fprintf(out, "  %-10s %s\n", "Image", ctr.Image)

		if len(ctr.Ports) > 0 {
			var portStrs []string
			for _, p := range ctr.Ports {
				proto := "TCP"
				if p.Protocol != nil {
					proto = string(*p.Protocol)
				}
				portStrs = append(portStrs, fmt.Sprintf("%d/%s", p.Port, proto))
			}
			fmt.Fprintf(out, "  %-10s %s\n", "Ports", strings.Join(portStrs, ", "))
		}

		if len(ctr.Env) > 0 {
			fmt.Fprintf(out, "  Env\n")
			for _, e := range ctr.Env {
				fmt.Fprintf(out, "    %s\n", formatEnvVar(e))
			}
		}

		// Resources.
		instType := wl.Spec.Template.Spec.Runtime.Resources.InstanceType
		if instType != "" {
			fmt.Fprintf(out, "  %-10s %s\n", "Resources", instType)
		}

		fmt.Fprintf(out, "\n")
	}

	// Next steps.
	fmt.Fprintf(out, "Next steps:\n")
	fmt.Fprintf(out, "  %-25s datumctl compute instances --workload=%s\n", "List instances:", workloadName)
	fmt.Fprintf(out, "  %-25s datumctl compute logs <instance>\n", "Stream logs:")
	fmt.Fprintf(out, "  %-25s datumctl compute rollout undo %s\n", "Roll back:", workloadName)

	return nil
}

// degradedAnnotation returns a short annotation for a per-city line when the
// deployment is not fully ready. It reads the blocking reason+message from the
// deployment's own Available condition, which the server rolls up from the
// underlying instances. No per-instance fetch or reason branching needed.
func degradedAnnotation(_ context.Context, _ client.Client, d computev1alpha.WorkloadDeployment) string {
	reason, msg, blocked := util.ReadinessBlock(d.Status.Conditions, computev1alpha.WorkloadDeploymentAvailable)
	if !blocked {
		return ""
	}
	if msg != "" {
		return "Blocked — " + msg
	}
	if reason != "" {
		return "Blocked — " + reason
	}
	return "Blocked"
}

// formatEnvVar renders a single EnvVar for display.
func formatEnvVar(e corev1.EnvVar) string {
	if e.ValueFrom != nil {
		if e.ValueFrom.SecretKeyRef != nil {
			return fmt.Sprintf("%-20s from secret %s", e.Name, e.ValueFrom.SecretKeyRef.Name)
		}
		if e.ValueFrom.ConfigMapKeyRef != nil {
			return fmt.Sprintf("%-20s from configmap %s", e.Name, e.ValueFrom.ConfigMapKeyRef.Name)
		}
	}
	return fmt.Sprintf("%-20s %s", e.Name, e.Value)
}
