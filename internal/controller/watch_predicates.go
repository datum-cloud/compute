// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// An Instance or claim is written many times while it starts: finalizers,
// interface status, runtime conditions, DNS status and more, each by a
// different controller. The watchers below only care about a few of those
// fields, so they wake on changes to those and let the rest go by. Creates,
// deletes and generic events always pass.
//
// Each predicate covers at least what its reconciler reads. When a reconciler
// starts reading another field, add it here, or that change will not wake it.
// Periodic cache resyncs (same resourceVersion) still pass, so a missed event
// is eventually picked up.

// changeFilter builds an update-only predicate for one object type.
func changeFilter[T client.Object](changed func(old, new T) bool) predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			old, okOld := e.ObjectOld.(T)
			updated, okNew := e.ObjectNew.(T)
			if !okOld || !okNew {
				return true
			}
			return old.GetResourceVersion() == updated.GetResourceVersion() ||
				metadataChanged(old, updated) || changed(old, updated)
		},
	}
}

// metadataChanged covers what every watcher here reads from metadata: which
// object it is, its spec revision, its labels and owners, and whether it is
// being deleted. Finalizer and annotation bookkeeping is left out.
func metadataChanged(old, updated client.Object) bool {
	return old.GetUID() != updated.GetUID() ||
		old.GetGeneration() != updated.GetGeneration() ||
		!equality.Semantic.DeepEqual(old.GetLabels(), updated.GetLabels()) ||
		!equality.Semantic.DeepEqual(old.GetOwnerReferences(), updated.GetOwnerReferences()) ||
		!equality.Semantic.DeepEqual(old.GetDeletionTimestamp(), updated.GetDeletionTimestamp())
}

// conditionStatusChanged reports whether the status of any of the given
// condition types differs.
func conditionStatusChanged(old, updated []metav1.Condition, types ...string) bool {
	for _, conditionType := range types {
		a := apimeta.FindStatusCondition(old, conditionType)
		b := apimeta.FindStatusCondition(updated, conditionType)
		if (a == nil) != (b == nil) || (a != nil && a.Status != b.Status) {
			return true
		}
	}
	return false
}

// conditionChanged reports whether a condition's status, reason or message
// differs.
func conditionChanged(old, updated []metav1.Condition, conditionType string) bool {
	a := apimeta.FindStatusCondition(old, conditionType)
	b := apimeta.FindStatusCondition(updated, conditionType)
	if a == nil || b == nil {
		return (a == nil) != (b == nil)
	}
	return a.Status != b.Status || a.Reason != b.Reason || a.Message != b.Message
}

// instanceChangedForDeployment passes the Instance updates the
// WorkloadDeployment reconciler acts on: the instance control actions, gates,
// suspension, revision and the condition rollup into deployment status.
func instanceChangedForDeployment() predicate.Predicate {
	return changeFilter(func(old, updated *computev1alpha.Instance) bool {
		return old.Status.Suspended != updated.Status.Suspended ||
			!equality.Semantic.DeepEqual(old.Status.Controller, updated.Status.Controller) ||
			conditionStatusChanged(old.Status.Conditions, updated.Status.Conditions,
				computev1alpha.InstanceReady,
				computev1alpha.InstanceProgrammed,
				computev1alpha.InstanceQuotaGranted,
				computev1alpha.ReferencedDataReady,
			) ||
			// The deployment reports the reason and message of a start failure.
			conditionChanged(old.Status.Conditions, updated.Status.Conditions, computev1alpha.InstanceAvailable)
	})
}

// networkInterfaceClaimChangedForDeployment passes the claim updates that can
// release an instance's Network gate or explain why it is held.
func networkInterfaceClaimChangedForDeployment() predicate.Predicate {
	return changeFilter(func(old, updated *networkingv1alpha.NetworkInterfaceClaim) bool {
		for _, conditionType := range []string{
			networkingv1alpha.NetworkInterfaceClaimBound,
			networkingv1alpha.NetworkInterfaceClaimAllocated,
			networkingv1alpha.NetworkInterfaceClaimPrepared,
		} {
			if conditionChanged(old.Status.Conditions, updated.Status.Conditions, conditionType) {
				return true
			}
		}
		return false
	})
}

// instanceChangedForDNSPublication passes the Instance updates that can change
// what the internal DNS publisher writes: eligibility (Ready, RuntimeReady),
// the interfaces and their addresses, and the reported publication status,
// which it restores if another writer changes it.
func instanceChangedForDNSPublication() predicate.Predicate {
	return changeFilter(func(old, updated *computev1alpha.Instance) bool {
		return conditionStatusChanged(old.Status.Conditions, updated.Status.Conditions,
			computev1alpha.InstanceReady,
			computev1alpha.InstanceRuntimeReady,
		) ||
			dnsInterfacesChanged(old.Status.NetworkInterfaces, updated.Status.NetworkInterfaces) ||
			!equality.Semantic.DeepEqual(old.Status.DNS, updated.Status.DNS)
	})
}

// dnsInterfacesChanged compares the parts of interface status the DNS
// publisher reads: which interface, its reference, whether it is allocated and
// programmed, and its addresses.
func dnsInterfacesChanged(old, updated []computev1alpha.InstanceNetworkInterfaceStatus) bool {
	if len(old) != len(updated) {
		return true
	}
	for i := range old {
		a, b := old[i], updated[i]
		if a.Name != b.Name ||
			!equality.Semantic.DeepEqual(a.NetworkInterfaceRef, b.NetworkInterfaceRef) ||
			!equality.Semantic.DeepEqual(a.Addresses, b.Addresses) ||
			conditionStatusChanged(a.Conditions, b.Conditions,
				computev1alpha.InstanceNetworkInterfaceAllocated,
				computev1alpha.InstanceNetworkInterfaceProgrammed,
			) {
			return true
		}
	}
	return false
}
