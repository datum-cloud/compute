// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// ensureNetworkBinding declares that the deployment's network is needed in the
// location the deployment is running in, by writing a NetworkBinding next to the
// hub WorkloadDeployment. NSO's presence controller folds every binding for the
// same network and location into one shared NetworkContext and propagates it to
// the cells serving that location, where the interface claim path reads it.
//
// The binding is one per WorkloadDeployment, named after it, rather than one per
// (network, location) pair. A pair-named object would be written by every
// deployment sharing the pair, making each of them a partial owner of something
// shared and leaving nobody able to remove it. Naming it after the deployment
// keeps it wholly compute's: it is created when the deployment lands somewhere,
// and the hub garbage-collects it with its owner.
//
// It returns the binding so the caller can report what NSO says about it. A nil
// binding means there is nothing to declare yet, not that anything failed.
func (r *WorkloadDeploymentFederator) ensureNetworkBinding(
	ctx context.Context,
	hubDeployment *computev1alpha.WorkloadDeployment,
) (*networkingv1alpha.NetworkBinding, error) {
	// The location is written by the cell that serves the deployment and reaches
	// the hub through Karmada status aggregation, so it is absent for as long as
	// nothing has placed the deployment. There is no presence to ask for until
	// then, and a binding without a location cannot be created at all.
	if hubDeployment.Status.Location == nil || hubDeployment.Status.Location.Name == "" {
		return nil, nil
	}

	network, ok := deploymentNetworkRef(hubDeployment)
	if !ok {
		return nil, nil
	}
	location := *hubDeployment.Status.Location

	key := client.ObjectKey{Namespace: hubDeployment.Namespace, Name: hubDeployment.Name}
	var existing networkingv1alpha.NetworkBinding
	err := r.FederationClient.Get(ctx, key, &existing)
	switch {
	case apierrors.IsNotFound(err):
		return r.createNetworkBinding(ctx, hubDeployment, network, location)
	case err != nil:
		return nil, fmt.Errorf("failed reading network binding %s/%s: %w", key.Namespace, key.Name, err)
	}

	// Everything below either rewrites or removes the object under this name. A
	// binding this deployment does not own belongs to another consumer of the
	// same presence, and taking it over would delete a declaration compute never
	// made.
	if !metav1.IsControlledBy(&existing, hubDeployment) {
		return nil, fmt.Errorf("network binding %s/%s is not controlled by this workload deployment",
			key.Namespace, key.Name)
	}

	if !existing.DeletionTimestamp.IsZero() {
		// A recreate is already in flight. The binding watch reconciles this
		// deployment again once the object is gone.
		return &existing, nil
	}

	// spec.network and spec.location are immutable on a NetworkBinding: a
	// deployment whose network was edited, or which a cell now serves from a
	// different location, is asking for a different presence. Delete the old
	// declaration and let the next pass make the new one, so the crossing is a
	// visible replacement rather than a silently rejected update.
	if !equality.Semantic.DeepEqual(existing.Spec.Network, network) ||
		!equality.Semantic.DeepEqual(existing.Spec.Location, location) {
		log.FromContext(ctx).Info("network binding declares a different presence, recreating",
			"binding", key.String(),
			"network", network.Name, "location", location.Name)
		if err := r.FederationClient.Delete(ctx, &existing, client.Preconditions{UID: &existing.UID}); err != nil {
			return nil, client.IgnoreNotFound(fmt.Errorf("failed deleting diverged network binding %s: %w", key, err))
		}
		return nil, nil
	}

	if err := r.reconcileNetworkBindingLabels(ctx, &existing, network, location); err != nil {
		return nil, err
	}
	return &existing, nil
}

