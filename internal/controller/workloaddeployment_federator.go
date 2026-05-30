// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

const (
	// federatorFinalizer is added to project-namespace WorkloadDeployments that
	// have been federated to the downstream control plane. It ensures we clean up
	// the downstream object and any orphaned PropagationPolicies before the project
	// object is permanently deleted.
	federatorFinalizer = "compute.datumapis.com/federator"

	// cityCodeLabel is applied to WorkloadDeployments in the downstream namespace
	// and is used by PropagationPolicy selectors to route them to the correct
	// POP-cell clusters. Downstream Cluster objects are expected to carry this
	// label with their city-code value.
	cityCodeLabel = "topology.datum.net/city-code"

	// kindWorkloadDeployment is the Kind string for WorkloadDeployment resources.
	kindWorkloadDeployment = "WorkloadDeployment"
)

// WorkloadDeploymentFederator replicates WorkloadDeployments from project
// namespaces into the downstream control plane so it can propagate them to the
// appropriate POP-cell clusters.
//
// For each WorkloadDeployment the controller:
//  1. Determines the downstream namespace via the ns-<project-namespace-uid>
//     convention (matching the MappedNamespaceResourceStrategy used by
//     go.datum.net/network-services-operator; this logic will migrate to Milo
//     once the shared library is promoted).
//  2. Upserts a corresponding WorkloadDeployment in that downstream namespace,
//     stamped with label topology.datum.net/city-code=<cityCode>.
//  3. Lazily creates a PropagationPolicy per city code per downstream namespace
//     that selects WorkloadDeployments by the city-code label and targets
//     clusters carrying the same label. The PP is deleted once no deployments
//     with that city code remain in the namespace.
//  4. Reads the aggregated status from the downstream control plane and writes
//     it back to the project-namespace object.
//  5. On deletion: removes the downstream WorkloadDeployment and cleans up
//     unused PropagationPolicies.
type WorkloadDeploymentFederator struct {
	mgr mcmanager.Manager
	// FederationClient is a client pointed at the Karmada federation control
	// plane (the federation hub that the management controllers read and write
	// through). The caller (cmd/main.go) constructs it from --federation-kubeconfig.
	FederationClient client.Client
	finalizers       finalizer.Finalizers
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list

func (r *WorkloadDeploymentFederator) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	if r.FederationClient == nil {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	ctx = mccontext.WithCluster(ctx, req.ClusterName)

	var deployment computev1alpha.WorkloadDeployment
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	finalizationResult, err := r.finalizers.Finalize(ctx, &deployment)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to finalize: %w", err)
	}
	if finalizationResult.Updated {
		if err = cl.GetClient().Update(ctx, &deployment); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update based on finalization result: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if !deployment.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	logger.Info("federating deployment to downstream control plane")

	// Determine the downstream namespace for this project namespace using the
	// ns-<namespace-uid> convention (MappedNamespaceResourceStrategy).
	// Using strategy.GetClient() for writes ensures the downstream namespace is
	// created with UpstreamOwnerNamespaceLabel so the InstanceProjector can
	// resolve the target project namespace without scanning all namespaces.
	strategy := downstreamclient.NewMappedNamespaceResourceStrategy(string(req.ClusterName), cl.GetClient(), r.FederationClient)
	downstreamNS, err := strategy.GetDownstreamNamespaceNameForUpstreamNamespace(ctx, deployment.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to determine downstream namespace: %w", err)
	}

	// Ensure the downstream namespace exists and carries the upstream tracking
	// labels so the InstanceProjector can resolve the project namespace by label
	// lookup instead of scanning all namespaces.
	if err := r.ensureDownstreamNamespace(ctx, downstreamNS, deployment.Namespace, string(req.ClusterName)); err != nil {
		return ctrl.Result{}, err
	}

	// Upsert the WorkloadDeployment in the downstream control plane via the
	// strategy client so any future Create calls also go through
	// ensureDownstreamNamespace automatically.
	if err := r.upsertDownstreamDeployment(ctx, strategy.GetClient(), &deployment, downstreamNS); err != nil {
		return ctrl.Result{}, err
	}

	// Lazily create the PropagationPolicy that targets clusters with the matching
	// city-code label.
	if err := r.ensurePropagationPolicy(ctx, downstreamNS, deployment.Spec.CityCode); err != nil {
		return ctrl.Result{}, err
	}

	// Pull aggregated status from the downstream control plane back into the
	// project namespace.
	if err := r.syncStatusFromDownstream(ctx, cl.GetClient(), &deployment, downstreamNS); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("federation complete")
	return ctrl.Result{}, nil
}

// Finalize removes the downstream WorkloadDeployment and, if no other
// deployments with the same city code remain in the downstream namespace, deletes
// the PropagationPolicy as well.
func (r *WorkloadDeploymentFederator) Finalize(ctx context.Context, obj client.Object) (finalizer.Result, error) {
	if r.FederationClient == nil {
		return finalizer.Result{}, nil
	}

	deployment := obj.(*computev1alpha.WorkloadDeployment)
	logger := log.FromContext(ctx).WithValues(
		"deployment", deployment.Name,
		"namespace", deployment.Namespace,
	)

	clusterName, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		return finalizer.Result{}, fmt.Errorf("cluster name not found in context")
	}

	cl, err := r.mgr.GetCluster(ctx, clusterName)
	if err != nil {
		return finalizer.Result{}, err
	}

	strategy := downstreamclient.NewMappedNamespaceResourceStrategy(string(clusterName), cl.GetClient(), r.FederationClient)
	downstreamNS, err := strategy.GetDownstreamNamespaceNameForUpstreamNamespace(ctx, deployment.Namespace)
	if err != nil {
		return finalizer.Result{}, fmt.Errorf("failed to determine downstream namespace during finalization: %w", err)
	}

	// Delete the downstream WorkloadDeployment.
	kd := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployment.Name,
			Namespace: downstreamNS,
		},
	}
	if err := r.FederationClient.Delete(ctx, kd); client.IgnoreNotFound(err) != nil {
		return finalizer.Result{}, fmt.Errorf("failed to delete downstream deployment %s/%s: %w", downstreamNS, deployment.Name, err)
	}
	logger.Info("deleted downstream WorkloadDeployment", "downstreamNamespace", downstreamNS)

	// Clean up the PropagationPolicy if no other deployments with the same city
	// code remain in this downstream namespace.
	if err := r.cleanupPropagationPolicyIfUnused(ctx, downstreamNS, deployment.Spec.CityCode); err != nil {
		return finalizer.Result{}, err
	}

	return finalizer.Result{}, nil
}

