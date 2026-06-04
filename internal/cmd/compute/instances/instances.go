package instances

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
)

type listOptions struct {
	workload string
	city     string
}

func Command() *cobra.Command {
	opts := &listOptions{}

	cmd := &cobra.Command{
		Use:   "instances",
		Short: "List or inspect workload instances",
		Long: `List all running instances in the project, optionally filtered by workload.
Use the describe subcommand for full details on a single instance.`,
		Example: `  # List all instances
  datumctl compute instances

  # Filter by workload
  datumctl compute instances --workload=api

  # Filter by city
  datumctl compute instances --city=DFW

  # Machine-readable output
  datumctl compute instances -o json

  # Describe a single instance
  datumctl compute instances describe api-dfw-0`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd, opts)
		},
	}

	cmd.Flags().StringVar(&opts.workload, "workload", "", "Filter instances to a specific workload")
	cmd.Flags().StringVar(&opts.city, "city", "", "Filter instances to a specific city")
	cmd.Flags().StringP("output", "o", "table", "Output format: table, wide, json, yaml")
	cmd.Flags().Bool("no-headers", false, "Omit the table header row (table and wide only)")

	_ = cmd.RegisterFlagCompletionFunc("workload", util.CompleteWorkloadNames)
	_ = cmd.RegisterFlagCompletionFunc("city", util.CompleteCityCodes)
	_ = cmd.RegisterFlagCompletionFunc("output", util.CompleteOutputFormats("table", "wide", "json", "yaml"))

	cmd.AddCommand(describeCommand())

	return cmd
}

type instanceRow struct {
	name        string
	workload    string
	city        string
	externalIP  string
	internalIP  string
	runtimeKind string // "sandbox" or "vm"
	instType    string
	age         string
	status      string
}

