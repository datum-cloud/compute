// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrl "sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	resourcemanagerv1alpha1 "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
)

const (
	internalDNSNameConflict            = "NameConflict"
	internalDNSEligibleField           = "eligible"
	internalDNSContentField            = "content"
	internalDNSRecordTypeField         = "recordType"
	internalDNSRecordsField            = "records"
	internalDNSInstanceKind            = "Instance"
	internalDNSRecordTypeAAAA          = "AAAA"
	internalDNSNameField               = "name"
	internalDNSStatusField             = "status"
	internalDNSReasonField             = "reason"
	internalDNSUIDField                = "uid"
	internalDNSZoneRefField            = "dnsZoneRef"
	internalDNSConditionAccepted       = "Accepted"
	internalDNSGroup                   = "dns.networking.miloapis.com"
	internalDNSVersion                 = "v1alpha1"
	internalDNSInstanceUID             = "internal-dns.compute.datumapis.com/instance-uid"
	internalDNSInstanceNameHash        = "internal-dns.compute.datumapis.com/instance-name-hash"
	internalDNSVPCUID                  = "internal-dns.compute.datumapis.com/vpc-uid"
	internalDNSManagedBy               = "internal-dns.compute.datumapis.com/managed-by"
	internalDNSManager                 = "compute-instance-publisher"
	internalDNSResyncInterval          = 10 * time.Minute
	internalDNSAPITimeout              = 10 * time.Second
	defaultDNSConcurrency              = 4
	internalDNSConditionsField         = "conditions"
	internalDNSObservedGenerationField = "observedGeneration"
	internalDNSRegistrationRefField    = "registrationRef"
	internalDNSRecordTypesField        = "recordTypes"
	internalDNSConditionReady          = "Ready"
	internalDNSConditionPublished      = "Published"
	internalDNSConditionTypeField      = "type"
	internalDNSSpecField               = "spec"
	internalDNSRecordSetsField         = "recordSets"
)

var (
	dnsResolverContextGVK = schema.GroupVersionKind{Group: internalDNSGroup, Version: internalDNSVersion, Kind: "DNSResolverContext"}
	dnsRegistrationGVK    = schema.GroupVersionKind{Group: internalDNSGroup, Version: internalDNSVersion, Kind: "DNSRegistration"}
	dnsContributionGVK    = schema.GroupVersionKind{Group: internalDNSGroup, Version: internalDNSVersion, Kind: "DNSRecordContribution"}
)

// InternalDNSProjectIdentity identifies the project that owns DNS resources.
type InternalDNSProjectIdentity struct {
	ProjectName string
	ProjectUID  types.UID
}

// InternalDNSProjectAccess supplies the owning project API reader and writer.
type InternalDNSProjectAccess struct {
	Reader client.Reader
	Writer client.Client
}

type InternalDNSProjectAccessFunc func(
	ctx context.Context,
	projectID string,
	providerCluster cluster.Cluster,
) (InternalDNSProjectAccess, error)

type internalDNSClusterGetter interface {
	GetCluster(ctx context.Context, clusterName multicluster.ClusterName) (cluster.Cluster, error)
}

// InternalDNSPublisherReconciler publishes the private addresses of an Instance
// under one stable identity name per attached VPC. DNS failures only requeue
// this controller; they never alter Compute readiness.
type InternalDNSPublisherReconciler struct {
	mgr                         internalDNSClusterGetter
	ProjectIDForInstance        func(context.Context, multicluster.ClusterName, *computev1alpha.Instance) (string, error)
	ProjectNamespaceForInstance func(context.Context, multicluster.ClusterName, *computev1alpha.Instance) (string, error)
	ProjectAccess               InternalDNSProjectAccessFunc
	ResolveProjectIdentity      func(context.Context, string) (InternalDNSProjectIdentity, error)
	ProjectIdentities           map[string]InternalDNSProjectIdentity
	MaxConcurrentReconciles     int
	Now                         func() time.Time
	ObservationSources          map[string]InternalDNSObservationSource
	ObserveInterface            InternalDNSObserveFunc
	// LocalRegion, on a cell, publishes into the cell's own cluster and marks
	// every object with this region as its origin. The project authors the
	// shared registrations, so the cell only reads them.
	LocalRegion string
	// SkipLocations are locations whose cells publish locally. The management
	// publisher withdraws what it published there.
	SkipLocations map[string]bool
}

