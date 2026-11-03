// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func instanceDNSReservationName(resolver *unstructured.Unstructured) string {
	return "compute-instances-" + shortHash(string(resolver.GetUID()))
}

// The context, not an Instance, owns the lifetime of compute's namespace policy.
// This reconciler also runs in empty projects, before any endpoint is published.
type internalDNSReservationReconciler struct {
	publisher *InternalDNSPublisherReconciler
}

func (r *internalDNSReservationReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, internalDNSAPITimeout)
	defer cancel()
	cl, err := r.publisher.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	access, err := r.publisher.projectAccess(ctx, string(req.ClusterName), cl)
	if err != nil {
		return ctrl.Result{}, err
	}
	resolver := newDNSObject(dnsResolverContextGVK, req.Namespace, req.Name)
	if err := access.Reader.Get(ctx, req.NamespacedName, resolver); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	enabled, _, _ := unstructured.NestedBool(resolver.Object, internalDNSSpecField, "managedNamespace", "enabled")
	if !enabled || !resolver.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}
	if !unstructuredConditionCurrent(resolver, internalDNSConditionReady) {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	_, changed, err := ensureInstanceDNSReservation(ctx, access, resolver)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if changed {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func ensureInstanceDNSReservation(ctx context.Context, access InternalDNSProjectAccess, resolver *unstructured.Unstructured) (*unstructured.Unstructured, bool, error) {
	_, zoneRef, err := allocatedDNSNamespace(resolver)
	if err != nil {
		return nil, false, err
	}
	owner := metav1.OwnerReference{APIVersion: dnsResolverContextGVK.GroupVersion().String(), Kind: dnsResolverContextGVK.Kind, Name: resolver.GetName(), UID: resolver.GetUID(), Controller: ptrBool(true)}
	spec := map[string]any{internalDNSZoneRefField: zoneRef, internalDNSNameField: instanceDNSScope, internalDNSRecordTypesField: []any{"A", internalDNSRecordTypeAAAA}, "ttlSeconds": int64(30), "reservedDescendants": []any{instanceDNSScope}}
	return ensureDNSObject(ctx, access, dnsRegistrationGVK, resolver.GetNamespace(), instanceDNSReservationName(resolver), map[string]string{internalDNSManagedBy: internalDNSManager}, owner, spec)
}

func (r *internalDNSReservationReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	return mcbuilder.ControllerManagedBy(mgr).Named("internal-dns-instance-reservations").
		For(newDNSObject(dnsResolverContextGVK, "", ""), mcbuilder.WithEngageWithLocalCluster(false)).
		Owns(newDNSObject(dnsRegistrationGVK, "", ""), mcbuilder.WithEngageWithLocalCluster(false)).Complete(r)
}
