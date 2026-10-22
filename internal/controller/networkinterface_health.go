// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// overlayInterfaceProgrammed replaces the claim's Programmed copy on an
// interface status with the bound NetworkInterface's own. The data plane
// reports a withdrawn advertisement on the interface, and the claim's copy does
// not follow it.
func overlayInterfaceProgrammed(
	ctx context.Context,
	clusterClient client.Client,
	namespace string,
	status *computev1alpha.InstanceNetworkInterfaceStatus,
) error {
	ref := status.NetworkInterfaceRef
	if ref == nil || ref.Name == "" {
		return nil
	}

	var networkInterface networkingv1alpha.NetworkInterface
	if err := clusterClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, &networkInterface); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed fetching network interface: %w", err)
	}

	programmed := apimeta.FindStatusCondition(networkInterface.Status.Conditions, networkingv1alpha.NetworkInterfaceProgrammed)
	if programmed == nil {
		return nil
	}

	mirrored := *programmed
	mirrored.ObservedGeneration = 0
	apimeta.RemoveStatusCondition(&status.Conditions, computev1alpha.InstanceNetworkInterfaceProgrammed)
	status.Conditions = append(status.Conditions, mirrored)
	return nil
}

// unprogrammedInterface reports the first interface the data plane says it no
// longer carries. Unknown is not reported: it is the state of every interface
// before its sandbox is attached.
func unprogrammedInterface(instance *computev1alpha.Instance) (string, bool) {
	for _, status := range instance.Status.NetworkInterfaces {
		programmed := apimeta.FindStatusCondition(status.Conditions, computev1alpha.InstanceNetworkInterfaceProgrammed)
		if programmed == nil || programmed.Status != metav1.ConditionFalse {
			continue
		}
		detail := programmed.Message
		if detail == "" {
			detail = programmed.Reason
		}
		return fmt.Sprintf("Interface %q is not programmed: %s", status.Name, detail), true
	}
	return "", false
}

// mapNetworkInterfaceToInstance follows an interface to the Instance that
// controls the claim holding it.
func mapNetworkInterfaceToInstance(
	ctx context.Context,
	clusterClient client.Client,
	clusterName multicluster.ClusterName,
	networkInterface *networkingv1alpha.NetworkInterface,
) []mcreconcile.Request {
	if networkInterface.Spec.ClaimRef == nil || networkInterface.Spec.ClaimRef.Name == "" {
		return nil
	}

	var claim networkingv1alpha.NetworkInterfaceClaim
	key := client.ObjectKey{Namespace: networkInterface.Namespace, Name: networkInterface.Spec.ClaimRef.Name}
	if err := clusterClient.Get(ctx, key, &claim); err != nil {
		if !apierrors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "failed fetching claim for network interface watch", "claim", key)
		}
		return nil
	}

	owner := metav1.GetControllerOf(&claim)
	if owner == nil || owner.Kind != internalDNSInstanceKind || owner.APIVersion != computev1alpha.GroupVersion.String() {
		return nil
	}
	return []mcreconcile.Request{{
		Request: reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: claim.Namespace, Name: owner.Name},
		},
		ClusterName: clusterName,
	}}
}

// networkInterfaceProgrammedChanged admits an interface update only when its
// Programmed condition moves, so the HolderAvailable writes the instance
// controller makes to the same interface do not come back as events.
func networkInterfaceProgrammedChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldInterface, okOld := e.ObjectOld.(*networkingv1alpha.NetworkInterface)
			newInterface, okNew := e.ObjectNew.(*networkingv1alpha.NetworkInterface)
			if !okOld || !okNew {
				return true
			}
			oldProgrammed := apimeta.FindStatusCondition(oldInterface.Status.Conditions, networkingv1alpha.NetworkInterfaceProgrammed)
			newProgrammed := apimeta.FindStatusCondition(newInterface.Status.Conditions, networkingv1alpha.NetworkInterfaceProgrammed)
			if oldProgrammed == nil || newProgrammed == nil {
				return oldProgrammed != newProgrammed
			}
			return oldProgrammed.Status != newProgrammed.Status ||
				oldProgrammed.Reason != newProgrammed.Reason ||
				oldProgrammed.Message != newProgrammed.Message
		},
	}
}