func (r *InternalDNSPublisherReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (out ctrl.Result, err error) {
	defer func(ctx context.Context) { out, err = settleRoutineErrors(ctx, r.controllerName(), out, err) }(ctx)
	ctx, cancel := context.WithTimeout(ctx, internalDNSAPITimeout)
	defer cancel()

	providerCluster, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var instance computev1alpha.Instance
	if err := providerCluster.GetAPIReader().Get(ctx, req.NamespacedName, &instance); err != nil {
		if instanceNotFound(err, req.Name) {
			return ctrl.Result{}, r.withdrawDeletedInstance(ctx, req, providerCluster)
		}
		return ctrl.Result{}, err
	}

	projectID, err := r.projectID(ctx, req.ClusterName, &instance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolve DNS project identity: %w", err)
	}
	projectNamespace, err := r.projectNamespace(ctx, req.ClusterName, &instance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolve DNS project namespace: %w", err)
	}
	identity, err := r.projectIdentity(ctx, projectID)
	if err != nil {
		return ctrl.Result{}, err
	}
	access, err := r.projectAccess(ctx, projectID, providerCluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	// DNS resources live in the project API and are owned by the project-side
	// Instance lifetime, even when this observation came from an edge copy with
	// a different Kubernetes UID. This makes normal deletion and replacement
	// cleanup independent of receiving a final reconcile event.
	var projectInstance computev1alpha.Instance
	if err := access.Reader.Get(ctx, client.ObjectKey{Namespace: projectNamespace, Name: instance.Name}, &projectInstance); err != nil {
		return ctrl.Result{}, fmt.Errorf("resolve project Instance owner for DNS publication: %w", err)
	}
	if projectInstance.UID == "" {
		return ctrl.Result{}, fmt.Errorf("project Instance %s/%s has no API-assigned UID", projectNamespace, instance.Name)
	}
	if projectInstance.UID != instance.UID {
		return ctrl.Result{}, fmt.Errorf("refusing DNS publication from Instance copy UID %q for current project Instance UID %q", instance.UID, projectInstance.UID)
	}
	// Withdraw an earlier Instance of the same name first, so a failure later
	// in this pass cannot leave the replacement answering with its
	// predecessor's address. The resync retries a failed sweep.
	if err := deletePredecessorDNSResources(ctx, access, projectNamespace, &projectInstance); err != nil {
		log.FromContext(ctx).Error(err, "withdrawing DNS records of an earlier instance with the same name", "instance", req.NamespacedName)
	}
	owner := metav1.OwnerReference{
		APIVersion: computev1alpha.GroupVersion.String(),
		Kind:       internalDNSInstanceKind,
		Name:       projectInstance.Name,
		UID:        projectInstance.UID,
		Controller: ptrBool(true),
	}

	if !instance.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.deletePublishedResources(ctx, access, projectNamespace, &projectInstance, nil)
	}
	if r.SkipLocations[instance.Labels[computev1alpha.LocationLabel]] {
		// Hand the instance to its cell only once the cell's own publication
		// has reached the project, so there is no gap.
		handed, err := cellPublished(ctx, access.Reader, projectNamespace, projectInstance.Labels[computev1alpha.InstanceSourceUIDLabel])
		if err != nil {
			return ctrl.Result{}, err
		}
		if handed {
			requeue := ctrl.Result{RequeueAfter: internalDNSPollInterval(internalDNSResyncInterval)}
			return requeue, r.deletePublishedResources(ctx, access, projectNamespace, &projectInstance, nil)
		}
	}

	vpcUID := vpcUIDFunc(projectNetworkUID)
	if r.LocalRegion != "" {
		vpcUID = cellNetworkUID(instance.Labels[computev1alpha.LocationLabel])
	}
	attachments, err := instanceDNSAttachmentsWith(ctx, access.Reader, projectNamespace, &projectInstance, r.observeInterface, vpcUID)
	if err != nil {
		return ctrl.Result{}, err
	}
	desired := make(map[string]struct{}, len(attachments))
	publicationStatus := make([]computev1alpha.InstanceDNSStatus, 0, len(attachments))
	var publicationErr error
	for _, attachment := range attachments {
		resourceName := internalDNSResourceName(projectInstance.UID, attachment.VPCUID)
		desired[resourceName] = struct{}{}
		ready, err := r.reconcileAttachment(ctx, access, identity, projectNamespace, &projectInstance, owner, attachment, resourceName)
		publication, statusErr := r.publicationStatus(ctx, access.Reader, identity, projectNamespace, &projectInstance, attachment, resourceName, ready, err)
		publicationStatus = append(publicationStatus, publication)
		if statusErr != nil {
			publicationErr = errors.Join(publicationErr, statusErr)
		}
		if err != nil {
			publicationErr = errors.Join(publicationErr, err)
		}
	}
	if err := r.reportPublication(ctx, access.Writer, access.Reader, &projectInstance, publicationStatus); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.deletePublishedResources(ctx, access, projectNamespace, &projectInstance, desired); err != nil {
		return ctrl.Result{}, err
	}
	// Every input arrives as an event: the Instance, the DNS objects it owns,
	// and the resolver contexts in its namespace. The resync only bounds how
	// long a missed event can leave publication stale.
	return ctrl.Result{RequeueAfter: internalDNSPollInterval(internalDNSResyncInterval)}, publicationErr
}

type instanceDNSAttachment struct {
	VPCUID           types.UID
	Network          networkingv1alpha.NetworkRef
	RecordSets       []any
	Eligible         bool
	ObservationError error
}

// vpcUIDFunc resolves the project UID of the network an interface attaches to.
type vpcUIDFunc func(ctx context.Context, reader client.Reader, namespace, network string) (types.UID, error)

// projectNetworkUID reads the network from the project API.
func projectNetworkUID(ctx context.Context, reader client.Reader, namespace, name string) (types.UID, error) {
	var network networkingv1alpha.Network
	if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &network); err != nil {
		return "", fmt.Errorf("resolve VPC %s/%s for instance DNS: %w", namespace, name, err)
	}
	if network.UID == "" {
		return "", fmt.Errorf("network %s/%s has no API-assigned UID", namespace, name)
	}
	return network.UID, nil
}

