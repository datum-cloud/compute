// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// InstanceReconciler reconciles an Instance object
type InstanceReconciler struct {
	mgr mcmanager.Manager
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/finalizers,verbs=update

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

	logger.Info("reconciling instance")
	defer logger.Info("reconcile complete")

	if changed, err := r.reconcileInstanceReadyCondition(ctx, cl.GetClient(), &instance, r.checkForNetworkCreationFailure); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{}, cl.GetClient().Status().Update(ctx, &instance)
	}

	return ctrl.Result{}, nil
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
		// Update Ready condition to False, Reason to "SchedulingGatesPresent"
		// and Message to "Scheduling gates present"

		// Collect a list of scheduling gate names
		var schedulingGateNames []string
		for _, gate := range instance.Spec.Controller.SchedulingGates {
			schedulingGateNames = append(schedulingGateNames, gate.Name)
		}

		networkCreationFailure, networkCreationFailureMessage, err := networkFailureChecker(ctx, clusterClient, instance)
		if err != nil {
			return false, fmt.Errorf("failed checking for network creation failure: %w", err)
		}

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

	// Load the WorkloadDeployment for the instance
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

// SetupWithManager sets up the controller with the Manager.
func (r *InstanceReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.Instance{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Complete(r)
}
