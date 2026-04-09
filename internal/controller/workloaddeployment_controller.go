// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
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

	"go.datum.net/compute/internal/controller/instancecontrol"
	instancecontrolstateful "go.datum.net/compute/internal/controller/instancecontrol/stateful"
)

// WorkloadDeploymentReconciler reconciles a WorkloadDeployment object
type WorkloadDeploymentReconciler struct {
	mgr        mcmanager.Manager
	finalizers finalizer.Finalizers
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/finalizers,verbs=update

func (r *WorkloadDeploymentReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
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
		if v, ok := err.(kerrors.Aggregate); ok && v.Is(errDeploymentHasInstances) {
			// Don't produce an error in this case and let the watch on deployments
			// result in another reconcile schedule.
			logger.Info("deployment still has instances, waiting until removal")
			return ctrl.Result{}, nil
		} else {
			return ctrl.Result{}, fmt.Errorf("failed to finalize: %w", err)
		}
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

	logger.Info("reconciling deployment")
	defer logger.Info("reconcile complete")

	if deployment.Status.Location == nil {
		return ctrl.Result{}, nil
	}

	// Collect all instances for this deployment
	listOpts := client.MatchingLabels{
		computev1alpha.WorkloadDeploymentUIDLabel: string(deployment.GetUID()),
	}

	var instances computev1alpha.InstanceList
	if err := cl.GetClient().List(ctx, &instances, listOpts); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed listing instances: %w", err)
	}

	instanceControl := instancecontrolstateful.New()

	actions, err := instanceControl.GetActions(ctx, cl.GetScheme(), &deployment, instances.Items)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed getting instance control actions: %w", err)
	}

	logger.Info("collected instance control actions", "count", len(actions))

	for _, action := range actions {
		// We don't need to actually check this, but it'll reduce log noise.
		if action.IsSkipped() {
			continue
		}

		logger.Info("instance control action", "instance", action.Object.GetName(), "action", action.ActionType())

		if err := action.Execute(ctx, cl.GetClient()); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed executing instance control action: %w", err)
		}
	}

	networkReady, err := r.reconcileNetworks(ctx, cl.GetClient(), &deployment)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed reconciling networks: %w", err)
	}

	// Networks are all ready with subnets ready to use, remove any scheduling
	// gates on instances. If any instances were created by actions above, those
	// will result in this reconciler being processed again, so will properly have
	// their gates removed.

	replicas := len(instances.Items)
	currentReplicas := 0
	desiredReplicas := deployment.Spec.ScaleSettings.MinReplicas
	if dt := deployment.DeletionTimestamp; !dt.IsZero() {
		desiredReplicas = 0
	}

	readyReplicas := 0
	for _, instance := range instances.Items {
		if networkReady && len(instance.Spec.Controller.SchedulingGates) > 0 {
			newGates := slices.DeleteFunc(instance.Spec.Controller.SchedulingGates, func(gate computev1alpha.SchedulingGate) bool {
				return gate.Name == instancecontrol.NetworkSchedulingGate.String()
			})

			if len(newGates) != len(instance.Spec.Controller.SchedulingGates) {
				if _, err := controllerutil.CreateOrPatch(ctx, cl.GetClient(), &instance, func() error {
					instance.Spec.Controller.SchedulingGates = newGates
					return nil
				}); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed updating instance: %w", err)
				}
			}
		}

		if apimeta.IsStatusConditionTrue(instance.Status.Conditions, computev1alpha.InstanceProgrammed) {
			if instance.Status.Controller.ObservedTemplateHash == instancecontrol.ComputeHash(deployment.Spec.Template) {
				currentReplicas++
			}
		}

		if apimeta.IsStatusConditionTrue(instance.Status.Conditions, computev1alpha.InstanceReady) {
			readyReplicas++
		}
	}

	patchResult, err := controllerutil.CreateOrPatch(ctx, cl.GetClient(), &deployment, func() error {
		deployment.Status.Replicas = int32(replicas)
		deployment.Status.CurrentReplicas = int32(currentReplicas)
		deployment.Status.DesiredReplicas = desiredReplicas
		deployment.Status.ReadyReplicas = int32(readyReplicas)

		if readyReplicas > 0 {
			apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
				Type:    computev1alpha.WorkloadDeploymentAvailable,
				Status:  metav1.ConditionTrue,
				Reason:  "StableInstanceFound",
				Message: fmt.Sprintf("%d/%d instances are ready", readyReplicas, replicas),
			})
		} else if !networkReady {
			apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
				Type:    computev1alpha.WorkloadDeploymentAvailable,
				Status:  metav1.ConditionFalse,
				Reason:  "ProvisioningNetwork",
				Message: "Network is being provisioned",
			})
		} else if replicas > 0 {
			apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
				Type:    computev1alpha.WorkloadDeploymentAvailable,
				Status:  metav1.ConditionFalse,
				Reason:  "ProvisioningInstances",
				Message: "Instances are being provisioned",
			})
		}

		return nil
	})

	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed updating deployment status: %w", err)
	}

	logger.Info("deployment status processed", "operation_result", patchResult)

	return ctrl.Result{}, nil
}

