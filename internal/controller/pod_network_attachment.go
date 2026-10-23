// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	PodPrimaryNetworkEnabledLabel    = "compute.datumapis.com/primary-network-enabled"
	PodPrimaryNetworkEnabledValue    = "true"
	PodPrimaryNetworkAnnotation      = "compute.datumapis.com/primary-network"
	PodNetworkInterfacesEnabledLabel = "compute.datumapis.com/network-interfaces-enabled"
	PodNetworkInterfacesAnnotation   = "compute.datumapis.com/network-interfaces"
	PodNetworkSchedulingGate         = "compute.datumapis.com/network-prepared"
	MultusDefaultNetworkAnnotation   = "v1.multus-cni.io/default-network"
	MultusNetworksAnnotation         = "k8s.v1.cni.cncf.io/networks"

	podInterfaceAddressAnnotationPrefix = "compute.datumapis.com/address-"
	maxPodNetworkInterfaces             = 4
	vpcAttachmentKind                   = "VPCAttachment"
)

// PodNetworkInterface is one entry of the network interfaces annotation.
type PodNetworkInterface struct {
	// Network names the network in the Pod's namespace.
	Network string `json:"network"`
	// Interface is the device name inside the Pod. eth0 replaces the Pod's
	// primary network; any other name is added beside it.
	Interface string `json:"interface,omitempty"`
	// ClaimName fixes the claim's name. A replacement Pod naming the same claim
	// binds the same interface, so a Retain claim keeps its addresses.
	ClaimName string `json:"claimName,omitempty"`
	// ReclaimPolicy is copied to the claim. Retain needs a stable claim name.
	ReclaimPolicy networkingv1alpha.NetworkInterfaceReclaimPolicy `json:"reclaimPolicy,omitempty"`
}

func (i PodNetworkInterface) Primary() bool { return i.Interface == defaultInterfaceName }

// PodNetworkInterfaces returns the interfaces an opted-in Pod asks for, with
// defaults applied, or nil when the Pod has not opted in.
func PodNetworkInterfaces(pod *corev1.Pod) ([]PodNetworkInterface, error) {
	legacy := pod.Labels[PodPrimaryNetworkEnabledLabel] == PodPrimaryNetworkEnabledValue
	listed := pod.Labels[PodNetworkInterfacesEnabledLabel] == PodPrimaryNetworkEnabledValue
	switch {
	case legacy && listed:
		return nil, errors.New("pod cannot use both the primary network and network interfaces opt-in labels")
	case legacy:
		network := pod.Annotations[PodPrimaryNetworkAnnotation]
		if network == "" {
			return nil, errors.New("primary VPC network label requires a network name annotation")
		}
		return []PodNetworkInterface{{
			Network:       network,
			Interface:     defaultInterfaceName,
			ReclaimPolicy: networkingv1alpha.NetworkInterfaceReclaimPolicyDelete,
		}}, nil
	case !listed:
		return nil, nil
	}

	raw := pod.Annotations[PodNetworkInterfacesAnnotation]
	if raw == "" {
		return nil, fmt.Errorf("network interfaces label requires the %s annotation", PodNetworkInterfacesAnnotation)
	}
	var interfaces []PodNetworkInterface
	if err := json.Unmarshal([]byte(raw), &interfaces); err != nil {
		return nil, fmt.Errorf("parse %s: %w", PodNetworkInterfacesAnnotation, err)
	}
	if len(interfaces) == 0 || len(interfaces) > maxPodNetworkInterfaces {
		return nil, fmt.Errorf("%s must list between 1 and %d interfaces", PodNetworkInterfacesAnnotation, maxPodNetworkInterfaces)
	}
	interfaceNames := map[string]bool{}
	claimNames := map[string]bool{}
	for i := range interfaces {
		entry := &interfaces[i]
		if err := defaultAndValidatePodNetworkInterface(pod, entry); err != nil {
			return nil, err
		}
		if interfaceNames[entry.Interface] {
			return nil, fmt.Errorf("interface %s is listed more than once", entry.Interface)
		}
		interfaceNames[entry.Interface] = true
		if entry.ClaimName != "" {
			if claimNames[entry.ClaimName] {
				return nil, fmt.Errorf("claim %s is listed more than once", entry.ClaimName)
			}
			claimNames[entry.ClaimName] = true
		}
	}
	return interfaces, nil
}