func instanceDNSAttachments(ctx context.Context, reader client.Reader, projectNamespace string, instance *computev1alpha.Instance, observe InternalDNSObserveFunc) ([]instanceDNSAttachment, error) {
	return instanceDNSAttachmentsWith(ctx, reader, projectNamespace, instance, observe, projectNetworkUID)
}

func instanceDNSAttachmentsWith(ctx context.Context, reader client.Reader, projectNamespace string, instance *computev1alpha.Instance, observe InternalDNSObserveFunc, vpcUID vpcUIDFunc) ([]instanceDNSAttachment, error) {
	byVPC := map[types.UID]*instanceDNSAttachment{}
	for _, nic := range instance.Spec.NetworkInterfaces {
		namespace := nic.Network.Namespace
		if namespace == "" {
			namespace = projectNamespace
		}
		uid, err := vpcUID(ctx, reader, namespace, nic.Network.Name)
		if err != nil {
			return nil, err
		}
		attachment := byVPC[uid]
		if attachment == nil {
			attachment = &instanceDNSAttachment{VPCUID: uid, Network: networkingv1alpha.NetworkRef{Name: nic.Network.Name, Namespace: namespace}, RecordSets: []any{}}
			byVPC[uid] = attachment
		}

		if observe == nil {
			attachment.ObservationError = fmt.Errorf("no current interface observation is available")
			continue
		}
		observation, err := observe(ctx, reader, projectNamespace, instance, nic)
		if err != nil {
			attachment.ObservationError = errors.Join(attachment.ObservationError, err)
			continue
		}
		if !observation.Eligible {
			continue
		}
		attachment.Eligible = true
		for _, address := range observation.Addresses {
			addr, ok := hostAddress(address.Address)
			if !ok {
				continue
			}
			rrtype, field := "A", "a"
			if addr.Is6() {
				rrtype, field = internalDNSRecordTypeAAAA, "aaaa"
			}
			attachment.RecordSets = append(attachment.RecordSets, map[string]any{
				internalDNSRecordTypeField: rrtype,
				internalDNSRecordsField:    []any{map[string]any{internalDNSNameField: "", "ttl": int64(30), field: map[string]any{internalDNSContentField: addr.String()}}},
			})
		}
	}

	attachments := make([]instanceDNSAttachment, 0, len(byVPC))
	for _, attachment := range byVPC {
		attachment.Eligible = attachment.Eligible && len(attachment.RecordSets) > 0
		sort.Slice(attachment.RecordSets, func(i, j int) bool {
			return fmt.Sprint(attachment.RecordSets[i]) < fmt.Sprint(attachment.RecordSets[j])
		})
		attachments = append(attachments, *attachment)
	}
	sort.Slice(attachments, func(i, j int) bool { return attachments[i].VPCUID < attachments[j].VPCUID })
	return attachments, nil
}

