// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	"go.miloapis.com/milo/pkg/downstreamclient"

	"go.datum.net/compute/internal/controller/instancecontrol"
	"go.datum.net/compute/internal/quota"
)

const (
	// instanceQuotaFinalizer ensures the quota ResourceClaim is deleted when
	// an Instance is removed.
	instanceQuotaFinalizer = "quota.compute.datumapis.com/claim-cleanup"

	// instanceControllerFinalizer is registered with the finalizer framework and
	// triggers downstream write-back cleanup on deletion.
	instanceControllerFinalizer = "compute.datumapis.com/instance-controller"

	// instanceQuotaClaimSourceLabel is stamped on ResourceClaim objects with the
	// name of the edge cluster that created them. The claim watch predicate uses
	// this label to filter out claims written by other edge controllers targeting
	// the same project control planes.
	instanceQuotaClaimSourceLabel = "compute.datumapis.com/source-cluster"
)

// clusterGetter is the subset of mcmanager.Manager used by InstanceReconciler.
// Keeping it narrow allows unit tests to substitute a minimal fake.
type clusterGetter interface {
	GetCluster(ctx context.Context, clusterName multicluster.ClusterName) (cluster.Cluster, error)
}

// InstanceReconciler reconciles an Instance object
type InstanceReconciler struct {
	mgr                clusterGetter
	scheme             *runtime.Scheme
	quotaClientManager *quota.ProjectQuotaClientManager
	edgeClusterName    string
	// projectIDForInstance derives the Milo project ID used for quota
	// ResourceClaim management. In Milo mode it returns string(clusterName); in
	// single-cell mode it returns instance.Namespace because the cluster name is
	// always "single" and the namespace is the per-project identifier.
	projectIDForInstance func(clusterName multicluster.ClusterName, instance *computev1alpha.Instance) string
	// clusterNameForProject maps a Milo project ID back to the multicluster
	// ClusterName that owns that project's workloads. In Milo mode the
	// ClusterName equals the project ID. In single-cell mode the only registered
	// cluster is "single" regardless of project ID. When nil, falls back to
	// multicluster.ClusterName(projectID), which is correct for Milo mode.
	clusterNameForProject func(projectID string) multicluster.ClusterName
	// DownstreamClient is an optional client pointing at the downstream control plane.
	// When non-nil, the reconciler writes a copy of each Instance back to the
	// downstream control plane so that the InstanceProjector (running in the
	// management cluster) can aggregate status across all POP cells. Set to nil to
	// disable federation write-back (e.g. in non-federation deployments).
	DownstreamClient client.Client
	finalizers       finalizer.Finalizers
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups=quota.miloapis.com,resources=resourceclaims,verbs=get;list;watch;create;delete

func (r *InstanceReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (_ ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	ctx = mccontext.WithCluster(ctx, req.ClusterName)
	var instance computev1alpha.Instance
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &instance); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Run the finalizer framework first. This handles downstream write-back cleanup
	// via the Finalize method registered below.
	finalizationResult, err := r.finalizers.Finalize(ctx, &instance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to finalize: %w", err)
	}
	if finalizationResult.Updated {
		if err = cl.GetClient().Update(ctx, &instance); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update based on finalization result: %w", err)
		}
		return ctrl.Result{}, nil
	}

	logger.Info("reconciling instance")
	defer logger.Info("reconcile complete")

	if !instance.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.reconcileDeletion(ctx, cl.GetClient(), req.ClusterName, &instance)
	}

	if !controllerutil.ContainsFinalizer(&instance, instanceQuotaFinalizer) {
		controllerutil.AddFinalizer(&instance, instanceQuotaFinalizer)
		if err := cl.GetClient().Update(ctx, &instance); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed adding quota finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	statusChanged, err := r.reconcileQuotaCondition(ctx, req.ClusterName, &instance)
	if err != nil {
		return ctrl.Result{}, err
	}

	readyChanged, err := r.reconcileInstanceReadyCondition(ctx, cl.GetClient(), &instance, r.checkForNetworkCreationFailure)
	if err != nil {
		return ctrl.Result{}, err
	}

	if statusChanged || readyChanged {
		if err := cl.GetClient().Status().Update(ctx, &instance); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.writeBackToDownstream(ctx, req.ClusterName, &instance); err != nil {
			return ctrl.Result{}, err
		}
		// Return after the status update so that the next reconcile sees the
		// updated QuotaGranted condition before attempting spec changes.
		return ctrl.Result{}, nil
	}

	if err := r.removeQuotaSchedulingGate(ctx, cl.GetClient(), &instance); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileDeletion handles quota-claim cleanup when an Instance is being
// deleted. It removes the quota finalizer once the ResourceClaim is gone.
func (r *InstanceReconciler) reconcileDeletion(ctx context.Context, cl client.Client, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) error {
	if !controllerutil.ContainsFinalizer(instance, instanceQuotaFinalizer) {
		return nil
	}

	if r.quotaClientManager != nil {
		projectID := r.resolveProjectID(clusterName, instance)
		projectClient, err := r.quotaClientManager.ClientForProject(ctx, projectID, r.scheme)
		if err != nil {
			return fmt.Errorf("failed getting quota client for deletion: %w", err)
		}

		claimName := fmt.Sprintf("%s--%s", instance.Namespace, instance.Name)
		var claim quotav1alpha1.ResourceClaim
		if err := projectClient.Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: claimName}, &claim); err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed getting resource claim for deletion: %w", err)
			}
		} else {
			if err := projectClient.Delete(ctx, &claim); client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("failed deleting resource claim: %w", err)
			}
		}
	}

	controllerutil.RemoveFinalizer(instance, instanceQuotaFinalizer)
	if err := cl.Update(ctx, instance); err != nil {
		return fmt.Errorf("failed removing quota finalizer: %w", err)
	}
	return nil
}

