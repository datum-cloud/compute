// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
)

// completeFromWorkload completes a flag from the workload the command names.
func completeFromWorkload(candidates func(context.Context, client.Client, string) ([]string, error)) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		kube, err := util.NewClient(util.ProjectFromCmd(cmd))
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names, _ := candidates(context.Background(), kube, args[0])
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// instanceNames returns a workload's instances by the short names line
// prefixes show.
func instanceNames(ctx context.Context, kube client.Client, workload string) ([]string, error) {
	var list computev1alpha.InstanceList
	if err := kube.List(ctx, &list,
		client.InNamespace(util.ResourceNamespace),
		client.MatchingLabels{computev1alpha.WorkloadNameLabel: workload},
	); err != nil {
		return nil, err
	}
	names := make([]string, len(list.Items))
	for i, inst := range list.Items {
		names[i] = strings.TrimPrefix(inst.Name, workload+"-")
	}
	return names, nil
}

func containerNames(ctx context.Context, kube client.Client, workload string) ([]string, error) {
	var wl computev1alpha.Workload
	if err := kube.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: workload}, &wl); err != nil {
		return nil, err
	}
	return containersOf(&wl), nil
}

// containersOf returns a workload's container names, or nil for a VM workload.
func containersOf(wl *computev1alpha.Workload) []string {
	sandbox := wl.Spec.Template.Spec.Runtime.Sandbox
	if sandbox == nil {
		return nil
	}
	names := make([]string, len(sandbox.Containers))
	for i, c := range sandbox.Containers {
		names[i] = c.Name
	}
	return names
}
