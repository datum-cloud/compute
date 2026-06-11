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
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
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

	// NetworkingEnabled controls whether the networking integration with
	// network-services-operator is active. When false, NetworkBinding creation is
	// skipped, the Network scheduling gate is never added to Instances (and is
	// actively removed if present), and the networking step is treated as
	// immediately ready. Defaults to true.
	NetworkingEnabled bool
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=locations,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=networkbindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=networkcontexts,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=subnetclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=subnets,verbs=get;list;watch

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
		// The finalizer-add Update is metadata-only and may be filtered by event
		// predicates or handlers, so requeue explicitly to guarantee the
		// deployment is reconciled past this point.
		return ctrl.Result{Requeue: true}, nil
	}

	if !deployment.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	logger.Info("reconciling deployment")
	defer logger.Info("reconcile complete")

	// Collect all instances for this deployment
	listOpts := client.MatchingLabels{
		computev1alpha.WorkloadDeploymentUIDLabel: string(deployment.GetUID()),
	}

	var instances computev1alpha.InstanceList
	if err := cl.GetClient().List(ctx, &instances, listOpts); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed listing instances: %w", err)
	}

	instanceControl := instancecontrolstateful.NewWithOptions(instancecontrolstateful.Options{
		NetworkingEnabled: r.NetworkingEnabled,
	})

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

	// When networking is disabled, bypass the entire network provisioning path.
	// The Network scheduling gate is treated as cleared and no NetworkBindings
	// are created. This lets Instances reach the runtime on cells where
	// network-services-operator (VPC) is not yet available.
	var networkReady bool
	locationResolved := true
	if !r.NetworkingEnabled {
		networkReady = true
	} else {
		var resolvedLocation *networkingv1alpha.LocationReference
		networkReady, resolvedLocation, err = r.reconcileNetworks(ctx, cl.GetClient(), &deployment)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed reconciling networks: %w", err)
		}
		// Persist the resolved Location to status so downstream components (e.g.
		// the stateful instance control strategy) can propagate it to Instances.
		// When no matching Location exists, resolvedLocation is nil and
		// Status.Location remains nil — instance creation is not blocked.
		locationResolved = resolvedLocation != nil
		if resolvedLocation != nil {
			deployment.Status.Location = resolvedLocation
		}
	}

	// Networks are all ready with subnets ready to use, remove any scheduling
	// gates on instances. If any instances were created by actions above, those
	// will result in this reconciler being processed again, so will properly have
	// their gates removed.

	replicas := len(instances.Items)
	desiredReplicas := deployment.Spec.ScaleSettings.MinReplicas
	if dt := deployment.DeletionTimestamp; !dt.IsZero() {
		desiredReplicas = 0
	}

	currentReplicas, updatedReplicas, readyReplicas, quotaBlockedReplicas, err := r.reconcileInstanceGates(ctx, cl.GetClient(), &deployment, instances.Items, networkReady)
	if err != nil {
		return ctrl.Result{}, err
	}

	deployment.Status.Replicas = int32(replicas)
	deployment.Status.CurrentReplicas = int32(currentReplicas)
	deployment.Status.UpdatedReplicas = int32(updatedReplicas)
	deployment.Status.DesiredReplicas = desiredReplicas
	deployment.Status.ReadyReplicas = int32(readyReplicas)
	deployment.Status.ObservedGeneration = deployment.Generation

	if quotaBlockedReplicas > 0 {
		apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:    computev1alpha.WorkloadDeploymentReplicasReady,
			Status:  metav1.ConditionFalse,
			Reason:  computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
			Message: fmt.Sprintf("%d of %d desired replicas are pending quota", quotaBlockedReplicas, desiredReplicas),
		})
	} else {
		apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:    computev1alpha.WorkloadDeploymentReplicasReady,
			Status:  metav1.ConditionTrue,
			Reason:  "ReplicasAvailable",
			Message: fmt.Sprintf("%d/%d replicas available", readyReplicas, desiredReplicas),
		})
	}

	if readyReplicas > 0 {
		apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:    computev1alpha.WorkloadDeploymentAvailable,
			Status:  metav1.ConditionTrue,
			Reason:  "StableInstanceFound",
			Message: fmt.Sprintf("%d/%d instances are ready", readyReplicas, replicas),
		})
	} else if !locationResolved {
		// Network provisioning cannot even start without a Location, so surface
		// the unresolved city rather than the generic provisioning reason — it is
		// the only user-visible signal while the deployment waits for the city's
		// Location to be created.
		apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:    computev1alpha.WorkloadDeploymentAvailable,
			Status:  metav1.ConditionFalse,
			Reason:  "NoMatchingLocation",
			Message: fmt.Sprintf("No Location matches city code %q", deployment.Spec.CityCode),
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

	if err := cl.GetClient().Status().Update(ctx, &deployment); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed updating deployment status: %w", err)
	}

	logger.Info("deployment status updated")

	return ctrl.Result{}, nil
}