func internalDNSInterfaceName(nic computev1alpha.InstanceNetworkInterface) string {
	if nic.Name != "" {
		return nic.Name
	}
	return defaultInterfaceName
}

func hostAddress(raw string) (netip.Addr, bool) {
	if addr, err := netip.ParseAddr(raw); err == nil {
		return addr.Unmap(), true
	}
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || prefix.Bits() != prefix.Addr().BitLen() {
		return netip.Addr{}, false
	}
	return prefix.Addr().Unmap(), true
}

func (r *InternalDNSPublisherReconciler) reconcileAttachment(ctx context.Context, access InternalDNSProjectAccess, identity InternalDNSProjectIdentity, namespace string, instance *computev1alpha.Instance, owner metav1.OwnerReference, attachment instanceDNSAttachment, resourceName string) (bool, error) {
	if errors.Is(attachment.ObservationError, errNetworkObservationUnconfigured) {
		return false, nil
	}
	if attachment.ObservationError != nil {
		return false, attachment.ObservationError
	}
	managed, err := findResolverContext(ctx, access.Reader, namespace, identity.ProjectUID, attachment.VPCUID)
	if err != nil {
		return false, err
	}
	if managed == nil {
		return false, nil
	}

	labels := map[string]string{
		internalDNSInstanceUID:      string(instance.UID),
		internalDNSInstanceNameHash: shortHash(instance.Name),
		internalDNSVPCUID:           string(attachment.VPCUID),
		internalDNSManagedBy:        internalDNSManager,
	}
	if r.LocalRegion != "" {
		labels[internalDNSOriginLabel] = r.LocalRegion
	}
	ownerName := allocatedInstanceDNSName(instance)
	for i := range attachment.RecordSets {
		rrset := attachment.RecordSets[i].(map[string]any)
		records := rrset[internalDNSRecordsField].([]any)
		for _, record := range records {
			record.(map[string]any)[internalDNSNameField] = ownerName
		}
	}

	var registration *unstructured.Unstructured
	if r.LocalRegion != "" {
		if registration, err = lookupInstanceDNSReservation(ctx, access, managed); err != nil || registration == nil {
			return false, err
		}
	} else {
		var changed bool
		registration, changed, err = ensureInstanceDNSReservation(ctx, access, managed)
		if err != nil || changed {
			return false, err
		}
	}
	registrationRef := objectReference(registration)

	contributionSpec := map[string]any{
		internalDNSRegistrationRefField: registrationRef,
		internalDNSRecordSetsField:      attachment.RecordSets,
		internalDNSEligibleField:        attachment.Eligible,
		internalDNSReasonField:          map[bool]string{true: "AddressAllocatedAndProgrammed", false: "AddressUnavailable"}[attachment.Eligible],
	}
	_, changed, err := ensureDNSObject(ctx, access, dnsContributionGVK, namespace, resourceName, labels, owner, contributionSpec)
	return err == nil && !changed, err
}

