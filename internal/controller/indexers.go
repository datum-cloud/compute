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
	// deploymentCityCodeIndex indexes WorkloadDeployments by their Spec.CityCode
	// so that SubnetClaim/Subnet watches can efficiently find the deployments
	// that target the same city as a changed networking resource.
	deploymentCityCodeIndex = "deploymentCityCodeIndex"
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

	if err := mgr.GetFieldIndexer().IndexField(ctx, &computev1alpha.WorkloadDeployment{}, deploymentCityCodeIndex, deploymentCityCodeIndexFunc); err != nil {
		return fmt.Errorf("failed to add workload deployment indexer %q: %w", deploymentCityCodeIndex, err)
	}

	return nil
}

func deploymentWorkloadUIDIndexFunc(o client.Object) []string {
	// Skip deployments without a workload UID: indexing them under the empty
	// key would make them matchable by a GC query built from a corrupt (empty)
	// UID, mirroring deploymentCityCodeIndexFunc.
	uid := string(o.(*computev1alpha.WorkloadDeployment).Spec.WorkloadRef.UID)
	if uid == "" {
		return nil
	}
	return []string{uid}
}

func deploymentCityCodeIndexFunc(o client.Object) []string {
	deployment := o.(*computev1alpha.WorkloadDeployment)
	if deployment.Spec.CityCode == "" {
		return nil
	}
	return []string{deployment.Spec.CityCode}
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
