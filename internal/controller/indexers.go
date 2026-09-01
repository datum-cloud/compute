package controller

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/referenceddata"
)

const (
	deploymentWorkloadUIDIndex = "deploymentWorkloadUIDIndex"
	workloadNetworksIndex      = "workloadNetworksIndex"

	deploymentLocationIndex = "deploymentLocationIndex"

	// wdRefersToConfigMapIndex indexes WorkloadDeployments by the ConfigMap
	// names they reference (via env.ValueFrom, envFrom, or volumes). Used by
	// the ReferencedDataController's source-watch to re-queue WDs when a source
	// ConfigMap changes (rotation).
	wdRefersToConfigMapIndex = "wdRefersToConfigMapIndex"

	// wdRefersToSecretIndex indexes WorkloadDeployments by the Secret names
	// they reference. Used by the ReferencedDataController's source-watch.
	wdRefersToSecretIndex = "wdRefersToSecretIndex"
)

func AddIndexers(ctx context.Context, mgr mcmanager.Manager) error {
	return errors.Join(
		addWorkloadDeploymentIndexers(ctx, mgr),
		addWorkloadIndexers(ctx, mgr),
		addInstanceIndexers(ctx, mgr),
	)
}

func addInstanceIndexers(_ context.Context, _ mcmanager.Manager) error {
	// No instance-level indexes are currently registered. The companion-watch
	// enqueue handler (enqueueInstancesInNamespace) lists all Instances in the
	// namespace directly, which is correct because companions are shared across
	// all Instances in a namespace.
	return nil
}

func addWorkloadDeploymentIndexers(ctx context.Context, mgr mcmanager.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &computev1alpha.WorkloadDeployment{}, deploymentWorkloadUIDIndex, deploymentWorkloadUIDIndexFunc); err != nil {
		return fmt.Errorf("failed to add workload deployment indexer %q: %w", deploymentWorkloadUIDIndex, err)
	}

	// Index workload deployments by location
	if err := mgr.GetFieldIndexer().IndexField(ctx, &computev1alpha.WorkloadDeployment{}, deploymentLocationIndex, deploymentLocationIndexFunc); err != nil {
		return fmt.Errorf("failed to add workload deployment indexer %q: %w", deploymentLocationIndex, err)
	}

	// Index WDs by the ConfigMap and Secret names they reference, so the
	// ReferencedDataController can re-queue WDs when sources change (rotation).
	if err := mgr.GetFieldIndexer().IndexField(ctx, &computev1alpha.WorkloadDeployment{}, wdRefersToConfigMapIndex, wdRefersToConfigMapIndexFunc); err != nil {
		return fmt.Errorf("failed to add workload deployment indexer %q: %w", wdRefersToConfigMapIndex, err)
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &computev1alpha.WorkloadDeployment{}, wdRefersToSecretIndex, wdRefersToSecretIndexFunc); err != nil {
		return fmt.Errorf("failed to add workload deployment indexer %q: %w", wdRefersToSecretIndex, err)
	}

	return nil
}

func deploymentWorkloadUIDIndexFunc(o client.Object) []string {
	// Skip deployments without a workload UID: indexing them under the empty
	// key would make them matchable by a GC query built from a corrupt (empty)
	// UID.
	uid := string(o.(*computev1alpha.WorkloadDeployment).Spec.WorkloadRef.UID)
	if uid == "" {
		return nil
	}
	return []string{uid}
}

func deploymentLocationIndexFunc(o client.Object) []string {
	deployment := o.(*computev1alpha.WorkloadDeployment)
	if deployment.Spec.LocationRef.Name == "" {
		return nil
	}

	// Locations are cluster-scoped, so the name alone identifies one.
	return []string{deployment.Spec.LocationRef.Name}
}

func addWorkloadIndexers(ctx context.Context, mgr mcmanager.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &computev1alpha.Workload{}, workloadNetworksIndex, workloadNetworksIndexFunc); err != nil {
		return fmt.Errorf("failed to add workload indexer %q: %w", workloadNetworksIndex, err)
	}

	return nil
}

// wdRefersToConfigMapIndexFunc returns the namespace/name keys of all
// ConfigMaps referenced by a WorkloadDeployment's template.
func wdRefersToConfigMapIndexFunc(o client.Object) []string {
	wd := o.(*computev1alpha.WorkloadDeployment)
	refs := referenceddata.CollectFromTemplate(wd.Namespace, wd.Spec.Template)
	var keys []string
	for _, ref := range refs {
		if ref.Kind == "ConfigMap" {
			keys = append(keys, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}.String())
		}
	}
	return keys
}

// wdRefersToSecretIndexFunc returns the namespace/name keys of all Secrets
// referenced by a WorkloadDeployment's template.
func wdRefersToSecretIndexFunc(o client.Object) []string {
	wd := o.(*computev1alpha.WorkloadDeployment)
	refs := referenceddata.CollectFromTemplate(wd.Namespace, wd.Spec.Template)
	var keys []string
	for _, ref := range refs {
		if ref.Kind == kindSecret {
			keys = append(keys, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}.String())
		}
	}
	return keys
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
