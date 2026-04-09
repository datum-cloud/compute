package controller

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	deploymentWorkloadUIDIndex = "deploymentWorkloadUIDIndex"
	workloadNetworksIndex      = "workloadNetworksIndex"
	deploymentLocationIndex    = "deploymentLocationIndex"
)

func AddIndexers(ctx context.Context, mgr mcmanager.Manager) error {
	return errors.Join(
		addWorkloadDeploymentIndexers(ctx, mgr),
		addWorkloadIndexers(ctx, mgr),
	)
}

func addWorkloadDeploymentIndexers(ctx context.Context, mgr mcmanager.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &computev1alpha.WorkloadDeployment{}, deploymentWorkloadUIDIndex, deploymentWorkloadUIDIndexFunc); err != nil {
		return fmt.Errorf("failed to add workload deployment indexer %q: %w", deploymentWorkloadUIDIndex, err)
	}

	// Index workload deployments by location
	if err := mgr.GetFieldIndexer().IndexField(ctx, &computev1alpha.WorkloadDeployment{}, deploymentLocationIndex, deploymentLocationIndexFunc); err != nil {
		return fmt.Errorf("failed to add workload deployment indexer %q: %w", deploymentLocationIndex, err)
	}

	return nil
}

func deploymentWorkloadUIDIndexFunc(o client.Object) []string {
	return []string{
		string(o.(*computev1alpha.WorkloadDeployment).Spec.WorkloadRef.UID),
	}
}

func deploymentLocationIndexFunc(o client.Object) []string {
	deployment := o.(*computev1alpha.WorkloadDeployment)
	if deployment.Status.Location == nil {
		return nil
	}

	return []string{
		types.NamespacedName{
			Namespace: deployment.Status.Location.Namespace,
			Name:      deployment.Status.Location.Name,
		}.String(),
	}
}

func addWorkloadIndexers(ctx context.Context, mgr mcmanager.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &computev1alpha.Workload{}, workloadNetworksIndex, workloadNetworksIndexFunc); err != nil {
		return fmt.Errorf("failed to add workload indexer %q: %w", workloadNetworksIndex, err)
	}

	return nil
}

func workloadNetworksIndexFunc(o client.Object) []string {
	workload := o.(*computev1alpha.Workload)

	networks := make([]string, 0, len(workload.Spec.Template.Spec.NetworkInterfaces))
	for _, network := range workload.Spec.Template.Spec.NetworkInterfaces {
		namespacedName := types.NamespacedName{
			Namespace: network.Network.Namespace,
			Name:      network.Network.Name,
		}

		if namespacedName.Namespace == "" {
			namespacedName.Namespace = workload.GetNamespace()
		}

		networks = append(networks, namespacedName.String())
	}

	return networks
}