// ensureDownstreamNamespace creates or updates the downstream namespace, stamping
// it with the upstream tracking labels that MappedNamespaceResourceStrategy uses.
// This allows the InstanceProjector to resolve the project namespace name via a
// direct label lookup rather than scanning all namespaces by UID.
func (r *WorkloadDeploymentFederator) ensureDownstreamNamespace(ctx context.Context, name, upstreamNamespace, clusterName string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.FederationClient, ns, func() error {
		if ns.Labels == nil {
			ns.Labels = make(map[string]string)
		}
		ns.Labels[downstreamclient.UpstreamOwnerClusterNameLabel] = fmt.Sprintf("cluster-%s", strings.ReplaceAll(clusterName, "/", "_"))
		ns.Labels[downstreamclient.UpstreamOwnerNamespaceLabel] = upstreamNamespace
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ensure downstream namespace %q: %w", name, err)
	}
	return nil
}

// upsertDownstreamDeployment creates or updates the WorkloadDeployment in the
// downstream namespace via the provided client (expected to be strategy.GetClient()
// so the downstream namespace is created with upstream tracking labels).
func (r *WorkloadDeploymentFederator) upsertDownstreamDeployment(
	ctx context.Context,
	downstreamClient client.Client,
	deployment *computev1alpha.WorkloadDeployment,
	downstreamNS string,
) error {
	kd := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployment.Name,
			Namespace: downstreamNS,
		},
	}

	result, err := controllerutil.CreateOrPatch(ctx, downstreamClient, kd, func() error {
		if kd.Labels == nil {
			kd.Labels = make(map[string]string)
		}
		kd.Labels[cityCodeLabel] = deployment.Spec.CityCode
		kd.Labels[downstreamclient.UpstreamOwnerNamespaceLabel] = deployment.Namespace
		kd.Spec = deployment.Spec
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to upsert downstream deployment %s/%s: %w", downstreamNS, deployment.Name, err)
	}

	log.FromContext(ctx).Info("upserted downstream deployment", "result", result, "downstreamNamespace", downstreamNS)
	return nil
}