// createNetworkBinding writes the binding for a deployment that does not have
// one. The owner reference is a real one to the hub WorkloadDeployment in the
// same namespace, which is what releases the binding when the deployment goes
// away — no finalizer of compute's is involved, and nothing counts the other
// consumers of the presence.
//
// BlockOwnerDeletion is deliberately not set: it would require update access to
// the owner's finalizers subresource wherever OwnerReferencesPermissionEnforcement
// is enabled, and nothing here needs the deployment's own removal held up.
func (r *WorkloadDeploymentFederator) createNetworkBinding(
	ctx context.Context,
	hubDeployment *computev1alpha.WorkloadDeployment,
	network networkingv1alpha.NetworkRef,
	location networkingv1alpha.LocationReference,
) (*networkingv1alpha.NetworkBinding, error) {
	binding := &networkingv1alpha.NetworkBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hubDeployment.Name,
			Namespace: hubDeployment.Namespace,
			Labels:    networkBindingLabels(network, location),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: computev1alpha.GroupVersion.String(),
				Kind:       kindWorkloadDeployment,
				Name:       hubDeployment.Name,
				UID:        hubDeployment.UID,
				Controller: ptr.To(true),
			}},
		},
		Spec: networkingv1alpha.NetworkBindingSpec{
			Network:  network,
			Location: location,
			Consumer: &networkingv1alpha.NetworkBindingConsumer{
				APIGroup: computev1alpha.GroupVersion.Group,
				Kind:     kindWorkloadDeployment,
				Name:     hubDeployment.Name,
			},
		},
	}

	if err := r.FederationClient.Create(ctx, binding); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// The previous declaration is still going away. The next pass, or the
			// binding watch, picks it up.
			return nil, nil
		}
		return nil, fmt.Errorf("failed creating network binding %s/%s: %w",
			binding.Namespace, binding.Name, err)
	}

	log.FromContext(ctx).Info("created network binding",
		"binding", client.ObjectKeyFromObject(binding).String(),
		"network", network.Name, "location", location.Name)
	return binding, nil
}

// reconcileNetworkBindingLabels brings the labels compute stamps up to date
// without touching anything else on the object.
//
// NSO's presence controller patches the network's UID onto the binding, and that
// label is what its garbage collection keys on. A CreateOrUpdate that assigns
// the whole label map would strip it on every pass, which both breaks NSO's
// bookkeeping and puts the two controllers in a write loop against each other.
// The merge patch below is computed from a copy of the live object and only ever
// adds keys, so a key compute does not set is not in the patch at all.
func (r *WorkloadDeploymentFederator) reconcileNetworkBindingLabels(
	ctx context.Context,
	binding *networkingv1alpha.NetworkBinding,
	network networkingv1alpha.NetworkRef,
	location networkingv1alpha.LocationReference,
) error {
	desired := networkBindingLabels(network, location)

	stale := false
	for k, v := range desired {
		if binding.Labels[k] != v {
			stale = true
			break
		}
	}
	if !stale {
		return nil
	}

	patch := client.MergeFrom(binding.DeepCopy())
	if binding.Labels == nil {
		binding.Labels = map[string]string{}
	}
	for k, v := range desired {
		binding.Labels[k] = v
	}
	if err := r.FederationClient.Patch(ctx, binding, patch); err != nil {
		return fmt.Errorf("failed labelling network binding %s/%s: %w",
			binding.Namespace, binding.Name, err)
	}
	return nil
}

// networkBindingLabels returns the labels a consumer stamps on its binding. They
// are what makes the consumers of a presence findable as a list, and they are
// the only metadata on the binding compute owns.
func networkBindingLabels(
	network networkingv1alpha.NetworkRef,
	location networkingv1alpha.LocationReference,
) map[string]string {
	return map[string]string{
		networkingv1alpha.NetworkLabel:  network.Name,
		networkingv1alpha.LocationLabel: location.Name,
	}
}

// deploymentNetworkRef returns the network a deployment's instances attach to.
// The interface list is capped at one entry by the API, and a deployment without
// one asks for no presence at all.
func deploymentNetworkRef(deployment *computev1alpha.WorkloadDeployment) (networkingv1alpha.NetworkRef, bool) {
	interfaces := deployment.Spec.Template.Spec.NetworkInterfaces
	if len(interfaces) == 0 || interfaces[0].Network.Name == "" {
		return networkingv1alpha.NetworkRef{}, false
	}
	return interfaces[0].Network, true
}