// reconcileQuotaCondition reconciles the ResourceClaim and updates the
// InstanceQuotaGranted status condition. It returns true when the condition
// changed and a status update is required.
func (r *InstanceReconciler) reconcileQuotaCondition(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) (bool, error) {
	grantedCondition, err := r.reconcileQuotaClaim(ctx, clusterName, instance)
	if err != nil {
		return false, fmt.Errorf("failed reconciling quota claim: %w", err)
	}

	switch {
	case grantedCondition == nil || (grantedCondition.Status == metav1.ConditionFalse && grantedCondition.Reason == quotav1alpha1.ResourceClaimPendingReason):
		return apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceQuotaGranted,
			Status:             metav1.ConditionUnknown,
			Reason:             computev1alpha.InstanceQuotaGrantedReasonPendingEvaluation,
			Message:            "Waiting for quota evaluation",
			ObservedGeneration: instance.Generation,
		}), nil

	case grantedCondition.Status == metav1.ConditionTrue:
		return apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceQuotaGranted,
			Status:             metav1.ConditionTrue,
			Reason:             computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable,
			Message:            grantedCondition.Message,
			ObservedGeneration: instance.Generation,
		}), nil

	default: // grantedCondition.Status == metav1.ConditionFalse
		reason := computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded
		if grantedCondition.Reason == quotav1alpha1.ResourceClaimValidationFailedReason {
			reason = computev1alpha.InstanceQuotaGrantedReasonValidationFailed
		}
		return apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceQuotaGranted,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            grantedCondition.Message,
			ObservedGeneration: instance.Generation,
		}), nil
	}
}

// removeQuotaSchedulingGate removes the quota scheduling gate from the
// Instance spec once QuotaGranted=True has been persisted to status.
func (r *InstanceReconciler) removeQuotaSchedulingGate(ctx context.Context, cl client.Client, instance *computev1alpha.Instance) error {
	quotaGrantedCond := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceQuotaGranted)
	if quotaGrantedCond == nil || quotaGrantedCond.Status != metav1.ConditionTrue {
		return nil
	}
	if instance.Spec.Controller == nil {
		return nil
	}

	newGates := make([]computev1alpha.SchedulingGate, 0, len(instance.Spec.Controller.SchedulingGates))
	gateRemoved := false
	for _, gate := range instance.Spec.Controller.SchedulingGates {
		if gate.Name == instancecontrol.QuotaSchedulingGate.String() {
			gateRemoved = true
			continue
		}
		newGates = append(newGates, gate)
	}
	if !gateRemoved {
		return nil
	}

	patch := client.MergeFrom(instance.DeepCopy())
	instance.Spec.Controller.SchedulingGates = newGates
	if err := cl.Patch(ctx, instance, patch); err != nil {
		return fmt.Errorf("failed patching quota scheduling gate: %w", err)
	}
	return nil
}