func defaultAndValidatePodNetworkInterface(pod *corev1.Pod, entry *PodNetworkInterface) error {
	if entry.Interface == "" {
		entry.Interface = defaultInterfaceName
	}
	if entry.ReclaimPolicy == "" {
		entry.ReclaimPolicy = networkingv1alpha.NetworkInterfaceReclaimPolicyDelete
	}
	if entry.Network == "" {
		return fmt.Errorf("interface %s does not name a network", entry.Interface)
	}
	if len(entry.Interface) > 15 || len(validation.IsDNS1123Label(entry.Interface)) > 0 {
		return fmt.Errorf("interface name %q is not a valid device name", entry.Interface)
	}
	switch entry.ReclaimPolicy {
	case networkingv1alpha.NetworkInterfaceReclaimPolicyDelete, networkingv1alpha.NetworkInterfaceReclaimPolicyRetain:
	default:
		return fmt.Errorf("interface %s has unknown reclaim policy %q", entry.Interface, entry.ReclaimPolicy)
	}
	if entry.ClaimName != "" {
		if errs := validation.IsDNS1123Subdomain(entry.ClaimName); len(errs) > 0 {
			return fmt.Errorf("interface %s claim name %q is invalid: %s", entry.Interface, entry.ClaimName, strings.Join(errs, "; "))
		}
	}
	if entry.ReclaimPolicy == networkingv1alpha.NetworkInterfaceReclaimPolicyRetain && entry.ClaimName == "" && !ownedByStatefulSet(pod) {
		// A UID-named claim is never asked for again, so its retained address
		// would stay reserved with nothing to reclaim it.
		return fmt.Errorf("interface %s uses Retain and needs a claimName unless a StatefulSet owns the Pod", entry.Interface)
	}
	return nil
}

func ownedByStatefulSet(pod *corev1.Pod) bool {
	owner := metav1.GetControllerOf(pod)
	return owner != nil && owner.Kind == "StatefulSet" && owner.APIVersion == appsv1.SchemeGroupVersion.String()
}

// podNetworkInterfaceClaimName names the claim after the slot the Pod fills
// when it has one, so a replacement Pod finds the same claim name.
func podNetworkInterfaceClaimName(pod *corev1.Pod, entry PodNetworkInterface) string {
	switch {
	case entry.ClaimName != "":
		return entry.ClaimName
	case ownedByStatefulSet(pod):
		return networkInterfaceClaimName(pod.Name, entry.Interface)
	default:
		return "pod-" + string(pod.UID) + "-" + entry.Interface
	}
}

// PodInterfaceAddressAnnotation is where compute publishes an interface's
// primary address, without its prefix length, for the downward API.
func PodInterfaceAddressAnnotation(interfaceName string) string {
	return podInterfaceAddressAnnotationPrefix + interfaceName
}

// PodNetworkAttachmentReconciler attaches opted-in native Pods, including
// DaemonSet Pods, to networks before the scheduler is allowed to bind them.
// It runs only against the cell cluster, not project control planes.
type PodNetworkAttachmentReconciler struct {
	client.Client
}

