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
	// have been federated to Karmada. It ensures we clean up the Karmada-side
	// object and any orphaned PropagationPolicies before the project object is
	// permanently deleted.
	federatorFinalizer = "compute.datumapis.com/federator"

	// cityCodeLabel is applied to WorkloadDeployments in the Karmada namespace
	// and is used by PropagationPolicy selectors to route them to the correct
	// POP-cell clusters. Karmada Cluster objects are expected to carry this
	// label with their city-code value.
	cityCodeLabel = "topology.datum.net/city-code"
)

// WorkloadDeploymentFederator replicates WorkloadDeployments from project
// namespaces into the Karmada control plane so that Karmada can propagate them
// to the appropriate POP-cell clusters.
//
// For each WorkloadDeployment the controller:
//  1. Determines the Karmada namespace via the ns-<project-namespace-uid>
//     convention (matching the MappedNamespaceResourceStrategy used by
//     go.datum.net/network-services-operator; this logic will migrate to Milo
//     once the shared library is promoted).
//  2. Upserts a corresponding WorkloadDeployment in that Karmada namespace,
//     stamped with label topology.datum.net/city-code=<cityCode>.
//  3. Lazily creates a PropagationPolicy per city code per Karmada namespace
//     that selects WorkloadDeployments by the city-code label and targets
//     clusters carrying the same label. The PP is deleted once no deployments
//     with that city code remain in the namespace.
//  4. Reads the aggregated status from Karmada and writes it back to the
//     project-namespace object.
//  5. On deletion: removes the Karmada-side WorkloadDeployment and cleans up
//     unused PropagationPolicies.
type WorkloadDeploymentFederator struct {
	mgr mcmanager.Manager
	// KarmadaClient is a client pointed at the Karmada API server. The caller
	// (cmd/main.go) is responsible for constructing it from --karmada-kubeconfig.
	KarmadaClient client.Client
	finalizers    finalizer.Finalizers
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list

func (r *WorkloadDeploymentFederator) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	if r.KarmadaClient == nil {
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

	logger.Info("federating deployment to Karmada")

	// Determine the Karmada namespace for this project namespace using the
	// ns-<namespace-uid> convention (MappedNamespaceResourceStrategy).
	strategy := downstreamclient.NewMappedNamespaceResourceStrategy(req.ClusterName, cl.GetClient(), r.KarmadaClient)
	karmadaNS, err := strategy.GetDownstreamNamespaceNameForUpstreamNamespace(ctx, deployment.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to determine Karmada namespace: %w", err)
	}

	// Ensure the Karmada namespace exists before writing any resources into it.
	if err := r.ensureKarmadaNamespace(ctx, karmadaNS); err != nil {
		return ctrl.Result{}, err
	}

	// Upsert the WorkloadDeployment in Karmada.
	if err := r.upsertKarmadaDeployment(ctx, &deployment, karmadaNS); err != nil {
		return ctrl.Result{}, err
	}

	// Lazily create the PropagationPolicy that targets clusters with the matching
	// city-code label.
	if err := r.ensurePropagationPolicy(ctx, karmadaNS, deployment.Spec.CityCode); err != nil {
		return ctrl.Result{}, err
	}

	// Pull aggregated status from Karmada back into the project namespace.
	if err := r.syncStatusFromKarmada(ctx, cl.GetClient(), &deployment, karmadaNS); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("federation complete")
	return ctrl.Result{}, nil
}

// Finalize removes the Karmada-side WorkloadDeployment and, if no other
// deployments with the same city code remain in the Karmada namespace, deletes
// the PropagationPolicy as well.
func (r *WorkloadDeploymentFederator) Finalize(ctx context.Context, obj client.Object) (finalizer.Result, error) {
	if r.KarmadaClient == nil {
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

	strategy := downstreamclient.NewMappedNamespaceResourceStrategy(clusterName, cl.GetClient(), r.KarmadaClient)
	karmadaNS, err := strategy.GetDownstreamNamespaceNameForUpstreamNamespace(ctx, deployment.Namespace)
	if err != nil {
		return finalizer.Result{}, fmt.Errorf("failed to determine Karmada namespace during finalization: %w", err)
	}

	// Delete the Karmada-side WorkloadDeployment.
	kd := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployment.Name,
			Namespace: karmadaNS,
		},
	}
	if err := r.KarmadaClient.Delete(ctx, kd); client.IgnoreNotFound(err) != nil {
		return finalizer.Result{}, fmt.Errorf("failed to delete Karmada-side deployment %s/%s: %w", karmadaNS, deployment.Name, err)
	}
	logger.Info("deleted Karmada-side WorkloadDeployment", "karmadaNamespace", karmadaNS)

	// Clean up the PropagationPolicy if no other deployments with the same city
	// code remain in this Karmada namespace.
	if err := r.cleanupPropagationPolicyIfUnused(ctx, karmadaNS, deployment.Spec.CityCode); err != nil {
		return finalizer.Result{}, err
	}

	return finalizer.Result{}, nil
}

// ensureKarmadaNamespace creates the namespace in the Karmada API server if it
// does not already exist.
func (r *WorkloadDeploymentFederator) ensureKarmadaNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := r.KarmadaClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to ensure Karmada namespace %q: %w", name, err)
	}
	return nil
}

