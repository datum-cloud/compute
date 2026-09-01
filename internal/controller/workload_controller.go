// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

const (
	workloadControllerFinalizer    = "compute.datumapis.com/workload-controller"
	workloadConditionTypeAvailable = "Available"
)

// WorkloadReconciler reconciles a Workload object
type WorkloadReconciler struct {
	mgr        mcmanager.Manager
	finalizers finalizer.Finalizers

	// NetworkingEnabled mirrors the NetworkingIntegration feature gate. When
	// false the Network watch is not registered: the networking CRDs are absent
	// on control planes without the integration, and engaging a watch against a
	// missing kind wedges the manager.
	NetworkingEnabled bool
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=networks,verbs=get;list;watch
// +kubebuilder:rbac:groups=locations.miloapis.com,resources=locations,verbs=get;list;watch

func (r *WorkloadReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	ctx = mccontext.WithCluster(ctx, req.ClusterName)

	var workload computev1alpha.Workload
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &workload); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	finalizationResult, err := r.finalizers.Finalize(ctx, &workload)
	if err != nil {
		if v, ok := err.(kerrors.Aggregate); ok && v.Is(errWorkloadHasDeployments) {
			// Don't produce an error in this case and let the watch on deployments
			// result in another reconcile schedule.
			logger.Info("workload still has deployments, waiting until removal")
			return ctrl.Result{}, nil
		} else {
			return ctrl.Result{}, fmt.Errorf("failed to finalize: %w", err)
		}
	}
	if finalizationResult.Updated {
		if err = cl.GetClient().Update(ctx, &workload); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update based on finalization result: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if !workload.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	logger.Info("reconciling workload")
	defer logger.Info("reconcile complete")

	// TODO(jreese) perform extra validation on the workload now that it's been
	// created.
	//
	// The following should be true before creating any WorkloadDeployments:
	//	- All networks referenced by network interfaces exist - Done
	//	- There is no overlap in attached networks. - TODO
	//
	// Violations of the above constraints should be placed in the Available
	// condition reason and message.

	// var attachedNetworks []networkingv1alpha.Network
	notFoundNetworks := sets.Set[string]{}
	for _, networkInterface := range workload.Spec.Template.Spec.NetworkInterfaces {
		var network networkingv1alpha.Network
		networkObjectKey := client.ObjectKey{
			Namespace: workload.Namespace,
			Name:      networkInterface.Network.Name,
		}
		if err := cl.GetClient().Get(ctx, networkObjectKey, &network); err != nil {
			if apierrors.IsNotFound(err) {
				notFoundNetworks.Insert(networkInterface.Network.Name)
			} else {
				return ctrl.Result{}, fmt.Errorf("failed fetching network: %w", err)
			}
		}
		// attachedNetworks = append(attachedNetworks, network)
	}

	if len(notFoundNetworks) > 0 {
		missingNetworks := strings.Join(notFoundNetworks.UnsortedList(), ", ")
		changed := apimeta.SetStatusCondition(&workload.Status.Conditions, metav1.Condition{
			Type:    workloadConditionTypeAvailable,
			Status:  metav1.ConditionFalse,
			Reason:  computev1alpha.WorkloadReasonNetworkNotFound,
			Message: fmt.Sprintf("Unable to find networks: %s", missingNetworks),
		})

		if changed {
			if err := cl.GetClient().Status().Update(ctx, &workload); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed updating workload status: %w", err)
			}
		}

		logger.Info("did not find all networks", "missing_networks", missingNetworks)
		return ctrl.Result{}, nil
	}

	// TODO(jreese) leverage status conditions + observed generation as a method
	// to shortcut extra work being done. Consider an optional system level
	// timeout based on the LastTransitionTime.
	//
	// TODO(jreese) annotate entities with the controller version to help ensure
	// we could run multiple versions of an operator at the same time and
	// incrementally promote resources to newer versions.

	desired, orphaned, err := r.getDeploymentsForWorkload(ctx, cl.GetClient(), &workload)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed getting deployments for workload: %w", err)
	}

	placementDeployments := make(map[string][]computev1alpha.WorkloadDeployment)

	if len(orphaned) > 0 {
		for _, deployment := range orphaned {
			if deployment.DeletionTimestamp.IsZero() {
				if err := cl.GetClient().Delete(ctx, &deployment); client.IgnoreNotFound(err) != nil {
					return ctrl.Result{}, fmt.Errorf("failed while deleting orphaned deployment: %w", err)
				}
			}

			placementDeployments[deployment.Spec.PlacementName] = append(
				placementDeployments[deployment.Spec.PlacementName],
				deployment,
			)
		}
	}

	for _, desiredDeployment := range desired {
		logger.Info("ensuring workload deployment", "deployment_name", desiredDeployment.Name)

		deployment := &computev1alpha.WorkloadDeployment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: desiredDeployment.Namespace,
				Name:      desiredDeployment.Name,
			},
		}

		_, err := controllerutil.CreateOrUpdate(ctx, cl.GetClient(), deployment, func() error {
			if deployment.CreationTimestamp.IsZero() {
				logger.Info("creating deployment", "deployment_name", deployment.Name)
				if err := controllerutil.SetControllerReference(&workload, deployment, cl.GetScheme()); err != nil {
					return fmt.Errorf("failed to set controller on workload deployment: %w", err)
				}
			} else {
				logger.Info("updating deployment", "deployment_name", deployment.Name)
			}

			mergeDeploymentMetadata(deployment, &desiredDeployment)

			// TODO(jreese) consider how this plays well with autoscaling
			deployment.Spec = desiredDeployment.Spec
			return nil
		})

		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed mutating workload deployment: %w", err)
		}

		placementDeployments[deployment.Spec.PlacementName] = append(
			placementDeployments[deployment.Spec.PlacementName],
			*deployment,
		)
	}

	return ctrl.Result{}, r.reconcileWorkloadStatus(ctx, cl.GetClient(), &workload, placementDeployments)
}

