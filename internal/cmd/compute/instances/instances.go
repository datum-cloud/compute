package instances

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	corev1 "k8s.io/api/core/v1"
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

  # Describe a single instance
  datumctl compute instances describe api-dfw-0`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd, opts)
		},
	}

	cmd.Flags().StringVar(&opts.workload, "workload", "", "Filter instances to a specific workload")
	cmd.Flags().StringVar(&opts.city, "city", "", "Filter instances to a specific city")

	cmd.AddCommand(describeCommand())

	return cmd
}

type instanceRow struct {
	name       string
	workload   string
	city       string
	externalIP string
	internalIP string
	instType   string
	age        string
	status     string
}

func runList(cmd *cobra.Command, opts *listOptions) error {
	ctx := context.Background()
	project := util.ProjectFromCmd(cmd)

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

	// List deployments — build map deploymentUID → *WorkloadDeployment.
	var deployList computev1alpha.WorkloadDeploymentList
	if err := c.List(ctx, &deployList, client.InNamespace(util.ResourceNamespace)); err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}
	deploymentMap := make(map[string]*computev1alpha.WorkloadDeployment, len(deployList.Items))
	for i := range deployList.Items {
		d := &deployList.Items[i]
		deploymentMap[string(d.UID)] = d
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
		depUID := inst.Labels[computev1alpha.WorkloadDeploymentUIDLabel]
		wlUID := inst.Labels[computev1alpha.WorkloadUIDLabel]

		city := "unknown"
		wlName := workloadMap[wlUID]
		if wlName == "" {
			wlName = "orphaned"
		}
		if dep, ok := deploymentMap[depUID]; ok {
			city = dep.Spec.CityCode
			if dep.Spec.WorkloadRef.Name != "" {
				wlName = dep.Spec.WorkloadRef.Name
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

		rows = append(rows, instanceRow{
			name:       inst.Name,
			workload:   wlName,
			city:       city,
			externalIP: extIP,
			internalIP: intIP,
			instType:   inst.Spec.Runtime.Resources.InstanceType,
			age:        util.RelativeAge(inst.CreationTimestamp),
			status:     util.InstanceStatus(inst.Status.Conditions),
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
	tw := util.NewTabWriter(out)
	fmt.Fprintf(tw, "NAME\tWORKLOAD\tCITY\tEXTERNAL IP\tINTERNAL IP\tTYPE\tAGE\tSTATUS\n")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.name, r.workload, r.city, r.externalIP, r.internalIP, r.instType, r.age, r.status)
	}
	tw.Flush()

	running := 0
	for _, r := range rows {
		if r.status == "Running" {
			running++
		}
	}
	pending := len(rows) - running
	fmt.Fprintf(out, "\n%d instances — %d Running, %d Pending, 0 Failed\n", len(rows), running, pending)

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

	// Look up deployment.
	deploymentUID := inst.Labels[computev1alpha.WorkloadDeploymentUIDLabel]
	workloadName := "orphaned"
	city := "unknown"
	placementName := ""

	if deploymentUID != "" {
		depSelector := labels.SelectorFromSet(labels.Set{computev1alpha.WorkloadDeploymentUIDLabel: deploymentUID})
		var depList computev1alpha.WorkloadDeploymentList
		if err := c.List(ctx, &depList, client.InNamespace(util.ResourceNamespace), client.MatchingLabelsSelector{Selector: depSelector}); err == nil && len(depList.Items) > 0 {
			dep := depList.Items[0]
			city = dep.Spec.CityCode
			placementName = dep.Spec.PlacementName
			workloadName = dep.Spec.WorkloadRef.Name
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

	// Next steps if not running and quota exceeded.
	quotaCond := util.FindCondition(inst.Status.Conditions, computev1alpha.InstanceQuotaGranted)
	if status != "Running" && quotaCond != nil && quotaCond.Reason == computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded {
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
