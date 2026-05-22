package restart

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
)

func Command() *cobra.Command {
	var city string

	cmd := &cobra.Command{
		Use:   "restart <workload-name>",
		Short: "Trigger a rolling restart of a workload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestart(cmd, args, city)
		},
		ValidArgsFunction: util.CompleteWorkloadNames,
	}

	cmd.Flags().StringVar(&city, "city", "", "Restart only instances in a specific city")

	return cmd
}

func runRestart(cmd *cobra.Command, args []string, city string) error {
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

	restartedAt := time.Now().UTC().Format(time.RFC3339)
	out := cmd.OutOrStdout()

	if city == "" {
		// Restart all placements by annotating the workload template.
		if workload.Spec.Template.Annotations == nil {
			workload.Spec.Template.Annotations = make(map[string]string)
		}
		workload.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = restartedAt

		if err := c.Update(ctx, &workload); err != nil {
			return fmt.Errorf("updating workload: %w", err)
		}

		fmt.Fprintf(out,
			"Restarting workload %q — rolling restart initiated.\nRun 'datumctl compute rollout %s' to watch progress.\n",
			workloadName, workloadName,
		)
		return nil
	}

	// Restart only deployments in the given city.
	selector := labels.SelectorFromSet(labels.Set{
		computev1alpha.WorkloadUIDLabel: string(workload.UID),
	})
	var deployList computev1alpha.WorkloadDeploymentList
	if err := c.List(ctx, &deployList,
		client.InNamespace(util.ResourceNamespace),
		client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}

	var matched []computev1alpha.WorkloadDeployment
	for _, d := range deployList.Items {
		if d.Spec.CityCode == city {
			matched = append(matched, d)
		}
	}

	if len(matched) == 0 {
		return fmt.Errorf("no deployment found for workload %q in city %q", workloadName, city)
	}

	for i := range matched {
		if matched[i].Spec.Template.Annotations == nil {
			matched[i].Spec.Template.Annotations = make(map[string]string)
		}
		matched[i].Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = restartedAt

		if err := c.Update(ctx, &matched[i]); err != nil {
			return fmt.Errorf("updating deployment in %s: %w", city, err)
		}
	}

	fmt.Fprintf(out,
		"Restarting workload %q in %s — rolling restart initiated.\nRun 'datumctl compute rollout %s' to watch progress.\n",
		workloadName, city, workloadName,
	)
	return nil
}
