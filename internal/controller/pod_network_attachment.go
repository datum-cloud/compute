// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	PodPrimaryNetworkEnabledLabel  = "compute.datumapis.com/primary-network-enabled"
	PodPrimaryNetworkAnnotation    = "compute.datumapis.com/primary-network"
	PodNetworkSchedulingGate       = "compute.datumapis.com/network-prepared"
	MultusDefaultNetworkAnnotation = "v1.multus-cni.io/default-network"
)

// PodNetworkAttachmentReconciler attaches opted-in native Pods, including
// DaemonSet Pods, to a network before the scheduler is allowed to bind them.
// It runs only against the cell cluster, not project control planes.
type PodNetworkAttachmentReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=networkinterfaceclaims,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=networkinterfaces,verbs=get

func (r *PodNetworkAttachmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if pod.Labels[PodPrimaryNetworkEnabledLabel] != "true" || pod.Annotations[PodPrimaryNetworkAnnotation] == "" || !pod.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if !hasPodNetworkGate(&pod) {
		// Never change a running Pod's network annotation. Admission must have
		// installed the gate before this controller can act.
		return ctrl.Result{}, nil
	}
	if pod.UID == "" {
		return ctrl.Result{}, nil
	}

	claimName := podNetworkClaimName(pod.UID)
	var claim networkingv1alpha.NetworkInterfaceClaim
	key := types.NamespacedName{Namespace: pod.Namespace, Name: claimName}
	if err := r.Get(ctx, key, &claim); apierrors.IsNotFound(err) {
		claim = networkingv1alpha.NetworkInterfaceClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: claimName},
			Spec: networkingv1alpha.NetworkInterfaceClaimSpec{
				Network:        networkingv1alpha.LocalNetworkRef{Name: pod.Annotations[PodPrimaryNetworkAnnotation]},
				InterfaceName:  "eth0",
				AttachmentMode: networkingv1alpha.NetworkInterfaceAttachmentModeNetns,
			},
		}
		if err := controllerutil.SetControllerReference(&pod, &claim, r.Scheme()); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, &claim); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("create Pod network claim: %w", err)
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	if !metav1.IsControlledBy(&claim, &pod) || claim.Spec.Network.Name != pod.Annotations[PodPrimaryNetworkAnnotation] {
		return ctrl.Result{}, fmt.Errorf("network claim %s is not owned by Pod %s or requests a different network", claim.Name, pod.Name)
	}
	if !networkInterfaceClaimSatisfied(&claim) || claim.Status.NetworkInterfaceRef == nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	var networkInterface networkingv1alpha.NetworkInterface
	if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: claim.Status.NetworkInterfaceRef.Name}, &networkInterface); err != nil {
		if apierrors.IsNotFound(err) {
			// The claim status can arrive before the interface appears in this
			// controller's cache. There is no interface watch to wake this Pod.
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	// Cloud currently names the prepared NAD after the VPCAttachment. Keep
	// the readiness check tied to the provider-published attachment ref;
	// a future provider-neutral NAD ref should replace this name contract.
	ref := networkInterface.Status.AttachmentRef
	if ref == nil || ref.Name == "" || ref.Kind != "VPCAttachment" || ref.APIGroup != "cloud.datumapis.com" {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	before := pod.DeepCopy()
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	// Explicit namespace/name works even when the NAD and Multus namespace
	// differ. The named NAD is unique to this Pod's UID-backed claim.
	defaultNetwork := pod.Namespace + "/" + ref.Name
	if existing := pod.Annotations[MultusDefaultNetworkAnnotation]; existing != "" && existing != defaultNetwork {
		return ctrl.Result{}, fmt.Errorf("Pod %s already specifies a different Multus default network", pod.Name)
	}
	// Persist the annotation while the Pod is still gated, then remove the gate
	// in a separate API write so the scheduler cannot bind it between steps.
	if pod.Annotations[MultusDefaultNetworkAnnotation] != defaultNetwork {
		pod.Annotations[MultusDefaultNetworkAnnotation] = defaultNetwork
		if err := r.Patch(ctx, &pod, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, fmt.Errorf("set Pod default network: %w", err)
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	filtered := pod.Spec.SchedulingGates[:0]
	for _, gate := range pod.Spec.SchedulingGates {
		if gate.Name != PodNetworkSchedulingGate {
			filtered = append(filtered, gate)
		}
	}
	pod.Spec.SchedulingGates = filtered
	if err := r.Patch(ctx, &pod, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, fmt.Errorf("release Pod network scheduling gate: %w", err)
	}
	return ctrl.Result{}, nil
}

func podNetworkClaimName(uid types.UID) string { return "pod-" + string(uid) + "-eth0" }

func hasPodNetworkGate(pod *corev1.Pod) bool {
	for _, gate := range pod.Spec.SchedulingGates {
		if gate.Name == PodNetworkSchedulingGate {
			return true
		}
	}
	return false
}

func (r *PodNetworkAttachmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Client = mgr.GetClient()
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Watches(&networkingv1alpha.NetworkInterfaceClaim{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			for _, owner := range obj.GetOwnerReferences() {
				if owner.Kind == "Pod" && owner.APIVersion == "v1" {
					return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: owner.Name}}}
				}
			}
			return nil
		})).
		Complete(r)
}