func (r *WorkloadDeploymentReconciler) reconcileNetworks(
	ctx context.Context,
	c client.Client,
	deployment *computev1alpha.WorkloadDeployment,
) (bool, error) {
	logger := log.FromContext(ctx)

	// First, ensure we have a NetworkBinding for each interface, and that the
	// binding is ready before we move on to create SubnetClaims.

	var networkContextRefs []networkingv1alpha.NetworkContextRef
	allNetworkBindingsReady := true
	for i, networkInterface := range deployment.Spec.Template.Spec.NetworkInterfaces {
		var networkBinding networkingv1alpha.NetworkBinding
		networkBindingObjectKey := client.ObjectKey{
			Namespace: deployment.Namespace,
			Name:      fmt.Sprintf("%s-net-%d", deployment.Name, i),
		}

		if err := c.Get(ctx, networkBindingObjectKey, &networkBinding); client.IgnoreNotFound(err) != nil {
			return false, fmt.Errorf("failed checking for existing network binding: %w", err)
		}

		if networkBinding.CreationTimestamp.IsZero() {
			networkBinding = networkingv1alpha.NetworkBinding{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: networkBindingObjectKey.Namespace,
					Name:      networkBindingObjectKey.Name,
				},
				Spec: networkingv1alpha.NetworkBindingSpec{
					Network:  networkInterface.Network,
					Location: *deployment.Status.Location,
				},
			}

			if err := controllerutil.SetControllerReference(deployment, &networkBinding, c.Scheme()); err != nil {
				return false, fmt.Errorf("failed to set controller on network binding: %w", err)
			}

			if err := c.Create(ctx, &networkBinding); err != nil {
				return false, fmt.Errorf("failed creating network binding: %w", err)
			}
		}

		if !apimeta.IsStatusConditionTrue(networkBinding.Status.Conditions, networkingv1alpha.NetworkBindingReady) {
			allNetworkBindingsReady = false
		} else if networkBinding.Status.NetworkContextRef != nil {
			networkContextRefs = append(networkContextRefs, *networkBinding.Status.NetworkContextRef)
		}
	}

	if !allNetworkBindingsReady {
		logger.Info("waiting for network bindings to be ready")
		return false, nil
	}

	// TODO(jreese): Currently this makes a SubnetClaim that will be used by
	// many instances. Move to a claim per instance interface, and allocate from
	// a larger subnet. In addition, it does not handle allocation of more than
	// one subnet per network context. We'll have a future IPAM controller in
	// network-services-operator that will handle this.
	//
	// Also, only handling ipv4

	for _, networkContextRef := range networkContextRefs {
		var networkContext networkingv1alpha.NetworkContext
		networkContextObjectKey := client.ObjectKey{
			Namespace: networkContextRef.Namespace,
			Name:      networkContextRef.Name,
		}

		if err := c.Get(ctx, networkContextObjectKey, &networkContext); client.IgnoreNotFound(err) != nil {
			return false, fmt.Errorf("failed checking for existing network context: %w", err)
		}

		if !apimeta.IsStatusConditionTrue(networkContext.Status.Conditions, networkingv1alpha.NetworkContextReady) {
			logger.Info("waiting for network context to be ready", "network_context", networkContext.Name)
			return false, nil
		}

		var subnetClaims networkingv1alpha.SubnetClaimList
		listOpts := []client.ListOption{
			client.InNamespace(networkContext.Namespace),
		}

		if err := c.List(ctx, &subnetClaims, listOpts...); err != nil {
			return false, fmt.Errorf("failed listing subnet claims: %w", err)
		}

		var subnetClaim networkingv1alpha.SubnetClaim
		for _, claim := range subnetClaims.Items {
			// If it's not the same subnet class, don't consider the subnet claim.
			if claim.Spec.SubnetClass != "private" {
				continue
			}

			// If it's not ipv4, don't consider the subnet claim.
			if claim.Spec.IPFamily != networkingv1alpha.IPv4Protocol {
				continue
			}

			// If it's not the same network context, don't consider the subnet claim.
			if claim.Spec.NetworkContext.Name != networkContext.Name {
				continue
			}

			// If it's not the same location, don't consider the subnet claim.
			if claim.Spec.Location.Namespace != deployment.Status.Location.Namespace ||
				claim.Spec.Location.Name != deployment.Status.Location.Name {
				continue
			}

			subnetClaim = claim
			break
		}

		if subnetClaim.CreationTimestamp.IsZero() {
			subnetClaim = networkingv1alpha.SubnetClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: networkContext.Namespace,
					// In the future, subnets will be created with an ordinal that increases.
					// This ensures that we don't create duplicate subnet claims when the
					// cache is not up to date.
					Name: fmt.Sprintf("%s-0", networkContext.Name),
				},
				Spec: networkingv1alpha.SubnetClaimSpec{
					SubnetClass: "private",
					IPFamily:    networkingv1alpha.IPv4Protocol,
					NetworkContext: networkingv1alpha.LocalNetworkContextRef{
						Name: networkContext.Name,
					},
					Location: *deployment.Status.Location,
				},
			}

			if err := controllerutil.SetOwnerReference(&networkContext, &subnetClaim, c.Scheme()); err != nil {
				return false, fmt.Errorf("failed to set controller on subnet claim: %w", err)
			}

			if err := c.Create(ctx, &subnetClaim); err != nil {
				return false, fmt.Errorf("failed creating subnet claim: %w", err)
			}

			logger.Info("created subnet claim", "subnetClaim", subnetClaim.Name)

			return false, nil
		}

		logger.Info("found subnet claim", "subnetClaim", subnetClaim.Name)

		if !apimeta.IsStatusConditionTrue(subnetClaim.Status.Conditions, "Ready") {
			logger.Info("waiting for subnet claim to be ready", "subnetClaim", subnetClaim.Name)
			return false, nil
		}

		var subnet networkingv1alpha.Subnet
		subnetObjectKey := client.ObjectKey{
			Namespace: subnetClaim.Namespace,
			Name:      subnetClaim.Status.SubnetRef.Name,
		}
		if err := c.Get(ctx, subnetObjectKey, &subnet); err != nil {
			return false, fmt.Errorf("failed fetching subnet: %w", err)
		}

		if !apimeta.IsStatusConditionTrue(subnet.Status.Conditions, "Ready") {
			logger.Info("waiting for subnet to be ready", "subnet", subnet.Name)
			return false, nil
		}

		logger.Info("subnet is ready", "subnet", subnet.Name)

	}

	return true, nil
}