func (r *WorkloadReconciler) reconcileWorkloadStatus(
	ctx context.Context,
	upstreamClient client.Client,
	workload *computev1alpha.Workload,
	placementDeployments map[string][]computev1alpha.WorkloadDeployment,
) error {
	logger := log.FromContext(ctx)
	logger.Info("reconciling placement status")
	newWorkloadStatus := workload.Status.DeepCopy()
	totalReplicas := int32(0)
	totalCurrentReplicas := int32(0)
	totalUpdatedReplicas := int32(0)
	totalDesiredReplicas := int32(0)
	totalReadyReplicas := int32(0)
	totalDeployments := int32(0)

	availablePlacementFound := false

	// worstReason / worstMessage track the highest-priority blocking reason seen
	// across all non-available deployments. When at least one deployment is
	// available the workload is available regardless, so these are only used in
	// the not-available path.
	worstReason := computev1alpha.WorkloadReasonNoAvailablePlacements
	worstMessage := "No available placements were found for the workload"
	worstPriority := workloadBlockingReasonPriority(worstReason)

	// Reconcile placement status
	newWorkloadStatus.Placements = []computev1alpha.WorkloadPlacementStatus{}

	// Sort placement names for deterministic iteration so that equal-priority
	// deployments in different placements are compared in a stable order.
	placementNames := make([]string, 0, len(placementDeployments))
	for name := range placementDeployments {
		placementNames = append(placementNames, name)
	}
	sort.Strings(placementNames)

	for _, placementName := range placementNames {
		placementDeployments := placementDeployments[placementName]
		placementStatus := computev1alpha.WorkloadPlacementStatus{
			Name: placementName,
		}

		// Get current status if it exists
		for _, ps := range workload.Status.Placements {
			if ps.Name == placementName {
				placementStatus = *ps.DeepCopy()
				break
			}
		}

		placementAvailableCondition := metav1.Condition{
			Type:    workloadConditionTypeAvailable,
			Status:  metav1.ConditionFalse,
			Reason:  computev1alpha.WorkloadReasonNoAvailableDeployments,
			Message: "No available deployments were found for the placement",
		}

		foundAvailableDeployment := false
		replicas := int32(0)
		currentReplicas := int32(0)
		updatedReplicas := int32(0)
		desiredReplicas := int32(0)
		readyReplicas := int32(0)
		totalDeployments += int32(len(placementDeployments))

		// Sort deployments by name within each placement so the tie-break between
		// equal-priority blockers is deterministic.
		sortedDeployments := make([]computev1alpha.WorkloadDeployment, len(placementDeployments))
		copy(sortedDeployments, placementDeployments)
		sort.Slice(sortedDeployments, func(i, j int) bool {
			return sortedDeployments[i].Name < sortedDeployments[j].Name
		})

		for _, deployment := range sortedDeployments {
			replicas += deployment.Status.Replicas
			currentReplicas += deployment.Status.CurrentReplicas
			updatedReplicas += deployment.Status.UpdatedReplicas
			desiredReplicas += deployment.Status.DesiredReplicas
			readyReplicas += deployment.Status.ReadyReplicas

			if apimeta.IsStatusConditionTrue(deployment.Status.Conditions, workloadConditionTypeAvailable) {
				foundAvailableDeployment = true
				continue
			}

			// Propagate the worst blocking reason upward from non-available deployments.
			availCond := apimeta.FindStatusCondition(deployment.Status.Conditions, workloadConditionTypeAvailable)
			if availCond != nil {
				p := workloadBlockingReasonPriority(availCond.Reason)
				if p > worstPriority {
					worstPriority = p
					worstReason = availCond.Reason
					worstMessage = availCond.Message
				}
			}
		}
		totalReplicas += replicas
		totalCurrentReplicas += currentReplicas
		totalUpdatedReplicas += updatedReplicas
		totalDesiredReplicas += desiredReplicas
		totalReadyReplicas += readyReplicas

		placementStatus.Replicas = replicas
		placementStatus.CurrentReplicas = currentReplicas
		placementStatus.UpdatedReplicas = updatedReplicas
		placementStatus.DesiredReplicas = desiredReplicas
		placementStatus.ReadyReplicas = readyReplicas

		if foundAvailableDeployment {
			placementAvailableCondition.Status = metav1.ConditionTrue
			placementAvailableCondition.Reason = "AvailableDeploymentFound"
			placementAvailableCondition.Message = "At least one available deployment was found"
			availablePlacementFound = true
		}

		apimeta.SetStatusCondition(&placementStatus.Conditions, placementAvailableCondition)

		newWorkloadStatus.Placements = append(newWorkloadStatus.Placements, placementStatus)
	}

	var availableCondition metav1.Condition
	if availablePlacementFound {
		availableCondition = metav1.Condition{
			Type:               workloadConditionTypeAvailable,
			Status:             metav1.ConditionTrue,
			Reason:             "AvailablePlacementFound",
			Message:            "At least one available placement was found",
			ObservedGeneration: workload.Generation,
		}
	} else {
		availableCondition = metav1.Condition{
			Type:               workloadConditionTypeAvailable,
			Status:             metav1.ConditionFalse,
			Reason:             worstReason,
			Message:            worstMessage,
			ObservedGeneration: workload.Generation,
		}
	}

	apimeta.SetStatusCondition(&newWorkloadStatus.Conditions, availableCondition)

	newWorkloadStatus.Deployments = totalDeployments
	newWorkloadStatus.Replicas = totalReplicas
	newWorkloadStatus.CurrentReplicas = totalCurrentReplicas
	newWorkloadStatus.UpdatedReplicas = totalUpdatedReplicas
	newWorkloadStatus.DesiredReplicas = totalDesiredReplicas
	newWorkloadStatus.ReadyReplicas = totalReadyReplicas
	newWorkloadStatus.ObservedGeneration = workload.Generation

	if equality.Semantic.DeepEqual(workload.Status, newWorkloadStatus) {
		return nil
	}

	workload.Status = *newWorkloadStatus
	if err := upstreamClient.Status().Update(ctx, workload); err != nil {
		return fmt.Errorf("failed updating workload status: %w", err)
	}

	return nil
}