func findResolverContext(ctx context.Context, reader client.Reader, namespace string, projectUID, vpcUID types.UID) (*unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(dnsResolverContextGVK.GroupVersion().WithKind(dnsResolverContextGVK.Kind + "List"))
	if err := reader.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list DNS resolver contexts: %w", err)
	}
	var match *unstructured.Unstructured
	for i := range list.Items {
		item := &list.Items[i]
		if !item.GetDeletionTimestamp().IsZero() || item.GetUID() == "" || item.GetGeneration() < 1 {
			continue
		}
		consumerID, _, _ := unstructured.NestedString(item.Object, internalDNSSpecField, "consumerID")
		if consumerID != string(projectUID)+"/"+string(vpcUID) || !unstructuredConditionCurrent(item, internalDNSConditionReady) {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("multiple ready DNS resolver contexts match project UID %q and VPC UID %q", projectUID, vpcUID)
		}
		match = item.DeepCopy()
	}
	return match, nil
}

func allocatedDNSNamespace(resolver *unstructured.Unstructured) (string, map[string]any, error) {
	suffix, _, _ := unstructured.NestedString(resolver.Object, internalDNSStatusField, "managedNamespace", "suffix")
	suffix = strings.TrimSuffix(strings.ToLower(suffix), ".")
	zoneRef, _, _ := unstructured.NestedMap(resolver.Object, internalDNSStatusField, "managedNamespace", internalDNSZoneRefField)
	if len(validation.IsDNS1123Subdomain(suffix)) > 0 || referenceName(zoneRef) == "" {
		return "", nil, fmt.Errorf("DNS context %q has no valid allocated namespace", resolver.GetName())
	}
	return suffix, map[string]any{internalDNSNameField: referenceName(zoneRef)}, nil
}

func unstructuredConditionCurrent(obj *unstructured.Unstructured, conditionType string) bool {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, internalDNSStatusField, internalDNSConditionsField)
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if ok && condition[internalDNSConditionTypeField] == conditionType && condition[internalDNSStatusField] == string(metav1.ConditionTrue) && condition[internalDNSObservedGenerationField] == obj.GetGeneration() {
			return true
		}
	}
	return false
}

func ensureDNSObject(ctx context.Context, access InternalDNSProjectAccess, gvk schema.GroupVersionKind, namespace, name string, labels map[string]string, owner metav1.OwnerReference, spec map[string]any) (*unstructured.Unstructured, bool, error) {
	desired := newDNSObject(gvk, namespace, name)
	desired.SetLabels(labels)
	desired.SetOwnerReferences([]metav1.OwnerReference{owner})
	desired.Object[internalDNSSpecField] = spec
	current := newDNSObject(gvk, namespace, name)
	err := access.Reader.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		if err := access.Writer.Create(ctx, desired); err != nil {
			return nil, false, fmt.Errorf("create %s %s/%s: %w", gvk.Kind, namespace, name, err)
		}
		return desired, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !current.GetDeletionTimestamp().IsZero() {
		return nil, false, fmt.Errorf("%s %s/%s is terminating", gvk.Kind, namespace, name)
	}
	if current.GetLabels()[internalDNSManagedBy] != internalDNSManager || current.GetLabels()[internalDNSInstanceUID] != labels[internalDNSInstanceUID] || !ownedBy(current, owner) {
		return nil, false, fmt.Errorf("refusing to adopt foreign %s %s/%s", gvk.Kind, namespace, name)
	}
	currentSpec, _, _ := unstructured.NestedMap(current.Object, internalDNSSpecField)
	if authorityReferencesChanged(gvk, currentSpec, spec) {
		if err := access.Writer.Delete(ctx, current); client.IgnoreNotFound(err) != nil {
			return nil, false, err
		}
		return current, true, nil
	}
	if !reflect.DeepEqual(currentSpec, spec) || !reflect.DeepEqual(current.GetLabels(), labels) || !reflect.DeepEqual(current.GetOwnerReferences(), []metav1.OwnerReference{owner}) {
		current.Object[internalDNSSpecField] = spec
		current.SetLabels(labels)
		current.SetOwnerReferences([]metav1.OwnerReference{owner})
		if err := access.Writer.Update(ctx, current); err != nil {
			return nil, false, fmt.Errorf("update %s %s/%s: %w", gvk.Kind, namespace, name, err)
		}
		return current, true, nil
	}
	return current, false, nil
}