// Finalize removes the downstream write-back Instance when the local Instance is
// deleted. It is a no-op when downstream federation is disabled.
func (r *InstanceReconciler) Finalize(ctx context.Context, obj client.Object) (finalizer.Result, error) {
	if r.DownstreamClient == nil {
		return finalizer.Result{}, nil
	}

	instance := obj.(*computev1alpha.Instance)

	downstreamInstance := &computev1alpha.Instance{}
	err := r.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(instance), downstreamInstance)
	if apierrors.IsNotFound(err) {
		// Already gone — nothing to do.
		return finalizer.Result{}, nil
	}
	if err != nil {
		return finalizer.Result{}, fmt.Errorf("failed getting downstream instance for deletion: %w", err)
	}

	if err := r.DownstreamClient.Delete(ctx, downstreamInstance); client.IgnoreNotFound(err) != nil {
		return finalizer.Result{}, fmt.Errorf("failed deleting downstream write-back instance: %w", err)
	}

	return finalizer.Result{}, nil
}

// writeBackToDownstream copies the Instance spec and status to the downstream
// control plane so that the InstanceProjector can aggregate state from all POP
// cells. It is a no-op when DownstreamClient is nil (federation disabled).
func (r *InstanceReconciler) writeBackToDownstream(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) error {
	if r.DownstreamClient == nil {
		return nil
	}

	// Encode the POP-cell cluster name using the same convention as NSO's
	// MappedNamespaceResourceStrategy: "cluster-<name>" with "/" → "_".
	encodedClusterName := "cluster-" + strings.ReplaceAll(string(clusterName), "/", "_")

	// Read the upstream project namespace name from the downstream namespace label
	// stamped by the WorkloadDeploymentFederator. This lets the InstanceProjector
	// resolve the target namespace via a direct label lookup on the Instance rather
	// than scanning all project cluster namespaces by UID.
	upstreamNamespace := instance.Namespace // fallback: cell namespace (ns-<uid>)
	var downstreamNS corev1.Namespace
	if err := r.DownstreamClient.Get(ctx, client.ObjectKey{Name: instance.Namespace}, &downstreamNS); err == nil {
		if v := downstreamNS.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]; v != "" {
			upstreamNamespace = v
		}
	}

	writeBack := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				downstreamclient.UpstreamOwnerClusterNameLabel: encodedClusterName,
				downstreamclient.UpstreamOwnerNamespaceLabel:   upstreamNamespace,
			},
		},
		Spec: instance.Spec,
	}

	existing := &computev1alpha.Instance{}
	err := r.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(writeBack), existing)
	if apierrors.IsNotFound(err) {
		// Ensure the namespace exists in the downstream control plane before creating the Instance.
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instance.Namespace}}
		if err := r.DownstreamClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed ensuring downstream namespace: %w", err)
		}
		if err := r.DownstreamClient.Create(ctx, writeBack); err != nil {
			return fmt.Errorf("failed creating downstream write-back instance: %w", err)
		}
		writeBack.Status = instance.Status
		if err := r.DownstreamClient.Status().Update(ctx, writeBack); err != nil {
			return fmt.Errorf("failed updating downstream write-back instance status after create: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed getting downstream instance: %w", err)
	}

	// Update spec + labels on the existing object, then push status separately.
	existing.Spec = instance.Spec
	existing.Labels = writeBack.Labels
	if err := r.DownstreamClient.Update(ctx, existing); err != nil {
		return fmt.Errorf("failed updating downstream write-back instance: %w", err)
	}

	existing.Status = instance.Status
	if err := r.DownstreamClient.Status().Update(ctx, existing); err != nil {
		return fmt.Errorf("failed updating downstream write-back instance status: %w", err)
	}

	return nil
}

