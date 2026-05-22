package status

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
)

func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <workload-name>",
		Short: "Show the health and placement status of a workload",
		Long: `Display the current health status of a workload with city-by-city replica
counts and plain-English explanations of any degraded conditions.`,
		Args:    cobra.ExactArgs(1),
		Example: `  datumctl compute status api`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, args)
		},
	}

	return cmd
}

func runStatus(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	project := util.ProjectFromCmd(cmd)

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	workloadName := args[0]

	// Fetch workload.
	var workload computev1alpha.Workload
	if err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: workloadName}, &workload); err != nil {
		if k8serrors.IsNotFound(err) {
			fmt.Fprintf(cmd.ErrOrStderr(), "workload %q not found in project %s\n", workloadName, project)
			return fmt.Errorf("workload not found")
		}
		return fmt.Errorf("getting workload: %w", err)
	}

	// List deployments for this workload.
	selector := labels.SelectorFromSet(labels.Set{computev1alpha.WorkloadUIDLabel: string(workload.UID)})
	var deployList computev1alpha.WorkloadDeploymentList
	if err := c.List(ctx, &deployList, client.InNamespace(util.ResourceNamespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}

	// Derive image.
	image := "(virtual machine)"
	if workload.Spec.Template.Spec.Runtime.Sandbox != nil &&
		len(workload.Spec.Template.Spec.Runtime.Sandbox.Containers) > 0 {
		image = workload.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image
	}

	instanceType := workload.Spec.Template.Spec.Runtime.Resources.InstanceType
	age := util.RelativeAgeVerbose(workload.CreationTimestamp)

	// Fetch revision ConfigMap.
	revision := "—"
	var cm corev1.ConfigMap
	cmName := "compute.datumapis.com-revision-history." + workloadName
	if err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: cmName}, &cm); err == nil {
		if v, ok := cm.Annotations["compute.datumapis.com/current-revision"]; ok {
			revision = v
		}
	}
	// If not found or any error, revision stays "—".

	// Compute totals.
	var totalDesired, totalReady int32
	for _, d := range deployList.Items {
		totalDesired += d.Status.DesiredReplicas
		totalReady += d.Status.ReadyReplicas
	}

	health := util.WorkloadHealth(workload.Status.Conditions, totalReady, totalDesired)

	out := cmd.OutOrStdout()

	// Header block — two-column layout.
	fmt.Fprintf(out, "%-12s %-31s project: %s\n", "Workload", workloadName, project)
	fmt.Fprintf(out, "%-12s %s\n", "Image", image)
	fmt.Fprintf(out, "%-12s %-31s Revision #%s\n", "Updated", age, revision)
	fmt.Fprintf(out, "\n")
	fmt.Fprintf(out, "%-12s %s\n", "Health", health)
	fmt.Fprintf(out, "\n")

	if len(deployList.Items) == 0 {
		fmt.Fprintf(out, "  No placements configured.\n")
		return nil
	}

	// Placement table — grouped by placement name.
	tw := util.NewTabWriter(out)
	fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", "", "CITY", "READY", "DESIRED", "TYPE")

	// Group deployments by placement name preserving order from workload spec.
	type deployGroup struct {
		name        string
		deployments []computev1alpha.WorkloadDeployment
	}
	var groups []deployGroup
	groupIndex := map[string]int{}
	for _, d := range deployList.Items {
		pn := d.Spec.PlacementName
		if idx, ok := groupIndex[pn]; ok {
			groups[idx].deployments = append(groups[idx].deployments, d)
		} else {
			groupIndex[pn] = len(groups)
			groups = append(groups, deployGroup{name: pn, deployments: []computev1alpha.WorkloadDeployment{d}})
		}
	}

	// Track degraded deployments for the detail block.
	type degradedEntry struct {
		city       string
		deployment computev1alpha.WorkloadDeployment
	}
	var degraded []degradedEntry

	for _, g := range groups {
		for i, d := range g.deployments {
			placementLabel := ""
			if i == 0 {
				placementLabel = g.name
			}
			readyStr := fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, d.Status.Replicas)
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%s\n",
				placementLabel,
				d.Spec.CityCode,
				readyStr,
				d.Status.DesiredReplicas,
				instanceType,
			)
			if d.Status.ReadyReplicas < d.Status.DesiredReplicas {
				degraded = append(degraded, degradedEntry{city: d.Spec.CityCode, deployment: d})
			}
		}
	}
	_ = tw.Flush()

	if len(degraded) == 0 {
		return nil
	}

	fmt.Fprintf(out, "\n")

	// For each degraded deployment, find the first unhealthy instance and get its detail.
	type degradedDetail struct {
		city         string
		count        int32
		statusLine   string
		detailMsg    string
		quotaExceed  bool
	}
	var details []degradedDetail
	anyQuotaExceeded := false

	for _, de := range degraded {
		depUID := string(de.deployment.UID)
		depSelector := labels.SelectorFromSet(labels.Set{computev1alpha.WorkloadDeploymentUIDLabel: depUID})
		var instList computev1alpha.InstanceList
		if err := c.List(ctx, &instList, client.InNamespace(util.ResourceNamespace), client.MatchingLabelsSelector{Selector: depSelector}); err != nil {
			// Skip detail on error.
			continue
		}

		var statusLine, detailMsg string
		quotaExceed := false
		for _, inst := range instList.Items {
			readyCond := util.FindCondition(inst.Status.Conditions, computev1alpha.InstanceReady)
			if readyCond == nil || readyCond.Status != "True" {
				s, d := util.InstanceStatusDetail(inst.Status.Conditions)
				statusLine = s
				detailMsg = d
				// Check if quota exceeded.
				qc := util.FindCondition(inst.Status.Conditions, computev1alpha.InstanceQuotaGranted)
				if qc != nil && qc.Reason == computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded {
					quotaExceed = true
					anyQuotaExceeded = true
				}
				break
			}
		}

		short := describeStatusShort(statusLine, de.deployment.Status.DesiredReplicas-de.deployment.Status.ReadyReplicas)
		details = append(details, degradedDetail{
			city:        de.city,
			count:       de.deployment.Status.DesiredReplicas - de.deployment.Status.ReadyReplicas,
			statusLine:  short,
			detailMsg:   detailMsg,
			quotaExceed: quotaExceed,
		})
	}

	for _, dd := range details {
		fmt.Fprintf(out, "  %s: %d instances could not start — %s\n", dd.city, dd.count, dd.statusLine)
		if dd.detailMsg != "" {
			fmt.Fprintf(out, "    %s\n", dd.detailMsg)
		}
	}

	// Next steps block.
	fmt.Fprintf(out, "\n  Next steps:\n")
	if anyQuotaExceeded {
		fmt.Fprintf(out, "    Reduce replicas:   datumctl compute scale %s --min=2\n", workloadName)
		fmt.Fprintf(out, "    Check quota:       datumctl compute quota\n")
	}
	fmt.Fprintf(out, "    View instances:    datumctl compute instances --workload=%s\n", workloadName)

	return nil
}

// describeStatusShort converts a full status line into a short degradation phrase.
func describeStatusShort(statusLine string, count int32) string {
	_ = count
	switch {
	case strings.Contains(statusLine, "quota exceeded"):
		return "quota exceeded"
	case strings.Contains(statusLine, "network provisioning in progress"):
		return "network provisioning in progress"
	case strings.Contains(statusLine, "network provisioning"):
		return "network provisioning"
	case statusLine == "Starting":
		return "starting"
	case statusLine == "Stopping":
		return "stopping"
	default:
		return statusLine
	}
}