func ownedBy(obj metav1.Object, owner metav1.OwnerReference) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner.UID && ref.APIVersion == owner.APIVersion && ref.Kind == owner.Kind && ref.Name == owner.Name {
			return true
		}
	}
	return false
}

func ptrBool(value bool) *bool { return &value }

// authorityReferencesChanged reports a change to a field the DNS API treats as
// immutable, which can only be applied by recreating the object.
func authorityReferencesChanged(gvk schema.GroupVersionKind, current, desired map[string]any) bool {
	switch gvk {
	case dnsContributionGVK:
		return !reflect.DeepEqual(current[internalDNSRegistrationRefField], desired[internalDNSRegistrationRefField])
	case dnsRegistrationGVK:
		return !reflect.DeepEqual(current[internalDNSNameField], desired[internalDNSNameField]) ||
			!reflect.DeepEqual(current[internalDNSZoneRefField], desired[internalDNSZoneRefField])
	}
	return false
}

// deletePublishedResources removes the instance's resources that are not in
// keep, and every resource left by an earlier Instance of the same name.
func (r *InternalDNSPublisherReconciler) deletePublishedResources(ctx context.Context, access InternalDNSProjectAccess, namespace string, instance *computev1alpha.Instance, keep map[string]struct{}) error {
	if err := deleteInstanceDNSResources(ctx, access, namespace, client.MatchingLabels{internalDNSInstanceUID: string(instance.UID), internalDNSManagedBy: internalDNSManager}, func(item *unstructured.Unstructured) bool {
		_, retained := keep[item.GetName()]
		return !retained || item.GetKind() == dnsRegistrationGVK.Kind
	}); err != nil {
		return err
	}
	return deletePredecessorDNSResources(ctx, access, namespace, instance)
}

// deletePredecessorDNSResources removes resources left by an earlier Instance
// with this Instance's name. A recreated Instance must not serve its
// predecessor's addresses while those resources wait for garbage collection.
// Resources written before the name label existed still wait for it.
func deletePredecessorDNSResources(ctx context.Context, access InternalDNSProjectAccess, namespace string, instance *computev1alpha.Instance) error {
	return deleteInstanceDNSResources(ctx, access, namespace, client.MatchingLabels{internalDNSInstanceNameHash: shortHash(instance.Name), internalDNSManagedBy: internalDNSManager}, func(item *unstructured.Unstructured) bool {
		return item.GetLabels()[internalDNSInstanceUID] != string(instance.UID) && ownedByInstanceNamed(item, instance.Name)
	})
}

// withdrawDeletedInstance removes the resources of an Instance that no longer
// exists, so a deleted or renamed Instance stops answering without waiting for
// garbage collection. It only runs where the Instance is read from the project
// API itself; otherwise garbage collection of the owned resources does this.
func (r *InternalDNSPublisherReconciler) withdrawDeletedInstance(ctx context.Context, req mcreconcile.Request, providerCluster cluster.Cluster) error {
	if r.ProjectIDForInstance != nil || r.ProjectNamespaceForInstance != nil {
		return nil
	}
	access, err := r.projectAccess(ctx, string(req.ClusterName), providerCluster)
	if err != nil {
		return err
	}
	// Keep the resources of an Instance created under this name since the read
	// above, in case another writer is reconciling it.
	var current computev1alpha.Instance
	if err := access.Reader.Get(ctx, req.NamespacedName, &current); err == nil {
		return deletePredecessorDNSResources(ctx, access, req.Namespace, &current)
	} else if !instanceNotFound(err, req.Name) {
		return err
	}
	return deleteInstanceDNSResources(ctx, access, req.Namespace, client.MatchingLabels{internalDNSInstanceNameHash: shortHash(req.Name), internalDNSManagedBy: internalDNSManager}, func(item *unstructured.Unstructured) bool {
		return ownedByInstanceNamed(item, req.Name)
	})
}