type preparedPodInterface struct {
	entry      PodNetworkInterface
	definition string
	address    string
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=networkinterfaceclaims,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=networking.datumapis.com,resources=networkinterfaces,verbs=get

func (r *PodNetworkAttachmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !pod.DeletionTimestamp.IsZero() || !hasPodNetworkGate(&pod) || pod.UID == "" {
		// Never change a running Pod's network annotations. Admission must
		// have installed the gate before this controller can act.
		return ctrl.Result{}, nil
	}
	entries, err := PodNetworkInterfaces(&pod)
	if err != nil {
		log.FromContext(ctx).Error(err, "Pod network interfaces are invalid; leaving the Pod gated")
		return ctrl.Result{}, nil
	}
	if len(entries) == 0 {
		return ctrl.Result{}, nil
	}

	prepared := make([]preparedPodInterface, 0, len(entries))
	for _, entry := range entries {
		ready, result, err := r.prepareInterface(ctx, &pod, entry)
		if err != nil || ready == nil {
			return result, err
		}
		prepared = append(prepared, *ready)
	}

	before := pod.DeepCopy()
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	var secondary []string
	changed := false
	for _, ready := range prepared {
		if ready.entry.Primary() {
			if existing := pod.Annotations[MultusDefaultNetworkAnnotation]; existing != "" && existing != ready.definition {
				return ctrl.Result{}, fmt.Errorf("pod %s already specifies a different Multus default network", pod.Name)
			}
			changed = setAnnotation(&pod, MultusDefaultNetworkAnnotation, ready.definition) || changed
		} else {
			secondary = append(secondary, ready.definition+"@"+ready.entry.Interface)
		}
		if ready.address != "" {
			changed = setAnnotation(&pod, PodInterfaceAddressAnnotation(ready.entry.Interface), ready.address) || changed
		}
	}
	if len(secondary) > 0 {
		networks := strings.Join(secondary, ",")
		if existing := pod.Annotations[MultusNetworksAnnotation]; existing != "" && existing != networks {
			return ctrl.Result{}, fmt.Errorf("pod %s already specifies different Multus networks", pod.Name)
		}
		changed = setAnnotation(&pod, MultusNetworksAnnotation, networks) || changed
	}
	// Persist the annotations while the Pod is still gated, then remove the
	// gate in a separate API write so the scheduler cannot bind it between steps.
	if changed {
		if err := r.Patch(ctx, &pod, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, fmt.Errorf("set Pod networks: %w", err)
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

// prepareInterface returns the interface once its claim is prepared, or a
// result to wait with while it is not.
func (r *PodNetworkAttachmentReconciler) prepareInterface(ctx context.Context, pod *corev1.Pod, entry PodNetworkInterface) (*preparedPodInterface, ctrl.Result, error) {
	wait := ctrl.Result{RequeueAfter: 5 * time.Second}
	claimName := podNetworkInterfaceClaimName(pod, entry)
	var claim networkingv1alpha.NetworkInterfaceClaim
	key := types.NamespacedName{Namespace: pod.Namespace, Name: claimName}
	if err := r.Get(ctx, key, &claim); apierrors.IsNotFound(err) {
		claim = networkingv1alpha.NetworkInterfaceClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: claimName, Labels: podClaimLabels(pod)},
			Spec: networkingv1alpha.NetworkInterfaceClaimSpec{
				Network:        networkingv1alpha.LocalNetworkRef{Name: entry.Network},
				InterfaceName:  entry.Interface,
				AttachmentMode: networkingv1alpha.NetworkInterfaceAttachmentModeNetns,
				ReclaimPolicy:  entry.ReclaimPolicy,
			},
		}
		if err := controllerutil.SetControllerReference(pod, &claim, r.Scheme()); err != nil {
			return nil, ctrl.Result{}, err
		}
		if err := r.Create(ctx, &claim); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, ctrl.Result{}, fmt.Errorf("create Pod network claim: %w", err)
		}
		return nil, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	} else if err != nil {
		return nil, ctrl.Result{}, err
	}

	if !claim.DeletionTimestamp.IsZero() {
		// The previous Pod's claim is still going away. Its name is the lock
		// that keeps two Pods from holding one interface.
		return nil, wait, nil
	}
	if !podClaimMatches(&claim, entry) {
		return nil, ctrl.Result{}, fmt.Errorf("network claim %s requests a different network, interface, or reclaim policy than Pod %s", claim.Name, pod.Name)
	}
	if !metav1.IsControlledBy(&claim, pod) {
		if metav1.GetControllerOf(&claim) != nil {
			log.FromContext(ctx).Info("waiting for another holder to release the network claim", "claim", claim.Name)
			return nil, wait, nil
		}
		if err := controllerutil.SetControllerReference(pod, &claim, r.Scheme()); err != nil {
			return nil, ctrl.Result{}, err
		}
		if err := r.Update(ctx, &claim); err != nil {
			return nil, ctrl.Result{}, fmt.Errorf("adopt network claim %s: %w", claim.Name, err)
		}
		return nil, ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if !networkInterfaceClaimSatisfied(&claim) || claim.Status.NetworkInterfaceRef == nil {
		return nil, wait, nil
	}

	var networkInterface networkingv1alpha.NetworkInterface
	if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: claim.Status.NetworkInterfaceRef.Name}, &networkInterface); err != nil {
		if apierrors.IsNotFound(err) {
			// The claim status can arrive before the interface appears in this
			// controller's cache. There is no interface watch to wake this Pod.
			return nil, wait, nil
		}
		return nil, ctrl.Result{}, err
	}
	// The provider names the prepared NAD after the VPCAttachment. Keep the
	// readiness check tied to the provider-published attachment ref; a future
	// provider-neutral NAD ref should replace this name contract.
	ref := networkInterface.Status.AttachmentRef
	if ref == nil || ref.Name == "" || ref.Kind != vpcAttachmentKind ||
		(ref.APIGroup != "cloud.datumapis.com" && ref.APIGroup != "galactic.datumapis.com") {
		return nil, wait, nil
	}
	// The named NAD is unique to this claim, and namespace/name works even
	// when the NAD and Multus namespaces differ.
	return &preparedPodInterface{
		entry:      entry,
		definition: pod.Namespace + "/" + ref.Name,
		address:    primaryClaimAddress(&claim),
	}, ctrl.Result{}, nil
}