var errWorkloadHasDeployments = errors.New("workload has deployments")

func (r *WorkloadReconciler) Finalize(ctx context.Context, obj client.Object) (finalizer.Result, error) {

	clusterName, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		return finalizer.Result{}, fmt.Errorf("cluster name not found in context")
	}

	cl, err := r.mgr.GetCluster(ctx, clusterName)
	if err != nil {
		return finalizer.Result{}, err
	}

	listOpts := client.MatchingFields{
		deploymentWorkloadUIDIndex: string(obj.GetUID()),
	}
	var deployments computev1alpha.WorkloadDeploymentList
	if err := cl.GetClient().List(ctx, &deployments, listOpts); err != nil {
		return finalizer.Result{}, err
	}

	if len(deployments.Items) == 0 {
		log.FromContext(ctx).Info("deployments have been removed")
		return finalizer.Result{}, nil
	}

	// All deployments need to be deleted before the workload may be deleted
	for _, deployment := range deployments.Items {
		if deployment.DeletionTimestamp.IsZero() {
			// Deletion will result in another reconcile of the workload, where we
			// will remove the finalizers.
			if err := cl.GetClient().Delete(ctx, &deployment); client.IgnoreNotFound(err) != nil {
				return finalizer.Result{}, fmt.Errorf("failed deleting workload deployment: %w", err)
			}
		}
	}

	// Really don't like using errors for communication here. I think we'd need
	// to move away from the finalizer helper to ensure we can wait on child
	// resources to be gone before allowing the finalizer to be removed.
	return finalizer.Result{}, errWorkloadHasDeployments
}