// mapNetworkBindingToRequest maps an event on a hub NetworkBinding back to the
// project WorkloadDeployment that declared it.
//
// The binding carries no cross-plane identity of its own. Its controller owner
// reference names the hub deployment, whose name is the same on every plane, and
// the hub namespace carries the project namespace and cluster this controller
// stamped on it when it created it. Bindings written by other consumers of the
// same presence have no such owner and are not this controller's to act on.
func (r *WorkloadDeploymentFederator) mapNetworkBindingToRequest(
	ctx context.Context,
	binding *networkingv1alpha.NetworkBinding,
) []mcreconcile.Request {
	logger := log.FromContext(ctx)

	owner := metav1.GetControllerOf(binding)
	if owner == nil || owner.Kind != kindWorkloadDeployment || owner.APIVersion != computev1alpha.GroupVersion.String() {
		return nil
	}

	var ns corev1.Namespace
	if err := r.FederationCluster.GetClient().Get(ctx, types.NamespacedName{Name: binding.Namespace}, &ns); err != nil {
		logger.V(1).Info("unable to resolve hub namespace for network binding; dropping event",
			"hubNamespace", binding.Namespace, "error", err)
		return nil
	}

	projectNamespace := ns.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	clusterName := projectClusterNameFromLabel(ns.Labels[downstreamclient.UpstreamOwnerClusterNameLabel])
	if projectNamespace == "" || clusterName == "" {
		logger.Error(nil, "hub namespace is missing upstream identity labels; dropping network binding event",
			"hubNamespace", binding.Namespace, "name", binding.Name)
		return nil
	}

	// Same reasoning as the downstream deployment mapping: an unengaged project
	// cluster has no WorkloadDeployment to reconcile, and enqueuing one would hot
	// loop against a control plane with no compute types.
	if _, err := r.mgr.GetCluster(ctx, multicluster.ClusterName(clusterName)); err != nil {
		logger.V(1).Info("project cluster not engaged for network binding mapping; dropping event",
			"clusterName", clusterName, "hubNamespace", binding.Namespace, "error", err)
		return nil
	}

	return []mcreconcile.Request{{
		ClusterName: multicluster.ClusterName(clusterName),
		Request: ctrl.Request{
			NamespacedName: types.NamespacedName{
				Namespace: projectNamespace,
				Name:      owner.Name,
			},
		},
	}}
}

// networkBindingRefusal translates what NSO says about a binding into the
// deployment's Available condition, or returns nil when there is nothing worth
// reporting.
//
// Only refusals a person can act on are surfaced. A binding sits at
// NetworkContextNotReady for as long as nothing marks the shared context
// programmed, which is the ordinary steady state and says nothing about the
// deployment. Reporting is all that happens either way: instances are gated on
// their own interface claims, and a binding that stops being ready must not take
// a running deployment apart.
func networkBindingRefusal(binding *networkingv1alpha.NetworkBinding) *metav1.Condition {
	if binding == nil {
		return nil
	}
	ready := apimeta.FindStatusCondition(binding.Status.Conditions, networkingv1alpha.NetworkBindingReady)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		return nil
	}

	switch ready.Reason {
	case networkingv1alpha.NetworkBindingReasonProjectUnresolved,
		networkingv1alpha.NetworkBindingReasonNetworkNotFound,
		networkingv1alpha.NetworkBindingReasonLocationNotAvailable:
	default:
		return nil
	}

	return &metav1.Condition{
		Type:    computev1alpha.WorkloadDeploymentAvailable,
		Status:  metav1.ConditionFalse,
		Reason:  ready.Reason,
		Message: ready.Message,
	}
}

// applyNetworkBindingRefusal folds a refused binding into the status the
// federator is about to write, so the reason a network never arrived is readable
// on the deployment the user has rather than only on an object in the hub.
//
// A deployment that is already available keeps its own answer: what its
// instances are observed to be doing is the stronger statement, and a refusal
// that matters shows up there as instances failing to program.
func applyNetworkBindingRefusal(
	status *computev1alpha.WorkloadDeploymentStatus,
	binding *networkingv1alpha.NetworkBinding,
	observedGeneration int64,
) {
	refusal := networkBindingRefusal(binding)
	if refusal == nil {
		return
	}
	if apimeta.IsStatusConditionTrue(status.Conditions, computev1alpha.WorkloadDeploymentAvailable) {
		return
	}
	refusal.ObservedGeneration = observedGeneration
	apimeta.SetStatusCondition(&status.Conditions, *refusal)
}