func runList(cmd *cobra.Command, opts *listOptions) error {
	ctx := context.Background()
	project := util.ProjectFromCmd(cmd)
	outputFlag, _ := cmd.Flags().GetString("output")
	noHeaders, _ := cmd.Flags().GetBool("no-headers")

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	// Optionally resolve workload UID.
	var workloadUID string
	if opts.workload != "" {
		var wl computev1alpha.Workload
		if err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: opts.workload}, &wl); err != nil {
			if k8serrors.IsNotFound(err) {
				return fmt.Errorf("workload %q not found", opts.workload)
			}
			return fmt.Errorf("getting workload: %w", err)
		}
		workloadUID = string(wl.UID)
	}

	// List instances.
	var instList computev1alpha.InstanceList
	listOpts := []client.ListOption{client.InNamespace(util.ResourceNamespace)}
	if workloadUID != "" {
		selector := labels.SelectorFromSet(labels.Set{computev1alpha.WorkloadUIDLabel: workloadUID})
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: selector})
	}
	if err := c.List(ctx, &instList, listOpts...); err != nil {
		return fmt.Errorf("listing instances: %w", err)
	}

	// JSON/YAML: emit raw API resource and return early (before city filter).
	switch util.OutputFormat(outputFlag) {
	case util.OutputJSON:
		return util.PrintJSON(cmd.OutOrStdout(), &instList)
	case util.OutputYAML:
		return util.PrintYAML(cmd.OutOrStdout(), &instList)
	}

	// List deployments — build map deploymentName → *WorkloadDeployment.
	// Keyed by name (not UID) because the WorkloadDeploymentUIDLabel on an
	// Instance carries the edge/Karmada WD UID, which differs from the
	// project-cluster WD UID. The WD name is identical across all planes.
	var deployList computev1alpha.WorkloadDeploymentList
	if err := c.List(ctx, &deployList, client.InNamespace(util.ResourceNamespace)); err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}
	deploymentMap := make(map[string]*computev1alpha.WorkloadDeployment, len(deployList.Items))
	for i := range deployList.Items {
		d := &deployList.Items[i]
		deploymentMap[d.Name] = d
	}

	// List workloads — build map workloadUID → name.
	var wlList computev1alpha.WorkloadList
	if err := c.List(ctx, &wlList, client.InNamespace(util.ResourceNamespace)); err != nil {
		return fmt.Errorf("listing workloads: %w", err)
	}
	workloadMap := make(map[string]string, len(wlList.Items))
	for _, wl := range wlList.Items {
		workloadMap[string(wl.UID)] = wl.Name
	}

	// Build rows.
	var rows []instanceRow
	for _, inst := range instList.Items {
		wlUID := inst.Labels[computev1alpha.WorkloadUIDLabel]

		city := "unknown"
		wlName := workloadMap[wlUID]
		if wlName == "" {
			wlName = "orphaned"
		}

		// Prefer self-describing labels stamped at creation time (fast path —
		// no join needed). Fall back to the WorkloadDeployment join for older
		// instances that predate the labels.
		labelCity := inst.Labels[computev1alpha.CityCodeLabel]
		labelWLName := inst.Labels[computev1alpha.WorkloadNameLabel]

		if labelCity != "" && labelWLName != "" {
			// Both labels present: no join needed.
			city = labelCity
			wlName = labelWLName
		} else {
			// At least one label absent — fall back to WorkloadDeployment lookup.
			// Prefer the explicit WorkloadDeploymentNameLabel; fall back to
			// deriving the WD name from the Instance name for existing instances
			// that predate the label.
			depName := inst.Labels[computev1alpha.WorkloadDeploymentNameLabel]
			if depName == "" {
				depName = wdNameFromInstanceName(inst.Name)
			}
			if dep, ok := deploymentMap[depName]; ok {
				if labelCity != "" {
					city = labelCity
				} else {
					city = dep.Spec.CityCode
				}
				if labelWLName != "" {
					wlName = labelWLName
				} else if dep.Spec.WorkloadRef.Name != "" {
					wlName = dep.Spec.WorkloadRef.Name
				}
			} else {
				// Deployment not found — use whatever labels we do have.
				if labelCity != "" {
					city = labelCity
				}
				if labelWLName != "" {
					wlName = labelWLName
				}
			}
		}

		// Client-side city filter.
		if opts.city != "" && city != opts.city {
			continue
		}

		extIP := ""
		intIP := ""
		if len(inst.Status.NetworkInterfaces) > 0 {
			ni := inst.Status.NetworkInterfaces[0]
			if ni.Assignments.ExternalIP != nil {
				extIP = *ni.Assignments.ExternalIP
			}
			if ni.Assignments.NetworkIP != nil {
				intIP = *ni.Assignments.NetworkIP
			}
		}

		runtimeKind := "vm"
		if inst.Spec.Runtime.Sandbox != nil {
			runtimeKind = "sandbox"
		}

		rows = append(rows, instanceRow{
			name:        inst.Name,
			workload:    wlName,
			city:        city,
			externalIP:  extIP,
			internalIP:  intIP,
			runtimeKind: runtimeKind,
			instType:    inst.Spec.Runtime.Resources.InstanceType,
			age:         util.RelativeAge(inst.CreationTimestamp),
			status:      util.InstanceStatus(inst.Status.Conditions),
		})
	}

	// Sort: workload ASC, city ASC, name ASC.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].workload != rows[j].workload {
			return rows[i].workload < rows[j].workload
		}
		if rows[i].city != rows[j].city {
			return rows[i].city < rows[j].city
		}
		return rows[i].name < rows[j].name
	})

	if len(rows) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No instances found in project %s.\n", project)
		return nil
	}

	out := cmd.OutOrStdout()
	wide := util.OutputFormat(outputFlag) == util.OutputWide
	tw := util.NewTabWriter(out)
	if !noHeaders {
		if wide {
			_, _ = fmt.Fprintf(tw, "NAME\tWORKLOAD\tCITY\tEXTERNAL IP\tINTERNAL IP\tTYPE\tAGE\tSTATUS\tINSTANCE TYPE\n")
		} else {
			_, _ = fmt.Fprintf(tw, "NAME\tWORKLOAD\tCITY\tEXTERNAL IP\tINTERNAL IP\tTYPE\tAGE\tSTATUS\n")
		}
	}
	for _, r := range rows {
		if wide {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.name, r.workload, r.city, r.externalIP, r.internalIP, r.runtimeKind, r.age, r.status, r.instType)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.name, r.workload, r.city, r.externalIP, r.internalIP, r.runtimeKind, r.age, r.status)
		}
	}
	_ = tw.Flush()

	var available, pending, failed int
	for _, r := range rows {
		switch {
		case r.status == "Available":
			available++
		case strings.HasPrefix(r.status, "Failed"):
			failed++
		default:
			pending++
		}
	}
	_, _ = fmt.Fprintf(out, "\n%d instances — %d Available, %d Pending, %d Failed\n", len(rows), available, pending, failed)

	return nil
}

func describeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "describe <instance-name>",
		Short: "Show full details for a single instance",
		Long: `Display runtime configuration, network status, and current conditions for an
instance, including plain-English explanations of any failure states.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDescribe(cmd, args)
		},
		ValidArgsFunction: util.CompleteInstanceNames,
	}
}

func runDescribe(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	project := util.ProjectFromCmd(cmd)

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	instanceName := args[0]

	var inst computev1alpha.Instance
	if err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: instanceName}, &inst); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("instance %q not found in project %s", instanceName, project)
		}
		return fmt.Errorf("getting instance: %w", err)
	}

	// Resolve CITY, WORKLOAD, and PLACEMENT. Prefer self-describing labels
	// stamped at creation time (no join needed). Fall back to a
	// WorkloadDeployment Get when any of the labels are absent, so that older
	// instances that predate the stamp still resolve correctly.
	workloadName := "orphaned"
	city := "unknown"
	placementName := ""

	labelCity := inst.Labels[computev1alpha.CityCodeLabel]
	labelWLName := inst.Labels[computev1alpha.WorkloadNameLabel]
	labelPlacement := inst.Labels[computev1alpha.PlacementNameLabel]

	if labelCity != "" && labelWLName != "" && labelPlacement != "" {
		// All three labels present: no join needed.
		city = labelCity
		workloadName = labelWLName
		placementName = labelPlacement
	} else {
		// At least one label absent — fall back to WorkloadDeployment Get.
		// Prefer the WorkloadDeploymentNameLabel; fall back to deriving the WD
		// name from the Instance name for existing instances that lack the label.
		depName := inst.Labels[computev1alpha.WorkloadDeploymentNameLabel]
		if depName == "" {
			depName = wdNameFromInstanceName(inst.Name)
		}
		if depName != "" {
			var dep computev1alpha.WorkloadDeployment
			if err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: depName}, &dep); err == nil {
				if labelCity != "" {
					city = labelCity
				} else {
					city = dep.Spec.CityCode
				}
				if labelPlacement != "" {
					placementName = labelPlacement
				} else {
					placementName = dep.Spec.PlacementName
				}
				if labelWLName != "" {
					workloadName = labelWLName
				} else {
					workloadName = dep.Spec.WorkloadRef.Name
				}
			} else {
				// WD Get failed — use whatever labels we do have.
				if labelCity != "" {
					city = labelCity
				}
				if labelWLName != "" {
					workloadName = labelWLName
				}
				if labelPlacement != "" {
					placementName = labelPlacement
				}
			}
		}
	}

	status, detail := util.InstanceStatusDetail(inst.Status.Conditions)

	out := cmd.OutOrStdout()

	// Key-value header block.
	fmt.Fprintf(out, "%-14s %s\n", "Instance", instanceName)
	fmt.Fprintf(out, "%-14s %s\n", "Workload", workloadName)
	if placementName != "" {
		fmt.Fprintf(out, "%-14s %s\n", "Placement", placementName)
	}
	fmt.Fprintf(out, "%-14s %s\n", "City", city)
	fmt.Fprintf(out, "%-14s %s\n", "Age", util.RelativeAgeVerbose(inst.CreationTimestamp))
	fmt.Fprintf(out, "%-14s %s\n", "Status", status)
	if detail != "" {
		fmt.Fprintf(out, "%-14s %s\n", "", detail)
	}
	fmt.Fprintf(out, "\n")

	// Runtime section.
	fmt.Fprintf(out, "Runtime\n")
	if inst.Spec.Runtime.Sandbox != nil {
		sb := inst.Spec.Runtime.Sandbox
		if len(sb.Containers) > 0 {
			ctr := sb.Containers[0]
			fmt.Fprintf(out, "  %-12s %s\n", "Image:", ctr.Image)

			if len(ctr.Env) > 0 {
				var envStrs []string
				for _, e := range ctr.Env {
					envStrs = append(envStrs, formatEnvVar(e))
				}
				fmt.Fprintf(out, "  %-12s %s\n", "Env:", strings.Join(envStrs, ", "))
			}

			if len(ctr.Ports) > 0 {
				var portStrs []string
				for _, p := range ctr.Ports {
					proto := "TCP"
					if p.Protocol != nil {
						proto = string(*p.Protocol)
					}
					portStrs = append(portStrs, fmt.Sprintf("%d/%s", p.Port, proto))
				}
				fmt.Fprintf(out, "  %-12s %s\n", "Ports:", strings.Join(portStrs, ", "))
			}
		}
		fmt.Fprintf(out, "  %-12s %s\n", "Type:", inst.Spec.Runtime.Resources.InstanceType)
	} else {
		fmt.Fprintf(out, "  %-12s %s\n", "Type:", "virtual-machine")
		fmt.Fprintf(out, "  %-12s %s\n", "Instance type:", inst.Spec.Runtime.Resources.InstanceType)
	}
	fmt.Fprintf(out, "\n")

	// Network block.
	fmt.Fprintf(out, "Network\n")
	networkLine := networkSummary(inst.Status.NetworkInterfaces)
	fmt.Fprintf(out, "  %s\n", networkLine)
	fmt.Fprintf(out, "\n")

	// Next steps if not available and quota exceeded.
	quotaCond := util.FindCondition(inst.Status.Conditions, computev1alpha.InstanceQuotaGranted)
	if status != "Available" && quotaCond != nil && quotaCond.Reason == computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded {
		fmt.Fprintf(out, "Next steps\n")
		fmt.Fprintf(out, "  datumctl compute scale %s --min=2\n", workloadName)
		fmt.Fprintf(out, "  datumctl compute quota\n")
	}

	return nil
}

// networkSummary returns a human-readable network status line.
func networkSummary(ifaces []computev1alpha.InstanceNetworkInterfaceStatus) string {
	if len(ifaces) == 0 {
		return "Waiting for addresses (not yet scheduled)"
	}
	ni := ifaces[0]
	if ni.Assignments.ExternalIP == nil && ni.Assignments.NetworkIP == nil {
		return "Waiting for addresses (not yet scheduled)"
	}
	extIP := "not assigned"
	if ni.Assignments.ExternalIP != nil {
		extIP = *ni.Assignments.ExternalIP
	}
	intIP := "not assigned"
	if ni.Assignments.NetworkIP != nil {
		intIP = *ni.Assignments.NetworkIP
	}
	return fmt.Sprintf("External: %s  Internal: %s", extIP, intIP)
}

// wdNameFromInstanceName derives the WorkloadDeployment name from an Instance
// name by stripping the trailing "-<ordinal>" suffix. Instance names follow the
// convention "<wd-name>-<ordinal>" (e.g. "my-api-default-dfw-0" → "my-api-default-dfw").
// This is used as a fallback when WorkloadDeploymentNameLabel is absent on older
// instances that predate that label.
//
// If the name has no trailing numeric segment (not a standard instance name),
// the original name is returned unchanged so callers can handle it gracefully.
func wdNameFromInstanceName(instanceName string) string {
	idx := strings.LastIndex(instanceName, "-")
	if idx < 0 {
		return instanceName
	}
	suffix := instanceName[idx+1:]
	// The suffix must be entirely numeric digits to qualify as an ordinal.
	for _, r := range suffix {
		if !unicode.IsDigit(r) {
			return instanceName
		}
	}
	if suffix == "" {
		return instanceName
	}
	return instanceName[:idx]
}

// formatEnvVar renders a single EnvVar for display.
func formatEnvVar(e corev1.EnvVar) string {
	if e.ValueFrom != nil {
		if e.ValueFrom.SecretKeyRef != nil {
			return e.Name + " (from secret)"
		}
		if e.ValueFrom.ConfigMapKeyRef != nil {
			return e.Name + " (from configmap)"
		}
	}
	return e.Name + "=" + e.Value
}