func (r *WorkloadDeploymentReconciler) reconcileInstanceGates(
	ctx context.Context,
	c client.Client,
	deployment *computev1alpha.WorkloadDeployment,
	instances []computev1alpha.Instance,
	networkReady bool,
) (currentReplicas, updatedReplicas, readyReplicas, quotaBlockedReplicas int, err error) {
	templateHash := instancecontrol.ComputeHash(deployment.Spec.Template)
	for _, instance := range instances {
		if apimeta.IsStatusConditionPresentAndEqual(instance.Status.Conditions, computev1alpha.InstanceQuotaGranted, metav1.ConditionFalse) {
			quotaBlockedReplicas++
		}

		// Spec.Controller is a nilable pointer; guard it before dereferencing the
		// scheduling gates so an instance without controller state cannot panic
		// the reconcile (mirrors the Status.Controller guard below).
		if networkReady && instance.Spec.Controller != nil && len(instance.Spec.Controller.SchedulingGates) > 0 {
			newGates := slices.DeleteFunc(instance.Spec.Controller.SchedulingGates, func(gate computev1alpha.SchedulingGate) bool {
				return gate.Name == instancecontrol.NetworkSchedulingGate.String()
			})
			if len(newGates) != len(instance.Spec.Controller.SchedulingGates) {
				if _, patchErr := controllerutil.CreateOrPatch(ctx, c, &instance, func() error {
					instance.Spec.Controller.SchedulingGates = newGates
					return nil
				}); patchErr != nil {
					return 0, 0, 0, 0, fmt.Errorf("failed updating instance: %w", patchErr)
				}
			}
		}

		// An instance is "updated" once it has observed the desired template
		// revision, regardless of readiness. Counting these (even before they are
		// Programmed) makes a rolling update / restart observable: UpdatedReplicas
		// dips below Replicas while the recreated instance comes up, then recovers.
		// Status.Controller is a pointer the infra provider may not have populated
		// yet; guard the deref to avoid a panic that would abort the reconcile.
		onLatestRevision := instance.Status.Controller != nil &&
			instance.Status.Controller.ObservedTemplateHash == templateHash
		if onLatestRevision {
			updatedReplicas++
		}

		// CurrentReplicas is the Programmed subset of UpdatedReplicas — updated
		// instances that are ready to serve.
		if onLatestRevision && apimeta.IsStatusConditionTrue(instance.Status.Conditions, computev1alpha.InstanceProgrammed) {
			currentReplicas++
		}

		if apimeta.IsStatusConditionTrue(instance.Status.Conditions, computev1alpha.InstanceReady) {
			readyReplicas++
		}
	}
	return currentReplicas, updatedReplicas, readyReplicas, quotaBlockedReplicas, nil
}

