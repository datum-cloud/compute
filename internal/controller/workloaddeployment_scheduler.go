// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// WorkloadDeploymentScheduler schedules a WorkloadDeployment
type WorkloadDeploymentScheduler struct {
	mgr mcmanager.Manager
}

func (r *WorkloadDeploymentScheduler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
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

	if !deployment.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	logger.Info("scheduling deployment")
	defer logger.Info("scheduling complete")

	// TODO(jreese) improve!
	// The first iteration of this scheduler will be very simple and only look for
	// the first available location that is viable for the deployment. In the
	// future, we could see a more advanced system similar to the Kubernetes
	// scheduler itself.

	// Step 1: Get Locations
	var locations networkingv1alpha.LocationList
	if err := cl.GetClient().List(ctx, &locations); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list locations: %w", err)
	}

	if len(locations.Items) == 0 {
		// Should only be the case in new environments if workloads are created
		// prior to location registration.

		changed := apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:               "Available",
			Status:             metav1.ConditionFalse,
			Reason:             "NoLocations",
			ObservedGeneration: deployment.Generation,
			Message:            "No locations are registered with the system.",
		})
		if changed {
			// TODO(jreese) investigate kubevirt / other operators for better tracking
			// of updates to the status. I seem to remember a "builder" of sorts that
			// looked rather nice.
			if err := cl.GetClient().Status().Update(ctx, &deployment); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update deployment status: %w", err)
			}
		}

		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// TODO(jreese) define standard Topology keys somewhere

	var selectedLocation *networkingv1alpha.Location
	for _, location := range locations.Items {
		cityCode, ok := location.Spec.Topology["topology.datum.net/city-code"]
		if ok && cityCode == deployment.Spec.CityCode {
			selectedLocation = &location
			break
		}
	}

	if selectedLocation == nil {
		changed := apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:               "Available",
			Status:             metav1.ConditionFalse,
			Reason:             "NoCandidateLocations",
			ObservedGeneration: deployment.Generation,
			Message:            "No locations are candidates for this deployment.",
		})
		if changed {
			if err := cl.GetClient().Status().Update(ctx, &deployment); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update deployment status: %w", err)
			}
		}
	} else {
		deployment.Status.Location = &networkingv1alpha.LocationReference{
			Name:      selectedLocation.Name,
			Namespace: selectedLocation.Namespace,
		}

		// TODO(jreese) make sure we don't run into update conflicts with the update
		// of the spec then status here. Just can't remember if it's an issue.

		apimeta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:               "Available",
			Status:             metav1.ConditionFalse,
			Reason:             "LocationAssigned",
			ObservedGeneration: deployment.Generation,
			Message:            "Deployment has been assigned a location.",
		})

		if err := cl.GetClient().Status().Update(ctx, &deployment); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update deployment status: %w", err)
		}

	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadDeploymentScheduler) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.WorkloadDeployment{}, mcbuilder.WithPredicates(
			predicate.NewPredicateFuncs(func(object client.Object) bool {
				// Don't process deployments that have been scheduled
				o := object.(*computev1alpha.WorkloadDeployment)
				return o.Status.Location == nil
			}),
		)).
		Named("workload-deployment-scheduler").
		Complete(r)
}