var errDeploymentHasInstances = errors.New("deployment has instances")

func (r *WorkloadDeploymentReconciler) Finalize(ctx context.Context, obj client.Object) (finalizer.Result, error) {
	clusterName, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		return finalizer.Result{}, fmt.Errorf("cluster name not found in context")
	}

	cl, err := r.mgr.GetCluster(ctx, clusterName)
	if err != nil {
		return finalizer.Result{}, err
	}

	var instanceList computev1alpha.InstanceList
	listOpts := []client.ListOption{
		client.InNamespace(obj.GetNamespace()),
		client.MatchingLabels{
			computev1alpha.WorkloadDeploymentUIDLabel: string(obj.GetUID()),
		},
	}

	if err := cl.GetClient().List(ctx, &instanceList, listOpts...); err != nil {
		return finalizer.Result{}, fmt.Errorf("failed listing instances: %w", err)
	}

	if len(instanceList.Items) == 0 {
		log.FromContext(ctx).Info("instances have been removed")
		return finalizer.Result{}, nil
	}

	// All instances need to be deleted before the deployment may be deleted
	for _, instance := range instanceList.Items {
		if instance.DeletionTimestamp.IsZero() {
			if err := cl.GetClient().Delete(ctx, &instance); client.IgnoreNotFound(err) != nil {
				return finalizer.Result{}, fmt.Errorf("failed deleting instance: %w", err)
			}
		}
	}

	// Really don't like using errors for communication here. I think we'd need
	// to move away from the finalizer helper to ensure we can wait on child
	// resources to be gone before allowing the finalizer to be removed.
	return finalizer.Result{}, errDeploymentHasInstances
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadDeploymentReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr
	r.finalizers = finalizer.NewFinalizers()
	if err := r.finalizers.Register(workloadControllerFinalizer, r); err != nil {
		return fmt.Errorf("failed to register finalizer: %w", err)
	}
	return mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.WorkloadDeployment{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Owns(&computev1alpha.Instance{}).
		Owns(&networkingv1alpha.NetworkBinding{}).
		Watches(&networkingv1alpha.SubnetClaim{}, func(clusterName string, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
			return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []mcreconcile.Request {
				subnetClaim := o.(*networkingv1alpha.SubnetClaim)
				return enqueueWorkloadDeploymentByLocation(ctx, mgr, clusterName, subnetClaim.Spec.Location)
			})
		}).
		Watches(&networkingv1alpha.Subnet{}, func(clusterName string, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
			return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []mcreconcile.Request {
				subnet := o.(*networkingv1alpha.Subnet)
				return enqueueWorkloadDeploymentByLocation(ctx, mgr, clusterName, subnet.Spec.Location)
			})
		}).
		Complete(r)
}

func enqueueWorkloadDeploymentByLocation(ctx context.Context, mgr mcmanager.Manager, clusterName string, locationRef networkingv1alpha.LocationReference) []mcreconcile.Request {
	logger := log.FromContext(ctx)

	cluster, err := mgr.GetCluster(ctx, clusterName)
	if err != nil {
		logger.Error(err, "failed to get cluster")
		return nil
	}
	clusterClient := cluster.GetClient()

	locationName := (types.NamespacedName{
		Namespace: locationRef.Namespace,
		Name:      locationRef.Name,
	}).String()
	listOpts := client.MatchingFields{
		deploymentLocationIndex: locationName,
	}

	var workloadDeployments computev1alpha.WorkloadDeploymentList

	if err := clusterClient.List(ctx, &workloadDeployments, listOpts); err != nil {
		logger.Error(err, "failed to list workloads")
		return nil
	}

	requests := make([]mcreconcile.Request, 0, len(workloadDeployments.Items))
	for _, workload := range workloadDeployments.Items {
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
}