// reconcileNetworks ensures NetworkBindings and SubnetClaims exist for all
// network interfaces on the deployment. It returns (networkReady, resolvedLocation, err).
// resolvedLocation is non-nil when a Location matching the deployment's city code
// was found; nil otherwise. Instance creation is never gated on resolvedLocation
// being non-nil — callers must treat a nil location as best-effort only.
func (r *WorkloadDeploymentReconciler) reconcileNetworks(
	ctx context.Context,
	c client.Client,
	deployment *computev1alpha.WorkloadDeployment,
) (bool, *networkingv1alpha.LocationReference, error) {
	logger := log.FromContext(ctx)

	// Resolve the Location for this deployment's city code. With Karmada
	// propagation the WorkloadDeployment lands in the cluster that serves the
	// requested city, so the Location object for that city must exist locally.
	var locationList networkingv1alpha.LocationList
	if err := c.List(ctx, &locationList); err != nil {
		return false, nil, fmt.Errorf("failed to list locations: %w", err)
	}

	var locationRef *networkingv1alpha.LocationReference
	for _, loc := range locationList.Items {
		if cityCode, ok := loc.Spec.Topology["topology.datum.net/city-code"]; ok && cityCode == deployment.Spec.CityCode {
			locationRef = &networkingv1alpha.LocationReference{
				Name:      loc.Name,
				Namespace: loc.Namespace,
			}
			break
		}
	}

	if locationRef == nil {
		// Surfaced to users via the Available condition (NoMatchingLocation); the
		// log is debug-level detail only.
		logger.V(1).Info("no location found for city code, waiting", "cityCode", deployment.Spec.CityCode)
		return false, nil, nil
	}

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
			return false, nil, fmt.Errorf("failed checking for existing network binding: %w", err)
		}

		if networkBinding.CreationTimestamp.IsZero() {
			networkBinding = networkingv1alpha.NetworkBinding{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: networkBindingObjectKey.Namespace,
					Name:      networkBindingObjectKey.Name,
				},
				Spec: networkingv1alpha.NetworkBindingSpec{
					Network:  networkInterface.Network,
					Location: *locationRef,
				},
			}

			if err := controllerutil.SetControllerReference(deployment, &networkBinding, c.Scheme()); err != nil {
				return false, nil, fmt.Errorf("failed to set controller on network binding: %w", err)
			}

			if err := c.Create(ctx, &networkBinding); err != nil {
				return false, nil, fmt.Errorf("failed creating network binding: %w", err)
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
		return false, locationRef, nil
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
			return false, nil, fmt.Errorf("failed checking for existing network context: %w", err)
		}

		if !apimeta.IsStatusConditionTrue(networkContext.Status.Conditions, networkingv1alpha.NetworkContextReady) {
			logger.Info("waiting for network context to be ready", "network_context", networkContext.Name)
			return false, locationRef, nil
		}

		var subnetClaims networkingv1alpha.SubnetClaimList
		listOpts := []client.ListOption{
			client.InNamespace(networkContext.Namespace),
		}

		if err := c.List(ctx, &subnetClaims, listOpts...); err != nil {
			return false, nil, fmt.Errorf("failed listing subnet claims: %w", err)
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
			if claim.Spec.Location.Namespace != locationRef.Namespace ||
				claim.Spec.Location.Name != locationRef.Name {
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
					Location: *locationRef,
				},
			}

			if err := controllerutil.SetOwnerReference(&networkContext, &subnetClaim, c.Scheme()); err != nil {
				return false, nil, fmt.Errorf("failed to set controller on subnet claim: %w", err)
			}

			if err := c.Create(ctx, &subnetClaim); err != nil {
				return false, nil, fmt.Errorf("failed creating subnet claim: %w", err)
			}

			logger.Info("created subnet claim", "subnetClaim", subnetClaim.Name)

			return false, locationRef, nil
		}

		logger.Info("found subnet claim", "subnetClaim", subnetClaim.Name)

		if !apimeta.IsStatusConditionTrue(subnetClaim.Status.Conditions, "Ready") {
			logger.Info("waiting for subnet claim to be ready", "subnetClaim", subnetClaim.Name)
			return false, locationRef, nil
		}

		var subnet networkingv1alpha.Subnet
		subnetObjectKey := client.ObjectKey{
			Namespace: subnetClaim.Namespace,
			Name:      subnetClaim.Status.SubnetRef.Name,
		}
		if err := c.Get(ctx, subnetObjectKey, &subnet); err != nil {
			return false, nil, fmt.Errorf("failed fetching subnet: %w", err)
		}

		if !apimeta.IsStatusConditionTrue(subnet.Status.Conditions, "Ready") {
			logger.Info("waiting for subnet to be ready", "subnet", subnet.Name)
			return false, locationRef, nil
		}

		logger.Info("subnet is ready", "subnet", subnet.Name)

	}

	return true, locationRef, nil
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

	b := mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.WorkloadDeployment{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Owns(&computev1alpha.Instance{})

	// Only watch networking resources when the networking integration is enabled.
	// On cells without network-services-operator these watches would log spurious
	// errors for missing CRDs.
	if r.NetworkingEnabled {
		b = b.
			Owns(&networkingv1alpha.NetworkBinding{}).
			// A deployment whose city has no Location yet waits without any other
			// wake-up event: NetworkBindings/SubnetClaims/Subnets only exist after
			// a Location resolved, and the reconciler does not poll. Watching
			// Locations re-reconciles the waiting deployments when their city's
			// Location appears (or its topology changes).
			Watches(&networkingv1alpha.Location{}, func(clusterName multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
				return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []mcreconcile.Request {
					location := o.(*networkingv1alpha.Location)
					return enqueueWorkloadDeploymentsForLocation(ctx, cl.GetClient(), clusterName, location)
				})
			}).
			Watches(&networkingv1alpha.SubnetClaim{}, func(clusterName multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
				return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []mcreconcile.Request {
					subnetClaim := o.(*networkingv1alpha.SubnetClaim)
					return enqueueWorkloadDeploymentByLocation(ctx, mgr, clusterName, subnetClaim.Spec.Location)
				})
			}).
			Watches(&networkingv1alpha.Subnet{}, func(clusterName multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
				return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []mcreconcile.Request {
					subnet := o.(*networkingv1alpha.Subnet)
					return enqueueWorkloadDeploymentByLocation(ctx, mgr, clusterName, subnet.Spec.Location)
				})
			})
	}

	return b.Complete(r)
}