// ensurePropagationPolicy creates or updates a PropagationPolicy in the downstream
// namespace that selects all WorkloadDeployments with the given city-code label
// and targets clusters carrying the same label.
func (r *WorkloadDeploymentFederator) ensurePropagationPolicy(
	ctx context.Context,
	downstreamNS string,
	cityCode string,
) error {
	pp := &karmadapolicyv1alpha1.PropagationPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      propagationPolicyNameFor(cityCode),
			Namespace: downstreamNS,
		},
	}

	result, err := controllerutil.CreateOrPatch(ctx, r.FederationClient, pp, func() error {
		pp.Spec = karmadapolicyv1alpha1.PropagationSpec{
			// Select all WorkloadDeployments in this namespace that carry the
			// city-code label. Using a label selector (rather than individual
			// resource names) means that new deployments for this city are
			// automatically picked up without updating the policy.
			ResourceSelectors: []karmadapolicyv1alpha1.ResourceSelector{
				{
					APIVersion: computev1alpha.GroupVersion.String(),
					Kind:       kindWorkloadDeployment,
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							cityCodeLabel: cityCode,
						},
					},
				},
			},
			Placement: karmadapolicyv1alpha1.Placement{
				// Route to clusters that carry the same city-code label. POP-cell
				// clusters registered with the downstream control plane must be
				// labeled accordingly.
				ClusterAffinity: &karmadapolicyv1alpha1.ClusterAffinity{
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							cityCodeLabel: cityCode,
						},
					},
				},
			},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to upsert PropagationPolicy for city %q in %s: %w", cityCode, downstreamNS, err)
	}

	log.FromContext(ctx).Info("upserted PropagationPolicy", "result", result, "cityCode", cityCode, "downstreamNamespace", downstreamNS)
	return nil
}

// syncStatusFromDownstream reads the aggregated status of the WorkloadDeployment
// from the downstream namespace and writes it back to the project-namespace
// object. It is a no-op when the downstream object does not yet exist.
//
// WorkloadDeploymentStatusSyncer is the primary reactive path; this call also
// pulls status during spec-change reconciles (e.g. manager restarts).
func (r *WorkloadDeploymentFederator) syncStatusFromDownstream(
	ctx context.Context,
	projectClient client.Client,
	deployment *computev1alpha.WorkloadDeployment,
	downstreamNS string,
) error {
	var kd computev1alpha.WorkloadDeployment
	if err := r.FederationClient.Get(ctx, types.NamespacedName{
		Name:      deployment.Name,
		Namespace: downstreamNS,
	}, &kd); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get downstream deployment for status sync: %w", err)
	}

	if equality.Semantic.DeepEqual(deployment.Status, kd.Status) {
		return nil
	}

	deployment.Status = kd.Status
	if err := projectClient.Status().Update(ctx, deployment); err != nil {
		return fmt.Errorf("failed to write downstream status back to project deployment: %w", err)
	}
	return nil
}

// cleanupPropagationPolicyIfUnused deletes the PropagationPolicy for the given
// city code if no WorkloadDeployments with that city code remain in the
// downstream namespace.
func (r *WorkloadDeploymentFederator) cleanupPropagationPolicyIfUnused(
	ctx context.Context,
	downstreamNS string,
	cityCode string,
) error {
	var remaining computev1alpha.WorkloadDeploymentList
	if err := r.FederationClient.List(ctx, &remaining,
		client.InNamespace(downstreamNS),
		client.MatchingLabels{cityCodeLabel: cityCode},
	); err != nil {
		return fmt.Errorf("failed to list remaining downstream deployments for city %q: %w", cityCode, err)
	}

	if len(remaining.Items) > 0 {
		// Other deployments still need this PropagationPolicy.
		return nil
	}

	pp := &karmadapolicyv1alpha1.PropagationPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      propagationPolicyNameFor(cityCode),
			Namespace: downstreamNS,
		},
	}
	if err := r.FederationClient.Delete(ctx, pp); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to delete PropagationPolicy for city %q in %s: %w", cityCode, downstreamNS, err)
	}

	log.FromContext(ctx).Info("deleted PropagationPolicy (no more deployments for city)", "cityCode", cityCode, "downstreamNamespace", downstreamNS)
	return nil
}

// SetupWithManager registers the controller with the multicluster manager.
// It must only be called when FederationClient is non-nil.
func (r *WorkloadDeploymentFederator) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr
	r.finalizers = finalizer.NewFinalizers()
	if err := r.finalizers.Register(federatorFinalizer, r); err != nil {
		return fmt.Errorf("failed to register federator finalizer: %w", err)
	}
	return mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.WorkloadDeployment{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Named("workload-deployment-federator").
		Complete(r)
}

// propagationPolicyNameFor returns the PropagationPolicy name for a given city
// code. The name is stable and deterministic so that multiple reconciles of
// different deployments sharing the same city code converge on the same policy.
func propagationPolicyNameFor(cityCode string) string {
	// Sanitize the city code to a valid Kubernetes name: lower-case, spaces → hyphens.
	sanitized := strings.ToLower(strings.ReplaceAll(cityCode, " ", "-"))
	return fmt.Sprintf("city-%s", sanitized)
}