func podClaimMatches(claim *networkingv1alpha.NetworkInterfaceClaim, entry PodNetworkInterface) bool {
	interfaceName := claim.Spec.InterfaceName
	if interfaceName == "" {
		interfaceName = defaultInterfaceName
	}
	policy := claim.Spec.ReclaimPolicy
	if policy == "" {
		policy = networkingv1alpha.NetworkInterfaceReclaimPolicyDelete
	}
	return claim.Spec.Network.Name == entry.Network && interfaceName == entry.Interface && policy == entry.ReclaimPolicy
}

// podClaimLabels copies the Pod's compute labels, which NSO carries on to the
// bound interface where network services select their members.
func podClaimLabels(pod *corev1.Pod) map[string]string {
	labels := map[string]string{}
	for key, value := range pod.Labels {
		if !strings.HasPrefix(key, "compute.datumapis.com/") ||
			key == PodPrimaryNetworkEnabledLabel || key == PodNetworkInterfacesEnabledLabel {
			continue
		}
		labels[key] = value
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

// primaryClaimAddress is the first address the claim holds, without its prefix
// length. For a delegated IPv6 block that is the block's first address.
func primaryClaimAddress(claim *networkingv1alpha.NetworkInterfaceClaim) string {
	if len(claim.Status.Addresses) == 0 {
		return ""
	}
	address := claim.Status.Addresses[0].Address
	if prefix, err := netip.ParsePrefix(address); err == nil {
		return prefix.Addr().String()
	}
	return address
}

func setAnnotation(pod *corev1.Pod, key, value string) bool {
	if pod.Annotations[key] == value {
		return false
	}
	pod.Annotations[key] = value
	return true
}

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
