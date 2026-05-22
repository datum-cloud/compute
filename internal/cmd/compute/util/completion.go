package util

import (
	"context"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// CompleteWorkloadNames is a ValidArgsFunction that lists workload names from
// the API. It suppresses file completion in all cases so the shell never falls
// back to filename completion when completing a workload-name argument.
func CompleteWorkloadNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	// Only complete the first positional argument.
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	project := ProjectFromCmd(cmd)
	c, err := NewClient(project)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var list computev1alpha.WorkloadList
	if err := c.List(context.Background(), &list, client.InNamespace(ResourceNamespace)); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, len(list.Items))
	for i, w := range list.Items {
		names[i] = w.Name
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