// enqueueWorkloadDeploymentByLocation maps an object that carries a
// LocationReference (SubnetClaim, Subnet) to the WorkloadDeployments targeting
// the referenced Location's city. The reference must be resolved to the Location
// object first because only its topology carries the city code.
func enqueueWorkloadDeploymentByLocation(ctx context.Context, mgr mcmanager.Manager, clusterName multicluster.ClusterName, locationRef networkingv1alpha.LocationReference) []mcreconcile.Request {
	logger := log.FromContext(ctx)

	cl, err := mgr.GetCluster(ctx, clusterName)
	if err != nil {
		logger.Error(err, "failed to get cluster")
		return nil
	}
	clusterClient := cl.GetClient()

	var location networkingv1alpha.Location
	if err := clusterClient.Get(ctx, types.NamespacedName{
		Namespace: locationRef.Namespace,
		Name:      locationRef.Name,
	}, &location); err != nil {
		logger.Error(err, "failed to get location for enqueue", "location", locationRef)
		return nil
	}

	return enqueueWorkloadDeploymentsForLocation(ctx, clusterClient, clusterName, &location)
}

// enqueueWorkloadDeploymentsForLocation maps a Location to the
// WorkloadDeployments that target its city, via the deploymentCityCodeIndex.
func enqueueWorkloadDeploymentsForLocation(ctx context.Context, c client.Client, clusterName multicluster.ClusterName, location *networkingv1alpha.Location) []mcreconcile.Request {
	logger := log.FromContext(ctx)

	cityCode, ok := location.Spec.Topology["topology.datum.net/city-code"]
	if !ok {
		return nil
	}

	var workloadDeployments computev1alpha.WorkloadDeploymentList
	if err := c.List(ctx, &workloadDeployments, client.MatchingFields{
		deploymentCityCodeIndex: cityCode,
	}); err != nil {
		logger.Error(err, "failed to list workload deployments")
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