// getDeploymentsForWorkload returns both deployments that are desired to exist
// for a workload, and deployments that have been orphaned and should be
// removed.
func (r *WorkloadReconciler) getDeploymentsForWorkload(
	ctx context.Context,
	upstreamClient client.Client,
	workload *computev1alpha.Workload,
) (desired []computev1alpha.WorkloadDeployment, orphaned []computev1alpha.WorkloadDeployment, err error) {

	listOpts := client.MatchingFields{
		deploymentWorkloadUIDIndex: string(workload.UID),
	}
	var deployments computev1alpha.WorkloadDeploymentList
	if err := upstreamClient.List(ctx, &deployments, listOpts); err != nil {
		return nil, nil, err
	}

	existingDeployments := sets.Set[string]{}
	desiredDeployments := sets.Set[string]{}

	for _, deployment := range deployments.Items {
		existingDeployments.Insert(deployment.Name)
	}

	var locations locationsv1alpha1.LocationList
	if err := upstreamClient.List(ctx, &locations); err != nil {
		return nil, nil, fmt.Errorf("failed to list locations: %w", err)
	}

	// Remember this: namespace, name, err := cache.SplitMetaNamespaceKey(key)
	for _, placement := range workload.Spec.Placements {
		for _, locationRef := range placement.Locations {
			var location *locationsv1alpha1.Location
			for i := range locations.Items {
				if locations.Items[i].Name == locationRef.Name {
					location = &locations.Items[i]
					break
				}
			}

			if location == nil || !apimeta.IsStatusConditionTrue(location.Status.Conditions, locationsv1alpha1.LocationConditionReady) {
				continue
			}

			// TODO(jreese) should we use GenerateName for deployments and identify
			// them via labels instead? Would help with race conditions on workload
			// recreation.

			deploymentName := fmt.Sprintf("%s-%s-%s", workload.Name, placement.Name, strings.ToLower(locationRef.Name))
			desiredDeployments.Insert(deploymentName)

			desired = append(desired, computev1alpha.WorkloadDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: workload.Namespace,
					Name:      deploymentName,
					Labels: map[string]string{
						computev1alpha.WorkloadUIDLabel: string(workload.UID),
						computev1alpha.LocationLabel:    locationRef.Name,
						labelServiceName:                labelServiceNameValue,
					},
				},
				Spec: computev1alpha.WorkloadDeploymentSpec{
					WorkloadRef: computev1alpha.WorkloadReference{
						Name: workload.Name,
						UID:  workload.UID,
					},
					PlacementName: placement.Name,
					LocationRef:   locationRef,
					Template:      workload.Spec.Template,
					ScaleSettings: placement.ScaleSettings,
					Replicas:      new(placement.ScaleSettings.MinReplicas),
				},
			})
		}
	}

	// Collect orphans
	for _, name := range existingDeployments.Difference(desiredDeployments).UnsortedList() {
		for _, deployment := range deployments.Items {
			if name == deployment.Name {
				orphaned = append(orphaned, deployment)
			}
		}
	}

	return desired, orphaned, nil
}