func (r *InstanceReconciler) reconcileQuotaClaim(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) (*metav1.Condition, error) {
	if r.quotaClientManager == nil {
		return nil, nil
	}

	logger := log.FromContext(ctx)

	projectID := r.resolveProjectID(clusterName, instance)
	projectClient, err := r.quotaClientManager.ClientForProject(ctx, projectID, r.scheme)
	if err != nil {
		return nil, fmt.Errorf("failed getting quota client for project %q: %w", projectID, err)
	}

	claimName := fmt.Sprintf("%s--%s", instance.Namespace, instance.Name)

	requests := []quotav1alpha1.ResourceRequest{
		{
			ResourceType: "compute.datumapis.com/instances",
			Amount:       1,
		},
	}

	cpuMillicores, memMiB, resolved := resolveInstanceResources(instance)
	if !resolved {
		logger.Info("unable to resolve resource amounts from instance spec, claiming instance count only")
	} else {
		requests = append(requests,
			quotav1alpha1.ResourceRequest{
				ResourceType: "compute.datumapis.com/vcpus",
				Amount:       cpuMillicores,
			},
			quotav1alpha1.ResourceRequest{
				ResourceType: "compute.datumapis.com/memory",
				Amount:       memMiB,
			},
		)
	}

	desired := &quotav1alpha1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claimName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				instanceQuotaClaimSourceLabel: r.edgeClusterName,
			},
		},
		Spec: quotav1alpha1.ResourceClaimSpec{
			ConsumerRef: quotav1alpha1.ConsumerRef{
				APIGroup: "resourcemanager.miloapis.com",
				Kind:     "Project",
				Name:     projectID,
			},
			ResourceRef: quotav1alpha1.UnversionedObjectReference{
				APIGroup:  "compute.datumapis.com",
				Kind:      "Instance",
				Name:      instance.Name,
				Namespace: instance.Namespace,
			},
			Requests: requests,
		},
	}

	var existing quotav1alpha1.ResourceClaim
	if err := projectClient.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed getting resource claim: %w", err)
		}
		if err := projectClient.Create(ctx, desired); err != nil {
			return nil, fmt.Errorf("failed creating resource claim: %w", err)
		}
		return nil, nil
	}

	grantedCondition := apimeta.FindStatusCondition(existing.Status.Conditions, quotav1alpha1.ResourceClaimGranted)
	return grantedCondition, nil
}

func resolveInstanceResources(instance *computev1alpha.Instance) (cpuMillicores int64, memMiB int64, resolved bool) {
	rt := instance.Spec.Runtime
	if rt.Sandbox != nil {
		var totalCPU resource.Quantity
		var totalMem resource.Quantity
		allSet := true
		for _, c := range rt.Sandbox.Containers {
			if c.Resources == nil || c.Resources.Limits == nil {
				allSet = false
				break
			}
			cpu, hasCPU := c.Resources.Limits[corev1.ResourceCPU]
			mem, hasMem := c.Resources.Limits[corev1.ResourceMemory]
			if !hasCPU || !hasMem {
				allSet = false
				break
			}
			totalCPU.Add(cpu)
			totalMem.Add(mem)
		}
		if !allSet || len(rt.Sandbox.Containers) == 0 {
			return 0, 0, false
		}
		return totalCPU.MilliValue(), totalMem.Value() / (1024 * 1024), true
	}

	cpu, hasCPU := rt.Resources.Requests[corev1.ResourceCPU]
	mem, hasMem := rt.Resources.Requests[corev1.ResourceMemory]
	if !hasCPU || !hasMem {
		return 0, 0, false
	}
	return cpu.MilliValue(), mem.Value() / (1024 * 1024), true
}

// networkFailureChecker is a function that checks if a network creation failure
// has occurred. It returns a boolean indicating if a failure has occurred, a
// message describing the failure, and an error if the check fails.
type networkFailureChecker func(ctx context.Context, upstreamClient client.Client, instance *computev1alpha.Instance) (failed bool, message string, err error)

