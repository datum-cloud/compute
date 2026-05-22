package quota

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
)

// availablePattern matches messages like "2 CPU available in IAD." or
// "2.5 available in DFW" to extract the numeric available quantity.
var availablePattern = regexp.MustCompile(`(\d+(?:\.\d+)?)\s+\w+\s+available`)

func Command() *cobra.Command {
	var city string
	var constrained bool

	cmd := &cobra.Command{
		Use:   "quota",
		Short: "Show compute quota usage for the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQuota(cmd, city, constrained)
		},
	}

	cmd.Flags().StringVar(&city, "city", "", "Narrow output to a specific city")
	cmd.Flags().BoolVar(&constrained, "constrained", false, "Show only constrained resources")

	return cmd
}

type groupKey struct {
	city         string
	instanceType string
}

func runQuota(cmd *cobra.Command, filterCity string, constrained bool) error {
	project := util.ProjectFromCmd(cmd)

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	ctx := context.Background()

	// List all instances in the project.
	var instList computev1alpha.InstanceList
	if err := c.List(ctx, &instList, client.InNamespace(util.ResourceNamespace)); err != nil {
		return fmt.Errorf("listing instances: %w", err)
	}

	// List all deployments to build a UID → city/instanceType lookup.
	var deployList computev1alpha.WorkloadDeploymentList
	if err := c.List(ctx, &deployList, client.InNamespace(util.ResourceNamespace)); err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}

	// Build map: deploymentUID → deployment.
	deployByUID := make(map[string]computev1alpha.WorkloadDeployment, len(deployList.Items))
	for _, d := range deployList.Items {
		deployByUID[string(d.UID)] = d
	}

	type groupData struct {
		count    int
		atLimit  bool
		limitMsg string
	}

	groups := make(map[groupKey]*groupData)

	for _, inst := range instList.Items {
		// Resolve city from deployment label.
		depUID := inst.Labels[computev1alpha.WorkloadDeploymentUIDLabel]
		dep, ok := deployByUID[depUID]
		if !ok {
			continue
		}
		city := dep.Spec.CityCode
		instanceType := dep.Spec.Template.Spec.Runtime.Resources.InstanceType
		if instanceType == "" {
			instanceType = "unknown"
		}

		k := groupKey{city: city, instanceType: instanceType}
		gd := groups[k]
		if gd == nil {
			gd = &groupData{}
			groups[k] = gd
		}
		gd.count++

		// Check quota condition.
		quotaCond := util.FindCondition(inst.Status.Conditions, computev1alpha.InstanceQuotaGranted)
		if quotaCond != nil &&
			quotaCond.Status == metav1.ConditionFalse &&
			quotaCond.Reason == computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded {
			gd.atLimit = true
			if quotaCond.Message != "" {
				gd.limitMsg = quotaCond.Message
			}
		}
	}

	// Build sorted keys.
	var keys []groupKey
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].city != keys[j].city {
			return keys[i].city < keys[j].city
		}
		return keys[i].instanceType < keys[j].instanceType
	})

	// Also list workload deployments to pick up zero-instance cities (not needed per spec, skip).

	out := cmd.OutOrStdout()

	// Filter by city.
	if filterCity != "" {
		var filtered []groupKey
		for _, k := range keys {
			if k.city == filterCity {
				filtered = append(filtered, k)
			}
		}
		keys = filtered
	}

	// Before filtering by constrained, check if there are any instances at all.
	if len(instList.Items) == 0 {
		_, _ = fmt.Fprint(out, "No instances running. No quota consumption to display.\n")
		return nil
	}

	// Filter by constrained.
	if constrained {
		var filtered []groupKey
		for _, k := range keys {
			if groups[k].atLimit {
				filtered = append(filtered, k)
			}
		}
		if len(filtered) == 0 {
			_, _ = fmt.Fprint(out, "No constrained resources found.\n")
			return nil
		}
		keys = filtered
	}

	fmt.Fprintf(out, "Quota usage for project %s\n\n", project)

	tw := util.NewTabWriter(out)
	fmt.Fprintf(tw, "CITY\tTYPE\tIN USE\tLIMIT\tAVAILABLE\n")

	for _, k := range keys {
		gd := groups[k]

		limit := "—"
		available := "—"
		if gd.limitMsg != "" {
			avail, ok := parseAvailable(gd.limitMsg)
			if ok {
				available = strconv.Itoa(avail)
				limit = strconv.Itoa(gd.count + avail)
			}
		}

		cityLabel := k.city
		if gd.atLimit {
			cityLabel += " [at limit]"
		}

		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", cityLabel, k.instanceType, gd.count, limit, available)
	}
	_ = tw.Flush()

	// Sort and print zero-instance groups (no quota consumed, nothing to show for "constrained").
	// Per spec these are not interesting for the quota view, so we skip them.

	_, _ = fmt.Fprint(out, "\nNote: limit information is derived from quota conditions on instances.\nRun 'datumctl quota' for full project quota management.\n")

	return nil
}

// parseAvailable extracts the integer available count from a quota condition
// message such as "Requested 4 CPU. 2 CPU available in IAD."
func parseAvailable(msg string) (int, bool) {
	m := availablePattern.FindStringSubmatch(msg)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return int(f), true
}