// instanceNotFound reports whether err says the named Instance does not exist,
// as opposed to the API not serving Instances at all, which also returns 404.
func instanceNotFound(err error, name string) bool {
	var status apierrors.APIStatus
	if !apierrors.IsNotFound(err) || !errors.As(err, &status) {
		return false
	}
	details := status.Status().Details
	return details != nil && details.Kind == "instances" && details.Name == name
}

func deleteInstanceDNSResources(ctx context.Context, access InternalDNSProjectAccess, namespace string, selector client.MatchingLabels, shouldDelete func(*unstructured.Unstructured) bool) error {
	for _, gvk := range []schema.GroupVersionKind{dnsContributionGVK, dnsRegistrationGVK} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := access.Reader.List(ctx, list, client.InNamespace(namespace), selector); err != nil {
			return fmt.Errorf("list stale %s resources: %w", gvk.Kind, err)
		}
		for i := range list.Items {
			item := &list.Items[i]
			item.SetGroupVersionKind(gvk)
			if !shouldDelete(item) {
				continue
			}
			if err := access.Writer.Delete(ctx, item, client.Preconditions{UID: ptrUID(item.GetUID())}); client.IgnoreNotFound(err) != nil && !apierrors.IsConflict(err) {
				return fmt.Errorf("delete stale %s %s/%s: %w", gvk.Kind, namespace, item.GetName(), err)
			}
		}
	}
	return nil
}

// ownedByInstanceNamed reports whether an Instance named name controls obj.
func ownedByInstanceNamed(obj metav1.Object, name string) bool {
	ref := metav1.GetControllerOf(obj)
	return ref != nil && ref.APIVersion == computev1alpha.GroupVersion.String() && ref.Kind == internalDNSInstanceKind && ref.Name == name
}

func ptrUID(uid types.UID) *types.UID { return &uid }

func newDNSObject(gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetNamespace(namespace)
	obj.SetName(name)
	return obj
}

func objectReference(obj *unstructured.Unstructured) map[string]any {
	ref := map[string]any{internalDNSNameField: obj.GetName(), internalDNSUIDField: string(obj.GetUID())}
	ref["generation"] = obj.GetGeneration()
	return ref
}

func referenceName(values map[string]any) string {
	value, _ := values[internalDNSNameField].(string)
	return value
}

// instanceDNSScope is the label under which compute publishes instance names in
// a zone, so an instance named web-01 is web-01.instances.<zone>.
const instanceDNSScope = "instances"

// allocatedInstanceDNSName is the instance's owner name relative to its zone.
func allocatedInstanceDNSName(instance *computev1alpha.Instance) string {
	return instanceDNSLabel(instance) + "." + instanceDNSScope
}

func instanceDNSLabel(instance *computev1alpha.Instance) string {
	base := strings.ToLower(instance.Name)
	var b strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	base = strings.Trim(b.String(), "-")
	if base == "" {
		base = "instance"
	}
	if len(base) > 63 {
		return strings.TrimRight(base[:46], "-") + "-" + shortHash(instance.Name)
	}
	return base
}