// upsertKarmadaDeployment creates or updates the WorkloadDeployment in the
// Karmada namespace, ensuring it carries the city-code label required by the
// PropagationPolicy selector.
func (r *WorkloadDeploymentFederator) upsertKarmadaDeployment(
	ctx context.Context,
	deployment *computev1alpha.WorkloadDeployment,
	karmadaNS string,
) error {
	kd := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployment.Name,
			Namespace: karmadaNS,
		},
	}

	result, err := controllerutil.CreateOrPatch(ctx, r.KarmadaClient, kd, func() error {
		if kd.Labels == nil {
			kd.Labels = make(map[string]string)
		}
		kd.Labels[cityCodeLabel] = deployment.Spec.CityCode
		kd.Spec = deployment.Spec
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to upsert Karmada deployment %s/%s: %w", karmadaNS, deployment.Name, err)
	}

	log.FromContext(ctx).Info("upserted Karmada deployment", "result", result, "karmadaNamespace", karmadaNS)
	return nil
}

// ensurePropagationPolicy creates or updates a PropagationPolicy in the Karmada
// namespace that selects all WorkloadDeployments with the given city-code label
// and targets clusters carrying the same label.
func (r *WorkloadDeploymentFederator) ensurePropagationPolicy(
	ctx context.Context,
	karmadaNS string,
	cityCode string,
) error {
	pp := &karmadapolicyv1alpha1.PropagationPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      propagationPolicyNameFor(cityCode),
			Namespace: karmadaNS,
		},
	}

	result, err := controllerutil.CreateOrPatch(ctx, r.KarmadaClient, pp, func() error {
		pp.Spec = karmadapolicyv1alpha1.PropagationSpec{
			// Select all WorkloadDeployments in this namespace that carry the
			// city-code label. Using a label selector (rather than individual
			// resource names) means that new deployments for this city are
			// automatically picked up without updating the policy.
			ResourceSelectors: []karmadapolicyv1alpha1.ResourceSelector{
				{
					APIVersion: computev1alpha.GroupVersion.String(),
					Kind:       "WorkloadDeployment",
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							cityCodeLabel: cityCode,
						},
					},
				},
			},
			Placement: karmadapolicyv1alpha1.Placement{
				// Route to clusters that carry the same city-code label. POP-cell
				// clusters registered with Karmada must be labeled accordingly.
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
		return fmt.Errorf("failed to upsert PropagationPolicy for city %q in %s: %w", cityCode, karmadaNS, err)
	}

	log.FromContext(ctx).Info("upserted PropagationPolicy", "result", result, "cityCode", cityCode, "karmadaNamespace", karmadaNS)
	return nil
}

// syncStatusFromKarmada reads the aggregated status of the WorkloadDeployment
// from the Karmada namespace and writes it back to the project-namespace object.
// It is a no-op when the Karmada-side object does not yet exist.
func (r *WorkloadDeploymentFederator) syncStatusFromKarmada(
	ctx context.Context,
	projectClient client.Client,
	deployment *computev1alpha.WorkloadDeployment,
	karmadaNS string,
) error {
	var kd computev1alpha.WorkloadDeployment
	if err := r.KarmadaClient.Get(ctx, types.NamespacedName{
		Name:      deployment.Name,
		Namespace: karmadaNS,
	}, &kd); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get Karmada deployment for status sync: %w", err)
	}

	if equality.Semantic.DeepEqual(deployment.Status, kd.Status) {
		return nil
	}

	deployment.Status = kd.Status
	if err := projectClient.Status().Update(ctx, deployment); err != nil {
		return fmt.Errorf("failed to write Karmada status back to project deployment: %w", err)
	}
	return nil
}

// cleanupPropagationPolicyIfUnused deletes the PropagationPolicy for the given
// city code if no WorkloadDeployments with that city code remain in the Karmada
// namespace.
func (r *WorkloadDeploymentFederator) cleanupPropagationPolicyIfUnused(
	ctx context.Context,
	karmadaNS string,
	cityCode string,
) error {
	var remaining computev1alpha.WorkloadDeploymentList
	if err := r.KarmadaClient.List(ctx, &remaining,
		client.InNamespace(karmadaNS),
		client.MatchingLabels{cityCodeLabel: cityCode},
	); err != nil {
		return fmt.Errorf("failed to list remaining Karmada deployments for city %q: %w", cityCode, err)
	}

	if len(remaining.Items) > 0 {
		// Other deployments still need this PropagationPolicy.
		return nil
	}

	pp := &karmadapolicyv1alpha1.PropagationPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      propagationPolicyNameFor(cityCode),
			Namespace: karmadaNS,
		},
	}
	if err := r.KarmadaClient.Delete(ctx, pp); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to delete PropagationPolicy for city %q in %s: %w", cityCode, karmadaNS, err)
	}

	log.FromContext(ctx).Info("deleted PropagationPolicy (no more deployments for city)", "cityCode", cityCode, "karmadaNamespace", karmadaNS)
	return nil
}

// SetupWithManager registers the controller with the multicluster manager.
// It must only be called when KarmadaClient is non-nil.
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