func (r *InstanceReconciler) reconcileInstanceReadyCondition(
	ctx context.Context,
	clusterClient client.Client,
	instance *computev1alpha.Instance,
	networkFailureChecker networkFailureChecker,
) (changed bool, err error) {
	logger := log.FromContext(ctx)

	quotaGrantedCondition := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceQuotaGranted)
	if quotaGrantedCondition != nil && quotaGrantedCondition.Status == metav1.ConditionFalse {
		msg := quotaGrantedCondition.Message
		changed = apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceProgrammed,
			Status:             metav1.ConditionFalse,
			Reason:             computev1alpha.InstanceProgrammedReasonPendingQuota,
			Message:            msg,
			ObservedGeneration: instance.Generation,
		})
		changed = apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceRunning,
			Status:             metav1.ConditionFalse,
			Reason:             computev1alpha.InstanceProgrammedReasonPendingQuota,
			Message:            msg,
			ObservedGeneration: instance.Generation,
		}) || changed
		changed = apimeta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.InstanceReady,
			Status:             metav1.ConditionFalse,
			Reason:             computev1alpha.InstanceProgrammedReasonPendingQuota,
			Message:            msg,
			ObservedGeneration: instance.Generation,
		}) || changed
		return changed, nil
	}

	readyCondition := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceReady)
	if readyCondition == nil {
		readyCondition = &metav1.Condition{
			Type:               computev1alpha.InstanceReady,
			Status:             metav1.ConditionFalse,
			Reason:             computev1alpha.InstanceProgrammedReasonPendingProgramming,
			ObservedGeneration: instance.Generation,
			Message:            "Instance has not been programmed",
		}
	} else {
		readyCondition = readyCondition.DeepCopy()
	}

	if instance.Spec.Controller != nil && len(instance.Spec.Controller.SchedulingGates) > 0 {
		var schedulingGateNames []string
		for _, gate := range instance.Spec.Controller.SchedulingGates {
			schedulingGateNames = append(schedulingGateNames, gate.Name)
		}

		networkCreationFailure, networkCreationFailureMessage, err := networkFailureChecker(ctx, clusterClient, instance)
		if err != nil {
			return false, fmt.Errorf("failed checking for network creation failure: %w", err)
		}

		readyCondition.Status = metav1.ConditionFalse
		if networkCreationFailure {
			readyCondition.Reason = "NetworkFailedToCreate"
			readyCondition.Message = networkCreationFailureMessage
		} else {
			readyCondition.Reason = computev1alpha.InstanceReadyReasonSchedulingGatesPresent
			readyCondition.Message = fmt.Sprintf("Scheduling gates present: %s", strings.Join(schedulingGateNames, ", "))
		}

		return apimeta.SetStatusCondition(&instance.Status.Conditions, *readyCondition), nil
	}

	pendingReason := "Pending"
	programmedCondition := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceProgrammed)
	if programmedCondition == nil || programmedCondition.Status != metav1.ConditionTrue {
		logger.Info("instance is not programmed", "instance", instance.Name)

		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = computev1alpha.InstanceProgrammedReasonPendingProgramming
		if programmedCondition != nil && programmedCondition.Reason != pendingReason {
			readyCondition.Reason = programmedCondition.Reason
		}

		readyCondition.Message = "Instance has not been programmed"
		if programmedCondition != nil && programmedCondition.Status != metav1.ConditionUnknown {
			readyCondition.Message = programmedCondition.Message
		}

		return apimeta.SetStatusCondition(&instance.Status.Conditions, *readyCondition), nil
	}

	logger.Info("instance is programmed", "instance", instance.Name)

	runningCondition := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceRunning)
	if runningCondition == nil || runningCondition.Status != metav1.ConditionTrue {
		logger.Info("instance is not running", "instance", instance.Name)

		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = pendingReason
		if runningCondition != nil && runningCondition.Reason != pendingReason {
			readyCondition.Reason = runningCondition.Reason
		}

		readyCondition.Message = "Instance is not running"
		if runningCondition != nil && runningCondition.Status != metav1.ConditionUnknown {
			readyCondition.Message = runningCondition.Message
		}

		return apimeta.SetStatusCondition(&instance.Status.Conditions, *readyCondition), nil
	}

	readyCondition.Status = metav1.ConditionTrue
	readyCondition.Reason = computev1alpha.InstanceReadyReasonRunning
	readyCondition.Message = "Instance is ready"

	return apimeta.SetStatusCondition(&instance.Status.Conditions, *readyCondition), nil
}