func internalDNSResourceName(instanceUID, vpcUID types.UID) string {
	return "instance-" + shortHash(string(instanceUID)+"/"+string(vpcUID))
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func (r *InternalDNSPublisherReconciler) projectID(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) (string, error) {
	if r.ProjectIDForInstance != nil {
		return r.ProjectIDForInstance(ctx, clusterName, instance)
	}
	return string(clusterName), nil
}

func (r *InternalDNSPublisherReconciler) projectNamespace(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) (string, error) {
	if r.ProjectNamespaceForInstance != nil {
		return r.ProjectNamespaceForInstance(ctx, clusterName, instance)
	}
	return instance.Namespace, nil
}

func (r *InternalDNSPublisherReconciler) projectAccess(ctx context.Context, projectID string, providerCluster cluster.Cluster) (InternalDNSProjectAccess, error) {
	if r.ProjectAccess != nil {
		return r.ProjectAccess(ctx, projectID, providerCluster)
	}
	return InternalDNSProjectAccess{Reader: providerCluster.GetAPIReader(), Writer: providerCluster.GetClient()}, nil
}

// InternalDNSProjectIdentityResolver reads the current project identity from Milo.
// Unavailable or terminating projects stop publication without withdrawing records.
func InternalDNSProjectIdentityResolver(reader client.Reader) func(context.Context, string) (InternalDNSProjectIdentity, error) {
	return func(ctx context.Context, projectName string) (InternalDNSProjectIdentity, error) {
		var project resourcemanagerv1alpha1.Project
		if err := reader.Get(ctx, client.ObjectKey{Name: projectName}, &project); err != nil {
			return InternalDNSProjectIdentity{}, fmt.Errorf("read DNS project %q: %w", projectName, err)
		}
		if project.UID == "" || !project.DeletionTimestamp.IsZero() {
			return InternalDNSProjectIdentity{}, fmt.Errorf("DNS project %q has no active identity", projectName)
		}
		return InternalDNSProjectIdentity{ProjectName: project.Name, ProjectUID: project.UID}, nil
	}
}

func (r *InternalDNSPublisherReconciler) projectIdentity(ctx context.Context, projectName string) (InternalDNSProjectIdentity, error) {
	if r.ResolveProjectIdentity != nil {
		return r.ResolveProjectIdentity(ctx, projectName)
	}
	identity, ok := r.ProjectIdentities[projectName]
	if !ok || identity.ProjectUID == "" {
		return InternalDNSProjectIdentity{}, fmt.Errorf("internal DNS identity is not configured for project %q", projectName)
	}
	return identity, nil
}

func (r *InternalDNSPublisherReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	if r.ResolveProjectIdentity == nil && len(r.ProjectIdentities) == 0 {
		return errors.New("an internal DNS project identity resolver is required")
	}
	r.mgr = mgr
	if r.LocalRegion == "" {
		if err := (&internalDNSReservationReconciler{publisher: r}).SetupWithManager(mgr); err != nil {
			return err
		}
	}
	concurrency := r.MaxConcurrentReconciles
	if concurrency <= 0 {
		concurrency = defaultDNSConcurrency
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named(r.controllerName()).
		For(&computev1alpha.Instance{}, mcbuilder.WithEngageWithLocalCluster(false)).
		Owns(newDNSObject(dnsRegistrationGVK, "", ""), mcbuilder.WithEngageWithLocalCluster(false)).
		Owns(newDNSObject(dnsContributionGVK, "", ""), mcbuilder.WithEngageWithLocalCluster(false)).
		Watches(newDNSObject(dnsResolverContextGVK, "", ""), func(clusterName multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
			return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []mcreconcile.Request {
				return enqueueInstancesInNamespace(ctx, cl.GetClient(), string(clusterName), obj.GetNamespace())
			})
		}, mcbuilder.WithEngageWithLocalCluster(false)).
		// A cell's registration copy has no owner, so its arrival is watched
		// by namespace.
		Watches(newDNSObject(dnsRegistrationGVK, "", ""), func(clusterName multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
			return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []mcreconcile.Request {
				if r.LocalRegion == "" {
					return nil
				}
				return enqueueInstancesInNamespace(ctx, cl.GetClient(), string(clusterName), obj.GetNamespace())
			})
		}, mcbuilder.WithEngageWithLocalCluster(false)).
		WithOptions(mccontroller.Options{MaxConcurrentReconciles: concurrency}).
		Complete(r)
}

// internalDNSPollInterval spreads resyncs so they do not arrive together.
func internalDNSPollInterval(base time.Duration) time.Duration {
	return base + time.Duration(rand.Int64N(int64(base/2)))
}

func (r *InternalDNSPublisherReconciler) controllerName() string {
	if r.LocalRegion != "" {
		return "internal-dns-local-instance-publisher"
	}
	return "internal-dns-instance-publisher"
}
