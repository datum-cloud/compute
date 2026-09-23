package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// InstanceTypeReconciler reconciles an InstanceType object
type InstanceTypeReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instancetypes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instancetypes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instancetypes/finalizers,verbs=update

func (r *InstanceTypeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var instanceType computev1alpha.InstanceType
	if err := r.Get(ctx, req.NamespacedName, &instanceType); err != nil {
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
		if err := r.Get(ctx, types.NamespacedName{Name: rep}, &replacement); err != nil {
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
	if err := r.Status().Update(ctx, &instanceType); err != nil {
		logger.Error(err, "unable to update InstanceType status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager runs the controller in mgr but watches InstanceTypes in
// catalog, the cluster holding the instance type catalog, which is not the
// cluster mgr runs against when discovery goes through Milo. r.Client must
// point at catalog as well.
func (r *InstanceTypeReconciler) SetupWithManager(mgr ctrl.Manager, catalog cluster.Cluster) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("instancetype").
		WatchesRawSource(source.Kind(
			catalog.GetCache(),
			&computev1alpha.InstanceType{},
			&handler.TypedEnqueueRequestForObject[*computev1alpha.InstanceType]{},
		)).
		Complete(r)
}
