// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/util/retry"
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

	// companionGuardRequeueAfter is the delay inserted between companion-guard
	// requeue attempts. Long enough for the referenced-data controller to delete
	// the hub companion; short enough that the deletion is not visibly delayed.
	companionGuardRequeueAfter = 5 * time.Second

	// companionGuardTimeout is the maximum wall-clock time the companion ordering
	// guard will block a WD deletion after its deletionTimestamp was set. After
	// this timeout the guard is bypassed and a warning is logged. The hub-side
	// OrphanRBReconciler is the authoritative backstop that reclaims any
	// ResourceBinding left behind, so bypassing the guard cannot leak an edge copy.
	companionGuardTimeout = 2 * time.Minute
)

// errCompanionsStillPresent is a sentinel returned by Finalize when hub
// companions are still present in the downstream namespace. Reconcile intercepts
// it, logs at Info (not Error), and returns RequeueAfter so the wait does not
// inflate the error-rate metric.
type errCompanionsStillPresent struct{ namespace string }

func (e errCompanionsStillPresent) Error() string {
	return fmt.Sprintf("companions still present in %s; waiting for referenced-data controller to finish cleanup", e.namespace)
}

// findCompanionGuardSentinel searches err (including any kerrors.Aggregate
// members) for errCompanionsStillPresent. The finalizer framework wraps errors
// in a kerrors.Aggregate whose Errors() slice contains the per-finalizer
// wrapped errors; because Aggregate does not implement Unwrap() the standard
// errors.As cannot traverse it, so we walk manually.
func findCompanionGuardSentinel(err error) (errCompanionsStillPresent, bool) {
	if agg, ok := err.(kerrors.Aggregate); ok {
		for _, e := range agg.Errors() {
			var sentinel errCompanionsStillPresent
			if errors.As(e, &sentinel) {
				return sentinel, true
			}
		}
		return errCompanionsStillPresent{}, false
	}
	var sentinel errCompanionsStillPresent
	if errors.As(err, &sentinel) {
		return sentinel, true
	}
	return errCompanionsStillPresent{}, false
}

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
		// The finalizer framework returns a kerrors.Aggregate which does not
		// implement Unwrap, so errors.As cannot traverse it directly. Walk the
		// aggregate manually to find the companion-guard sentinel so we can
		// requeue gracefully (Info log + RequeueAfter) instead of inflating the
		// error-rate metric.
		if waitErr, ok := findCompanionGuardSentinel(err); ok {
			logger.Info("deferring PropagationPolicy cleanup: hub companions still present",
				"downstreamNamespace", waitErr.namespace)
			return ctrl.Result{RequeueAfter: companionGuardRequeueAfter}, nil
		}
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

	// The companion ordering guard only applies when this is the last WD for its
	// city code — i.e. when cleanupPropagationPolicyIfUnused is actually about to
	// delete the PP. If other WDs with the same city code still exist the PP is
	// not deleted on this call anyway, so blocking here would deadlock any
	// shared-namespace deletion (WD-A blocked by WD-B's companion, forever).
	isLastForCity, err := r.isLastDeploymentForCity(ctx, downstreamNS, deployment.Spec.CityCode)
	if err != nil {
		return finalizer.Result{}, fmt.Errorf("failed to check city-code peers in %s: %w", downstreamNS, err)
	}
	if isLastForCity {
		if blocked, err := r.companionsStillPresent(ctx, downstreamNS, deployment); err != nil {
			return finalizer.Result{}, fmt.Errorf("failed to check for remaining companions in %s: %w", downstreamNS, err)
		} else if blocked {
			return finalizer.Result{}, errCompanionsStillPresent{namespace: downstreamNS}
		}
	}

	// Clean up the PropagationPolicy if no other deployments with the same city
	// code remain in this downstream namespace.
	if err := r.cleanupPropagationPolicyIfUnused(ctx, downstreamNS, deployment.Spec.CityCode); err != nil {
		return finalizer.Result{}, err
	}

	return finalizer.Result{}, nil
}

// isLastDeploymentForCity reports whether there are no other WorkloadDeployments
// with the same city code as cityCode remaining in downstreamNS (besides the one
// being deleted, which was already removed above).
func (r *WorkloadDeploymentFederator) isLastDeploymentForCity(ctx context.Context, downstreamNS, cityCode string) (bool, error) {
	var remaining computev1alpha.WorkloadDeploymentList
	if err := r.FederationClient.List(ctx, &remaining,
		client.InNamespace(downstreamNS),
		client.MatchingLabels{cityCodeLabel: cityCode},
	); err != nil {
		return false, fmt.Errorf("list downstream deployments for city %q: %w", cityCode, err)
	}
	return len(remaining.Items) == 0, nil
}