// Rough way to propagate creation errors up to the instance as soon as possible.
// Lots of room for improvement here.
func (r *InstanceReconciler) checkForNetworkCreationFailure(ctx context.Context, upstreamClient client.Client, instance *computev1alpha.Instance) (failed bool, message string, err error) {
	workloadDeploymentRef := metav1.GetControllerOf(instance)
	if workloadDeploymentRef == nil {
		return false, "", fmt.Errorf("instance is not owned by a workload deployment")
	}

	var workloadDeployment computev1alpha.WorkloadDeployment
	workloadDeploymentObjectKey := client.ObjectKey{
		Namespace: instance.Namespace,
		Name:      workloadDeploymentRef.Name,
	}
	if err := upstreamClient.Get(ctx, workloadDeploymentObjectKey, &workloadDeployment); err != nil {
		return false, "", fmt.Errorf("failed fetching workload deployment: %w", err)
	}

	for i := range instance.Spec.NetworkInterfaces {
		var networkBinding networkingv1alpha.NetworkBinding
		networkBindingObjectKey := client.ObjectKey{
			Namespace: workloadDeployment.Namespace,
			Name:      fmt.Sprintf("%s-net-%d", workloadDeployment.Name, i),
		}

		if err := upstreamClient.Get(ctx, networkBindingObjectKey, &networkBinding); client.IgnoreNotFound(err) != nil {
			return false, "", fmt.Errorf("failed checking for existing network binding: %w", err)
		}

		condition := apimeta.FindStatusCondition(networkBinding.Status.Conditions, networkingv1alpha.NetworkBindingReady)
		if condition != nil && condition.Status == metav1.ConditionFalse && condition.Reason == "NetworkFailedToCreate" {
			return true, condition.Message, nil
		}
	}

	return false, "", nil
}

// resolveProjectID returns the Milo project ID to use for quota calls. When
// projectIDForInstance is set it delegates to that function; otherwise it falls
// back to string(clusterName), which is correct for Milo-mode deployments where
// the cluster name IS the project name.
func (r *InstanceReconciler) resolveProjectID(clusterName multicluster.ClusterName, instance *computev1alpha.Instance) string {
	if r.projectIDForInstance != nil {
		return r.projectIDForInstance(clusterName, instance)
	}
	return string(clusterName)
}

// resolveClusterNameForProject returns the multicluster ClusterName for the
// given project ID. When clusterNameForProject is set it delegates to that
// function; otherwise it falls back to multicluster.ClusterName(projectID),
// which is correct for Milo-mode deployments where the cluster name IS the
// project name.
func (r *InstanceReconciler) resolveClusterNameForProject(projectID string) multicluster.ClusterName {
	if r.clusterNameForProject != nil {
		return r.clusterNameForProject(projectID)
	}
	return multicluster.ClusterName(projectID)
}

// SetupWithManager sets up the controller with the Manager.
//
// quotaRestConfig is the REST config used to reach Milo project control planes
// for ResourceClaim management. Pass nil to disable quota accounting.
//
// projectIDForInstance derives the Milo project ID for each reconcile request.
// In Milo mode pass nil (falls back to using ClusterName). In single-cell mode
// pass a function that returns instance.Namespace.
//
// clusterNameForProject maps a project ID back to the multicluster ClusterName.
// In Milo mode pass nil (falls back to ClusterName(projectID)). In single-cell
// mode pass a function that always returns "single".
func (r *InstanceReconciler) SetupWithManager(
	mgr mcmanager.Manager,
	quotaRestConfig *rest.Config,
	projectIDForInstance func(multicluster.ClusterName, *computev1alpha.Instance) string,
	edgeClusterName string,
	clusterNameForProject func(projectID string) multicluster.ClusterName,
) error {
	r.mgr = mgr
	r.scheme = mgr.GetLocalManager().GetScheme()
	r.edgeClusterName = edgeClusterName
	r.projectIDForInstance = projectIDForInstance
	r.clusterNameForProject = clusterNameForProject
	if quotaRestConfig != nil {
		r.quotaClientManager = quota.New(quotaRestConfig)
	}

	r.finalizers = finalizer.NewFinalizers()
	if err := r.finalizers.Register(instanceControllerFinalizer, r); err != nil {
		return fmt.Errorf("failed to register finalizer: %w", err)
	}

	edgeClusterNameVal := r.edgeClusterName

	return mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.Instance{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Watches(
			&quotav1alpha1.ResourceClaim{},
			func(_ multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
				return handler.TypedEnqueueRequestsFromMapFunc(
					func(ctx context.Context, obj client.Object) []mcreconcile.Request {
						claim := obj.(*quotav1alpha1.ResourceClaim)
						return []mcreconcile.Request{
							{
								Request: reconcile.Request{
									NamespacedName: types.NamespacedName{
										Namespace: claim.Spec.ResourceRef.Namespace,
										Name:      claim.Spec.ResourceRef.Name,
									},
								},
								ClusterName: r.resolveClusterNameForProject(claim.Spec.ConsumerRef.Name),
							},
						}
					},
				)
			},
			mcbuilder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetLabels()[instanceQuotaClaimSourceLabel] == edgeClusterNameVal
			})),
		).
		Complete(r)
}
