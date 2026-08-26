// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// networkInterfaceHolderAvailable is the condition compute publishes on every
// NetworkInterface an instance holds, reporting whether the holder considers
// itself able to serve. Networking selects members by it without ever learning
// that the holder is a workload.
//
// It is deliberately not NetworkInterfacePhaseAvailable, which reports that no
// claim holds the interface — the opposite of a healthy holder.
const networkInterfaceHolderAvailable = "HolderAvailable"

const (
	// holderReasonNotReported covers an interface whose holder has published no
	// availability state at all, which is not the same as a holder that reported
	// itself unavailable.
	holderReasonNotReported = "HolderNotReported"

	// holderReasonUnavailable is the fallback for a holder that reported itself
	// unavailable without naming a reason.
	holderReasonUnavailable = "HolderUnavailable"
)

const msgHolderNotReported = "The instance holding this interface has not reported an availability state"

// holderAvailableCondition translates an instance's Available condition into the
// condition networking reads on the interfaces it holds.
//
// Anything other than Available=True yields False, carrying the instance's own
// reason and message: Unknown means nobody has vouched for the instance, and a
// member that cannot be vouched for must drain rather than keep taking traffic.
func holderAvailableCondition(instance *computev1alpha.Instance) metav1.Condition {
	condition := metav1.Condition{
		Type:    networkInterfaceHolderAvailable,
		Status:  metav1.ConditionFalse,
		Reason:  holderReasonNotReported,
		Message: msgHolderNotReported,
	}

	available := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceAvailable)
	if available == nil {
		return condition
	}

	if available.Status == metav1.ConditionTrue {
		condition.Status = metav1.ConditionTrue
		condition.Reason = computev1alpha.InstanceAvailableReasonAvailable
		condition.Message = msgInstanceAvailable
		return condition
	}

	if available.Reason != "" {
		condition.Reason = available.Reason
	} else {
		condition.Reason = holderReasonUnavailable
	}
	condition.Message = available.Message
	if condition.Message == "" {
		condition.Message = fmt.Sprintf("Instance %q is not available", instance.Name)
	}
	return condition
}

// reconcileHolderAvailability projects the instance's availability onto every
// NetworkInterface it holds, so networking can decide membership health without
// depending on a fabric that may not be attached.
//
// It runs on every pass rather than only on a transition, so interfaces that
// predate the condition pick it up without their workload being redeployed.
// Writes only when the condition actually moves, so a steady instance produces
// no API traffic.
func (r *InstanceReconciler) reconcileHolderAvailability(
	ctx context.Context,
	clusterClient client.Client,
	instance *computev1alpha.Instance,
) error {
	if !r.NetworkingEnabled {
		return nil
	}

	condition := holderAvailableCondition(instance)

	var errs []error
	for _, interfaceStatus := range instance.Status.NetworkInterfaces {
		ref := interfaceStatus.NetworkInterfaceRef
		if ref == nil || ref.Name == "" {
			continue
		}

		key := client.ObjectKey{Namespace: instance.Namespace, Name: ref.Name}
		var networkInterface networkingv1alpha.NetworkInterface
		if err := clusterClient.Get(ctx, key, &networkInterface); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("get network interface %s: %w", key, err))
			continue
		}

		projected := condition
		projected.ObservedGeneration = networkInterface.Generation
		if !apimeta.SetStatusCondition(&networkInterface.Status.Conditions, projected) {
			continue
		}

		if err := clusterClient.Status().Update(ctx, &networkInterface); err != nil {
			errs = append(errs, fmt.Errorf("update network interface %s status: %w", key, err))
		}
	}

	return errors.Join(errs...)
}
