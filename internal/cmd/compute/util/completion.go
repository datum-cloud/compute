package util

import (
	"context"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/datumctl/plugin"
)

// CompleteInstanceNames is a ValidArgsFunction that lists instance names from the API.
func CompleteInstanceNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	project := ProjectFromCmd(cmd)
	c, err := NewClient(project)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var list computev1alpha.InstanceList
	if err := c.List(context.Background(), &list, client.InNamespace(ResourceNamespace)); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, len(list.Items))
	for i, inst := range list.Items {
		names[i] = inst.Name
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// CompleteCityCodes is a ValidArgsFunction that returns unique city codes from
// all WorkloadDeployments in the project.
func CompleteCityCodes(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	project := ProjectFromCmd(cmd)
	c, err := NewClient(project)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var list computev1alpha.WorkloadDeploymentList
	if err := c.List(context.Background(), &list, client.InNamespace(ResourceNamespace)); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	seen := make(map[string]bool)
	var codes []string
	for _, d := range list.Items {
		if !seen[d.Spec.CityCode] {
			seen[d.Spec.CityCode] = true
			codes = append(codes, d.Spec.CityCode)
		}
	}
	return codes, cobra.ShellCompDirectiveNoFileComp
}

// CompleteOutputFormats returns a ValidArgsFunction that completes -o/--output
// to the given allowed values.
func CompleteOutputFormats(allowed ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return allowed, cobra.ShellCompDirectiveNoFileComp
	}
}

// CompleteWorkloadNames is a ValidArgsFunction that lists workload names from
// the API. It suppresses file completion in all cases so the shell never falls
// back to filename completion when completing a workload-name argument.
func CompleteWorkloadNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
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

// CompleteWorkloadNamesAndFlags lists workload names from the API and also
// surfaces the command's own flags as completions. Used by commands where flags
// are the primary input (e.g. deploy) so that plain <TAB> offers flags without
// requiring the user to type "--" first.
var CompleteWorkloadNamesAndFlags = plugin.WithFlagCompletion(CompleteWorkloadNames)