// mergeDeploymentMetadata copies the controller-owned labels and annotations
// from desired onto deployment without discarding peer-owned keys. Only the
// spec is fully owned by this controller; metadata is shared with the
// referenced-data controller (which stamps the expected-referenced-data
// annotation) and the federation hub (which stamps propagation bookkeeping).
// A wholesale map replacement here would strip those keys, and because both
// controllers watch the same WorkloadDeployment, each write re-triggered the
// other in an annotation ping-pong that hot-looped otherwise-idle deployments.
func mergeDeploymentMetadata(deployment, desired *computev1alpha.WorkloadDeployment) {
	mergeLabels(deployment, desired.Labels)
	mergeAnnotations(deployment, desired.Annotations)
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr

	r.finalizers = finalizer.NewFinalizers()
	if err := r.finalizers.Register(workloadControllerFinalizer, r); err != nil {
		return fmt.Errorf("failed to register finalizer: %w", err)
	}

	b := mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.Workload{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Owns(&computev1alpha.WorkloadDeployment{}, mcbuilder.WithEngageWithLocalCluster(false))

	if !r.NetworkingEnabled {
		return b.Complete(r)
	}

	return b.
		Watches(&networkingv1alpha.Network{}, func(clusterName multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
			return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, network client.Object) []mcreconcile.Request {
				logger := log.FromContext(ctx)

				cluster, err := mgr.GetCluster(ctx, clusterName)
				if err != nil {
					logger.Error(err, "failed to get cluster")
					return nil
				}
				clusterClient := cluster.GetClient()

				networkName := client.ObjectKeyFromObject(network).String()
				listOpts := client.MatchingFields{
					workloadNetworksIndex: networkName,
				}

				var workloads computev1alpha.WorkloadList
				if err := clusterClient.List(ctx, &workloads, listOpts); err != nil {
					logger.Error(err, "failed to list workloads")
					return nil
				}

				var requests []mcreconcile.Request
				for _, workload := range workloads.Items {
					requests = append(requests, mcreconcile.Request{
						Request: reconcile.Request{
							NamespacedName: types.NamespacedName{
								Namespace: workload.Namespace,
								Name:      workload.Name,
							},
						},
						ClusterName: clusterName,
					})
				}

				return requests
			})
		}).
		Complete(r)
}

// workloadBlockingReasonPriority returns the relative priority of a blocking
// reason on Workload.Available. Higher numbers indicate causes that are more
// actionable and should be surfaced over lower-priority transient states.
// This is a server-internal ranking; clients observe only the winning condition.
//
// Priority table (matches RFC §5.4):
//
//	0 - NoAvailablePlacements / NoAvailableDeployments (default fallback)
//	1 - InstancesProvisioning  (transient startup)
//	2 - NetworkProvisioning    (infra provisioning)
//	3 - QuotaNotGranted / PendingQuota (operator action may be needed)
//	4 - ReferencedDataNotReady / AwaitingPropagation / Resolving (transient)
//	5 - SourceNotFound / SourceTooLarge / SourceUnauthorized (hard spec error)
//	6 - NetworkNotFound        (hard error; user action required)
//	7 - NetworkFailedToCreate  (hard infra error)
func workloadBlockingReasonPriority(reason string) int {
	switch reason {
	case computev1alpha.WorkloadReasonNoAvailablePlacements,
		computev1alpha.WorkloadReasonNoAvailableDeployments:
		return 0
	case computev1alpha.WorkloadDeploymentReasonInstancesProvisioning:
		return 1
	case computev1alpha.WorkloadDeploymentReasonNetworkProvisioning:
		return 2
	case computev1alpha.WorkloadDeploymentReasonQuotaNotGranted,
		computev1alpha.InstanceProgrammedReasonPendingQuota:
		return 3
	case computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady,
		computev1alpha.ReferencedDataReasonAwaitingPropagation,
		computev1alpha.ReferencedDataReasonResolving:
		return 4
	case computev1alpha.ReferencedDataReasonSourceNotFound,
		computev1alpha.ReferencedDataReasonSourceTooLarge,
		computev1alpha.ReferencedDataReasonSourceUnauthorized:
		return 5
	case computev1alpha.WorkloadReasonNetworkNotFound:
		return 6
	default:
		return 0
	}
}