// companionsStillPresent reports whether any companion ConfigMaps or Secrets
// (labeled referenced-data=true) remain in the given downstream namespace.
// Returns (true, nil) when companions are present, (false, nil) when the
// namespace is clear, and (false, err) on API failure.
//
// deployment is the project-plane WD being finalized. Its DeletionTimestamp is
// used to bound how long the guard can block: if the WD has been terminating
// for longer than companionGuardTimeout the guard is bypassed with a warning.
// The hub-side OrphanRBReconciler is the authoritative backstop that reclaims any
// ResourceBinding left behind, so a bypass cannot leak an edge copy.
func (r *WorkloadDeploymentFederator) companionsStillPresent(
	ctx context.Context,
	downstreamNS string,
	deployment *computev1alpha.WorkloadDeployment,
) (bool, error) {
	// If the WD has been terminating longer than the timeout, skip the check so
	// a wedged referenced-data controller cannot block deletion permanently.
	if deployment.DeletionTimestamp != nil {
		age := time.Since(deployment.DeletionTimestamp.Time)
		if age > companionGuardTimeout {
			log.FromContext(ctx).Info(
				"companion-guard timeout reached; bypassing guard — hub-side OrphanRBReconciler will reclaim any remaining companions",
				"age", age.Round(time.Second),
				"timeout", companionGuardTimeout,
				"downstreamNamespace", downstreamNS,
			)
			return false, nil
		}
	}

	companionSelector := client.MatchingLabels{
		computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
	}
	nsSelector := client.InNamespace(downstreamNS)

	var cms corev1.ConfigMapList
	if err := r.FederationClient.List(ctx, &cms, nsSelector, companionSelector); err != nil {
		return false, fmt.Errorf("list companion ConfigMaps: %w", err)
	}
	if len(cms.Items) > 0 {
		return true, nil
	}

	var secrets corev1.SecretList
	if err := r.FederationClient.List(ctx, &secrets, nsSelector, companionSelector); err != nil {
		return false, fmt.Errorf("list companion Secrets: %w", err)
	}
	return len(secrets.Items) > 0, nil
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
		// Propagate controller-managed annotations from the project WD to the
		// downstream WD. The cell reads the expected-referenced-data annotation
		// to gate-clear instances; without this copy it would never arrive.
		if anno, ok := deployment.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]; ok {
			if kd.Annotations == nil {
				kd.Annotations = make(map[string]string)
			}
			kd.Annotations[computev1alpha.ExpectedReferencedDataAnnotation] = anno
		}
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
			// Select WorkloadDeployments by city-code label, plus ALL
			// companion ConfigMaps and Secrets in this namespace that carry the
			// referenced-data label. The label selector on ConfigMap/Secret is
			// city-code-agnostic — companions are shared across city codes when
			// multiple WDs reference the same source. Karmada propagates the
			// entire set to matching clusters in one policy, so companions
			// co-arrive with their WorkloadDeployment.
			//
			// Using separate ResourceSelectors for each kind (WorkloadDeployment,
			// ConfigMap, Secret) is the idiomatic Karmada pattern for
			// multi-kind propagation within a single policy.
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
				{
					// Propagate companion ConfigMaps alongside WorkloadDeployments.
					// The referenced-data label is the only selector needed; there
					// is no per-city partitioning of companions.
					APIVersion: corev1.SchemeGroupVersion.String(),
					Kind:       kindConfigMap,
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
						},
					},
				},
				{
					// Propagate companion Secrets alongside WorkloadDeployments.
					APIVersion: corev1.SchemeGroupVersion.String(),
					Kind:       kindSecret,
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
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
// from the downstream namespace and merges it back into the project-namespace
// object. It is a no-op when the downstream object does not yet exist.
//
// Merge semantics: the resolver (ReferencedDataController) owns the
// ReferencedDataReady condition and any conditions with SourceNotFound,
// SourceUnauthorized, or SourceTooLarge reasons. This method preserves those
// conditions, overwriting only the downstream-owned portion of the status
// (replica counts, Programmed, Ready, etc.). Without this merge a concurrent
// federator status sync would overwrite the resolver's condition with whatever
// (empty or stale) value the downstream WD carries.
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

	// Build the merged status: start from downstream, then re-apply the
	// resolver-owned ReferencedDataReady condition from the project WD so we
	// never overwrite it with the downstream's copy.
	merged := kd.Status.DeepCopy()
	if resolverCond := apimeta.FindStatusCondition(deployment.Status.Conditions, computev1alpha.ReferencedDataReady); resolverCond != nil {
		apimeta.SetStatusCondition(&merged.Conditions, *resolverCond)
	}

	if equality.Semantic.DeepEqual(deployment.Status, *merged) {
		return nil
	}

	// Wrap in RetryOnConflict so a concurrent annotation Patch by the resolver
	// does not cause a hard error. The status write is idempotent from the
	// perspective of the downstream fields it carries.
	key := types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := projectClient.Get(ctx, key, deployment); err != nil {
			return err
		}
		// Re-compute merged on each attempt in case the resolver condition changed.
		merged = kd.Status.DeepCopy()
		if resolverCond := apimeta.FindStatusCondition(deployment.Status.Conditions, computev1alpha.ReferencedDataReady); resolverCond != nil {
			apimeta.SetStatusCondition(&merged.Conditions, *resolverCond)
		}
		if equality.Semantic.DeepEqual(deployment.Status, *merged) {
			return nil
		}
		deployment.Status = *merged
		if err := projectClient.Status().Update(ctx, deployment); err != nil {
			return err
		}
		return nil
	})
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
