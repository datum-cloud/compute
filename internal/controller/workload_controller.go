// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"
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
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const workloadControllerFinalizer = "compute.datumapis.com/workload-controller"

// WorkloadReconciler reconciles a Workload object
type WorkloadReconciler struct {
	mgr        mcmanager.Manager
	finalizers finalizer.Finalizers
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloads/finalizers,verbs=update

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
			Type:    "Available",
			Status:  metav1.ConditionFalse,
			Reason:  "NetworkNotFound",
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

			deployment.Annotations = desiredDeployment.Annotations
			deployment.Labels = desiredDeployment.Labels

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
	totalDesiredReplicas := int32(0)
	totalReadyReplicas := int32(0)
	totalDeployments := int32(0)

	availablePlacementFound := false

	// Reconcile placement status
	newWorkloadStatus.Placements = []computev1alpha.WorkloadPlacementStatus{}
	for placementName, placementDeployments := range placementDeployments {
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
			Type:    "Available",
			Status:  metav1.ConditionFalse,
			Reason:  "NoAvailableDeployments",
			Message: "No available deployments were found for the placement",
		}

		foundAvailableDeployment := false
		replicas := int32(0)
		currentReplicas := int32(0)
		desiredReplicas := int32(0)
		readyReplicas := int32(0)
		totalDeployments += int32(len(placementDeployments))
		for _, deployment := range placementDeployments {
			replicas += deployment.Status.Replicas
			currentReplicas += deployment.Status.CurrentReplicas
			desiredReplicas += deployment.Status.DesiredReplicas
			readyReplicas += deployment.Status.ReadyReplicas

			if apimeta.IsStatusConditionTrue(deployment.Status.Conditions, "Available") {
				foundAvailableDeployment = true
			}
		}
		totalReplicas += replicas
		totalCurrentReplicas += currentReplicas
		totalDesiredReplicas += desiredReplicas
		totalReadyReplicas += readyReplicas

		placementStatus.Replicas = replicas
		placementStatus.CurrentReplicas = currentReplicas
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

	availableCondition := metav1.Condition{
		Type:    "Available",
		Status:  metav1.ConditionFalse,
		Reason:  "NoAvailablePlacements",
		Message: "No available placements were found for the workload",
	}

	if availablePlacementFound {
		availableCondition.Status = metav1.ConditionTrue
		availableCondition.Reason = "AvailablePlacementFound"
		availableCondition.Message = "At least one available placement was found"
	}

	apimeta.SetStatusCondition(&newWorkloadStatus.Conditions, availableCondition)

	newWorkloadStatus.Deployments = totalDeployments
	newWorkloadStatus.Replicas = totalReplicas
	newWorkloadStatus.CurrentReplicas = totalCurrentReplicas
	newWorkloadStatus.DesiredReplicas = totalDesiredReplicas
	newWorkloadStatus.ReadyReplicas = totalReadyReplicas

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

	var locations networkingv1alpha.LocationList
	if err := upstreamClient.List(ctx, &locations); err != nil {
		return nil, nil, fmt.Errorf("failed to list locations: %w", err)
	}

	if len(locations.Items) == 0 {
		return nil, nil, fmt.Errorf("no locations are registered with the system")
	}

	// Remember this: namespace, name, err := cache.SplitMetaNamespaceKey(key)
	for _, placement := range workload.Spec.Placements {
		for _, cityCode := range placement.CityCodes {
			foundLocation := false
			for _, location := range locations.Items {
				locationCityCode, ok := location.Spec.Topology["topology.datum.net/city-code"]
				if ok && cityCode == locationCityCode {
					foundLocation = true
					break
				}
			}

			if !foundLocation {
				// TODO(jreese) update status condition on placement if no locations are
				// found.
				continue
			}

			// TODO(jreese) should we use GenerateName for deployments and identify
			// them via labels instead? Would help with race conditions on workload
			// recreation.

			deploymentName := fmt.Sprintf("%s-%s-%s", workload.Name, placement.Name, strings.ToLower(cityCode))
			desiredDeployments.Insert(deploymentName)

			desired = append(desired, computev1alpha.WorkloadDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: workload.Namespace,
					Name:      deploymentName,
					Labels: map[string]string{
						computev1alpha.WorkloadUIDLabel: string(workload.UID),
					},
				},
				Spec: computev1alpha.WorkloadDeploymentSpec{
					WorkloadRef: computev1alpha.WorkloadReference{
						Name: workload.Name,
						UID:  workload.UID,
					},
					PlacementName: placement.Name,
					CityCode:      cityCode,
					Template:      workload.Spec.Template,
					ScaleSettings: placement.ScaleSettings,
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

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr

	r.finalizers = finalizer.NewFinalizers()
	if err := r.finalizers.Register(workloadControllerFinalizer, r); err != nil {
		return fmt.Errorf("failed to register finalizer: %w", err)
	}

	return mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.Workload{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Owns(&computev1alpha.WorkloadDeployment{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Watches(&networkingv1alpha.Network{}, func(clusterName string, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
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
