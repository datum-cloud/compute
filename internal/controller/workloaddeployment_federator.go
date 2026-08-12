// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
	milosource "go.miloapis.com/milo/pkg/multicluster-runtime/source"
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

	kindWorkloadDeployment = "WorkloadDeployment"
)

// WorkloadDeploymentFederator replicates WorkloadDeployments from project
// namespaces into the downstream control plane so it can propagate them to the
// appropriate POP-cell clusters.
//
// For each WorkloadDeployment the controller:
//  1. Determines the downstream namespace via the ns-<project-namespace-uid>
//     convention (matching the MappedNamespaceResourceStrategy used by
//     go.datum.net/network-services-operator).
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
	// FederationCluster is a watchable cluster handle for the same Karmada
	// federation control plane that FederationClient talks to. It is used to set
	// up an informer-backed watch on the downstream WorkloadDeployment objects so
	// that status aggregated by Karmada onto the downstream WD is mirrored back to
	// the project-namespace WD immediately, rather than waiting for the next
	// informer resync. When nil (e.g. in unit tests), the downstream watch is
	// skipped and the controller falls back to watching only the VCP WD.
	FederationCluster cluster.Cluster
	finalizers        finalizer.Finalizers
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch

func (r *WorkloadDeploymentFederator) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	if r.FederationClient == nil {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx)

	// An empty cluster name resolves to the local host management cluster, which
	// has no compute CRDs — any Get would fail with "no matches for kind" and
	// requeue in a hot loop. The For watch (EngageWithLocalCluster=false) and the
	// preservation-wrapped downstream watch both set a real project cluster name,
	// so an empty name here is never legitimate. Drop it without erroring.
	if req.ClusterName == "" {
		logger.V(1).Info("dropping reconcile with empty cluster name")
		return ctrl.Result{}, nil
	}

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
	strategy := downstreamclient.NewMappedNamespaceResourceStrategy(string(req.ClusterName), cl.GetClient(), r.FederationClient)
	downstreamNS, err := strategy.GetDownstreamNamespaceNameForUpstreamNamespace(ctx, deployment.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to determine downstream namespace: %w", err)
	}

	if err := r.ensureDownstreamNamespace(ctx, downstreamNS, deployment.Namespace, string(req.ClusterName)); err != nil {
		return ctrl.Result{}, err
	}

	// Upsert the WorkloadDeployment in the downstream control plane via the
	// strategy client so any future Create calls also go through
	// ensureDownstreamNamespace automatically.
	if err := r.upsertDownstreamDeployment(ctx, strategy.GetClient(), &deployment, downstreamNS); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensurePropagationPolicy(ctx, downstreamNS, deployment.Spec.CityCode); err != nil {
		return ctrl.Result{}, err
	}

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
		ns.Labels[downstreamclient.UpstreamOwnerClusterNameLabel] = EncodeClusterName(clusterName)
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
		// Absence must mirror downstream too: the cell gate treats an absent
		// annotation as "resolver hasn't run" (wait), distinct from an empty
		// list ("nothing needed"). The resolver deletes the annotation — along
		// with the companions — when the template drops all references, so a
		// stale downstream copy would gate new instances forever on companions
		// that no longer exist.
		if anno, ok := deployment.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]; ok {
			if kd.Annotations == nil {
				kd.Annotations = make(map[string]string)
			}
			kd.Annotations[computev1alpha.ExpectedReferencedDataAnnotation] = anno
		} else {
			delete(kd.Annotations, computev1alpha.ExpectedReferencedDataAnnotation)
		}
		// Propagate the suspend request so the cell can act on it: Status is
		// never pushed hub->cell (only pulled cell->hub in
		// syncStatusFromDownstream), so SuspendedAnnotation is the only channel
		// the ComputeSuspend/ComputeResume hooks have to reach the cell.
		if anno, ok := deployment.Annotations[computev1alpha.SuspendedAnnotation]; ok {
			if kd.Annotations == nil {
				kd.Annotations = make(map[string]string)
			}
			kd.Annotations[computev1alpha.SuspendedAnnotation] = anno
		} else {
			delete(kd.Annotations, computev1alpha.SuspendedAnnotation)
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
	// The webhook requires cityCode, so an empty value here is corruption. An
	// empty-valued label selector would match the wrong deployment set and
	// mis-decide whether the PropagationPolicy is still in use.
	if cityCode == "" {
		return fmt.Errorf("cannot evaluate PropagationPolicy usage in namespace %q: city code is empty", downstreamNS)
	}

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
//
// The controller watches two control planes:
//
//   - The VCP/project WorkloadDeployment (via For), so spec changes in the
//     project namespace trigger federation to the downstream control plane.
//   - The downstream Karmada WorkloadDeployment (via WatchesRawSource against
//     FederationCluster), so when Karmada aggregates new status onto the
//     downstream WD the corresponding project WD is reconciled immediately and
//     the status is mirrored back. Without this second watch the federator only
//     caught up on the next informer resync (~10h), causing status lag.
func (r *WorkloadDeploymentFederator) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr
	r.finalizers = finalizer.NewFinalizers()
	if err := r.finalizers.Register(federatorFinalizer, r); err != nil {
		return fmt.Errorf("failed to register federator finalizer: %w", err)
	}

	b := mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.WorkloadDeployment{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Named("workload-deployment-federator")

	// Watch the downstream Karmada WorkloadDeployment whose status we mirror.
	// FederationCluster is a watchable handle for the federation control plane;
	// it is nil in unit tests, where only the For watch is exercised.
	//
	// The handler MUST preserve the ClusterName that mapDownstreamDeploymentToRequest
	// sets. milosource binds the raw source to the empty cluster name, and the
	// default TypedEnqueueRequestsFromMapFunc wraps the map in TypedInjectCluster,
	// which overwrites each request's ClusterName with that bound empty name — so
	// every request would resolve to the local host cluster (no compute CRDs) and
	// fail with "no matches for kind WorkloadDeployment". The preservation variant
	// skips that injection so our project-cluster ClusterName survives to Reconcile.
	if r.FederationCluster != nil {
		preserveClusterName := func(_ multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[*computev1alpha.WorkloadDeployment, mcreconcile.Request] {
			return mchandler.TypedEnqueueRequestsFromMapFuncWithClusterPreservation(r.mapDownstreamDeploymentToRequest)
		}
		b = b.WatchesRawSource(milosource.MustNewClusterSource(
			r.FederationCluster,
			&computev1alpha.WorkloadDeployment{},
			preserveClusterName,
		))
	}

	return b.Complete(r)
}

// mapDownstreamDeploymentToRequest maps an event on a downstream Karmada
// WorkloadDeployment to a reconcile request for the corresponding
// project-namespace WorkloadDeployment.
//
// Correlation mirrors the identity the federator establishes when it mirrors the
// object downstream (see upsertDownstreamDeployment / ensureDownstreamNamespace):
//
//   - The WD name is stable across all planes, so the request name equals the
//     downstream WD name.
//   - upsertDownstreamDeployment stamps the downstream WD with
//     UpstreamOwnerNamespaceLabel = the project namespace, which becomes the
//     request namespace.
//   - The project cluster name is not on the WD itself; ensureDownstreamNamespace
//     stamps it as UpstreamOwnerClusterNameLabel on the downstream namespace
//     (encoded "cluster-<name>" with "/" -> "_"). We read the namespace from the
//     federation plane to recover and decode it.
//
// Both correlation labels are stamped unconditionally by this controller
// (upsertDownstreamDeployment / ensureDownstreamNamespace), so a downstream WD
// or namespace lacking one is corruption, not a foreign object. Map functions
// cannot return errors and there is no polling backstop — a dropped event
// means permanently stale status on the project WD — so those drops are logged
// at error level to make the corruption visible.
func (r *WorkloadDeploymentFederator) mapDownstreamDeploymentToRequest(
	ctx context.Context,
	downstream *computev1alpha.WorkloadDeployment,
) []mcreconcile.Request {
	logger := log.FromContext(ctx)

	projectNamespace := downstream.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	if projectNamespace == "" {
		logger.Error(nil, "downstream WorkloadDeployment is missing the upstream-namespace label; dropping status event",
			"downstreamNamespace", downstream.Namespace, "name", downstream.Name,
			"label", downstreamclient.UpstreamOwnerNamespaceLabel)
		return nil
	}

	var ns corev1.Namespace
	if err := r.FederationCluster.GetClient().Get(ctx, types.NamespacedName{Name: downstream.Namespace}, &ns); err != nil {
		logger.V(1).Info("unable to resolve downstream namespace for status mapping; dropping event",
			"downstreamNamespace", downstream.Namespace, "error", err)
		return nil
	}
	encodedClusterName := ns.Labels[downstreamclient.UpstreamOwnerClusterNameLabel]
	if encodedClusterName == "" {
		logger.Error(nil, "downstream namespace is missing the upstream-cluster-name label; dropping status event",
			"downstreamNamespace", downstream.Namespace, "name", downstream.Name,
			"label", downstreamclient.UpstreamOwnerClusterNameLabel)
		return nil
	}
	clusterName := projectClusterNameFromLabel(encodedClusterName)
	if clusterName == "" {
		logger.Error(nil, "undecodable upstream-cluster-name label on downstream namespace; dropping status event",
			"downstreamNamespace", downstream.Namespace, "name", downstream.Name,
			"label", downstreamclient.UpstreamOwnerClusterNameLabel, "encoded", encodedClusterName)
		return nil
	}

	// Verify the project cluster is engaged before enqueuing. The Milo
	// multicluster provider keys clusters by bare project name, and GetCluster
	// returns an error for an unknown name. Without this guard, an unresolvable
	// name — or the empty string, which mcmanager routes to the local host
	// cluster that has no compute CRDs — would make Reconcile fail with
	// "no matches for kind WorkloadDeployment" in a hot loop. Dropping the event
	// is safe: once the provider engages the project cluster, the For watch
	// reconciles it and the next downstream status event maps cleanly.
	if _, err := r.mgr.GetCluster(ctx, multicluster.ClusterName(clusterName)); err != nil {
		logger.V(1).Info("project cluster not engaged for downstream status mapping; dropping event",
			"clusterName", clusterName, "downstreamNamespace", downstream.Namespace, "error", err)
		return nil
	}

	return []mcreconcile.Request{
		{
			ClusterName: multicluster.ClusterName(clusterName),
			Request: ctrl.Request{
				NamespacedName: types.NamespacedName{
					Namespace: projectNamespace,
					Name:      downstream.Name,
				},
			},
		},
	}
}

// projectClusterNameFromLabel extracts the project cluster name that the Milo
// multicluster provider uses as its cluster key from a downstream namespace's
// UpstreamOwnerClusterNameLabel value.
//
// MappedNamespaceResourceStrategy encodes the label as "cluster-<org>_<project>"
// (with "/" replaced by "_"), e.g. "cluster-datum-cloud" (no org) or
// "cluster-_test-project-abc" (empty org). The provider, however, keys clusters
// by bare project name only (multicluster provider: key = project.Name), so we
// strip the "cluster-" prefix, decode "_" back to "/", and return the final path
// segment — the project name. Examples:
//
//	"cluster-datum-cloud"        -> "datum-cloud"
//	"cluster-_test-project-abc"  -> "test-project-abc"
func projectClusterNameFromLabel(encoded string) string {
	name := DecodeClusterName(encoded)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// propagationPolicyNameFor returns the PropagationPolicy name for a given city
// code. The name is stable and deterministic so that multiple reconciles of
// different deployments sharing the same city code converge on the same policy.
func propagationPolicyNameFor(cityCode string) string {
	sanitized := strings.ToLower(strings.ReplaceAll(cityCode, " ", "-"))
	return fmt.Sprintf("city-%s", sanitized)
}
