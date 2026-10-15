package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// InstanceTypeReconciler reconciles the InstanceTypes in each project control
// plane. The catalog reaches a project through the compute ServiceConfiguration
// when the project activates compute, so every project holds its own copy and
// this controller maintains each copy's status in that project.
type InstanceTypeReconciler struct {
	mgr mcmanager.Manager
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instancetypes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instancetypes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instancetypes/finalizers,verbs=update

func (r *InstanceTypeReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()

	var instanceType computev1alpha.InstanceType
	if err := c.Get(ctx, req.NamespacedName, &instanceType); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch InstanceType")
		return ctrl.Result{}, err
	}

	// Record when the type was deprecated, so the Deprecated -> Disabled
	// transition can be gated on the deprecation grace period. Phase never moves
	// backwards, so the timestamp is only ever set once and never cleared.
	if instanceType.Spec.Lifecycle.Phase == computev1alpha.InstanceTypePhaseDeprecated &&
		instanceType.Status.DeprecatedAt == nil {
		now := metav1.NewTime(time.Now())
		instanceType.Status.DeprecatedAt = &now
	}

	readyCondition := metav1.Condition{
		Type:               computev1alpha.InstanceTypeConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Valid",
		Message:            "InstanceType is ready and valid",
		ObservedGeneration: instanceType.Generation,
	}

	if rep := instanceType.Spec.Lifecycle.ReplacementInstanceType; rep != "" {
		var replacement computev1alpha.InstanceType
		if err := c.Get(ctx, types.NamespacedName{Name: rep}, &replacement); err != nil {
			if errors.IsNotFound(err) {
				readyCondition.Status = metav1.ConditionFalse
				readyCondition.Reason = "ReplacementNotFound"
				readyCondition.Message = "Replacement instance type does not exist"
			} else {
				return ctrl.Result{}, err
			}
		} else if replacement.Spec.Lifecycle.Phase == computev1alpha.InstanceTypePhaseDisabled {
			readyCondition.Status = metav1.ConditionFalse
			readyCondition.Reason = "ReplacementDisabled"
			readyCondition.Message = "Replacement instance type is disabled"
		}
	}

	meta.SetStatusCondition(&instanceType.Status.Conditions, readyCondition)
	if err := c.Status().Update(ctx, &instanceType); err != nil {
		logger.Error(err, "unable to update InstanceType status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager watches InstanceTypes in every engaged project control
// plane.
func (r *InstanceTypeReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr

	return mcbuilder.ControllerManagedBy(mgr).
		Named("instancetype").
		For(&computev1alpha.InstanceType{},
			// Instance types live in project control planes, never in the
			// management cluster this manager runs against.
			mcbuilder.WithEngageWithLocalCluster(false),
		).
		Complete(r)
}
