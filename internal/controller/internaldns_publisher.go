// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	ctrl "sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	internalDNSInstanceKind   = "Instance"
	internalDNSRecordTypeAAAA = "AAAA"
	internalDNSNameField      = "name"
	internalDNSStatusField    = "status"
	internalDNSReasonField    = "reason"
	internalDNSUIDField       = "uid"
	internalDNSGroup          = "dns.networking.miloapis.com"
	internalDNSVersion        = "v1alpha1"
	internalDNSInstanceUID    = "internal-dns.compute.datumapis.com/instance-uid"
	internalDNSVPCUID         = "internal-dns.compute.datumapis.com/vpc-uid"
	internalDNSManagedBy      = "internal-dns.compute.datumapis.com/managed-by"
	internalDNSManager        = "compute-instance-publisher"
	defaultDNSLease           = 60 * time.Second
	maxDNSLease               = 90 * time.Second
	internalDNSPendingWait    = 10 * time.Second
	internalDNSAPITimeout     = 10 * time.Second
	defaultDNSConcurrency     = 4
	defaultInternalDNSDomain  = "datum.internal"
)

var (
	dnsResolverContextGVK = schema.GroupVersionKind{Group: internalDNSGroup, Version: internalDNSVersion, Kind: "DNSResolverContext"}
	dnsRegistrationGVK    = schema.GroupVersionKind{Group: internalDNSGroup, Version: internalDNSVersion, Kind: "DNSRegistration"}
	dnsGrantGVK           = schema.GroupVersionKind{Group: internalDNSGroup, Version: internalDNSVersion, Kind: "DNSContributionGrant"}
	dnsContributionGVK    = schema.GroupVersionKind{Group: internalDNSGroup, Version: internalDNSVersion, Kind: "DNSRecordContribution"}
)

// InternalDNSProjectIdentity is the trusted identity shared with the DNS
// control plane for one Milo project. ProjectName is the discovery key, while
// the two UIDs are immutable authorization identities.
type InternalDNSProjectIdentity struct {
	ProjectName      string
	ProjectUID       types.UID
	SourceClusterUID string
}

// InternalDNSProjectAccess supplies an uncached reader and authenticated writer
// for the owning project API. The writer's Kubernetes subject must match the
// configured principal subject; DNS admission verifies both that subject and
// SourceClusterUID.
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
	ProjectIdentities           map[string]InternalDNSProjectIdentity
	PrincipalSubject            string
	LeaseDuration               time.Duration
	MaxConcurrentReconciles     int
	Now                         func() time.Time
	ObservationSources          map[string]InternalDNSObservationSource
	ObserveInterface            InternalDNSObserveFunc
}

func (r *InternalDNSPublisherReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, internalDNSAPITimeout)
	defer cancel()

	providerCluster, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var instance computev1alpha.Instance
	// Lease renewal must come from a live API read. A cached projection can
	// survive a disconnected source and would incorrectly keep old addresses
	// eligible forever.
	if err := providerCluster.GetAPIReader().Get(ctx, req.NamespacedName, &instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	projectID, err := r.projectID(ctx, req.ClusterName, &instance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolve DNS project identity: %w", err)
	}
	projectNamespace, err := r.projectNamespace(ctx, req.ClusterName, &instance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolve DNS project namespace: %w", err)
	}
	identity, ok := r.ProjectIdentities[projectID]
	if !ok || identity.ProjectUID == "" || identity.SourceClusterUID == "" {
		return ctrl.Result{}, fmt.Errorf("internal DNS identity is not configured for project %q", projectID)
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
	owner := metav1.OwnerReference{
		APIVersion: computev1alpha.GroupVersion.String(),
		Kind:       internalDNSInstanceKind,
		Name:       projectInstance.Name,
		UID:        projectInstance.UID,
		Controller: ptrBool(true),
	}

	if !instance.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.deletePublishedResources(ctx, access, projectNamespace, projectInstance.UID, nil)
	}

	attachments, err := instanceDNSAttachments(ctx, access.Reader, projectNamespace, &projectInstance, r.observeInterface)
	if err != nil {
		return ctrl.Result{}, err
	}
	desired := make(map[string]struct{}, len(attachments))
	publicationStatus := make([]computev1alpha.InstanceDNSStatus, 0, len(attachments))
	var publicationErr error
	pending := false
	for _, attachment := range attachments {
		resourceName := internalDNSResourceName(projectInstance.UID, attachment.VPCUID)
		desired[resourceName] = struct{}{}
		ready, err := r.reconcileAttachment(ctx, access, identity, projectNamespace, &projectInstance, owner, attachment, resourceName)
		publication, statusErr := r.publicationStatus(ctx, access.Reader, projectNamespace, &projectInstance, attachment, resourceName, ready, err)
		publicationStatus = append(publicationStatus, publication)
		if statusErr != nil {
			publicationErr = errors.Join(publicationErr, statusErr)
		}
		if err != nil {
			publicationErr = errors.Join(publicationErr, err)
		}
		pending = pending || !ready
	}
	if err := r.reportPublication(ctx, access.Writer, &projectInstance, publicationStatus); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.deletePublishedResources(ctx, access, projectNamespace, projectInstance.UID, desired); err != nil {
		return ctrl.Result{}, err
	}
	if pending {
		return ctrl.Result{RequeueAfter: internalDNSPendingWait}, publicationErr
	}
	return ctrl.Result{RequeueAfter: r.leaseDuration() / 3}, publicationErr
}

type instanceDNSAttachment struct {
	VPCUID           types.UID
	Network          networkingv1alpha.NetworkRef
	RecordSets       []any
	Eligible         bool
	ValidUntil       time.Time
	ObservationError error
}

func instanceDNSAttachments(ctx context.Context, reader client.Reader, projectNamespace string, instance *computev1alpha.Instance, observe InternalDNSObserveFunc) ([]instanceDNSAttachment, error) {
	byVPC := map[types.UID]*instanceDNSAttachment{}
	for _, nic := range instance.Spec.NetworkInterfaces {
		namespace := nic.Network.Namespace
		if namespace == "" {
			namespace = projectNamespace
		}
		var network networkingv1alpha.Network
		if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: nic.Network.Name}, &network); err != nil {
			return nil, fmt.Errorf("resolve VPC %s/%s for instance DNS: %w", namespace, nic.Network.Name, err)
		}
		if network.UID == "" {
			return nil, fmt.Errorf("network %s/%s has no API-assigned UID", namespace, nic.Network.Name)
		}
		attachment := byVPC[network.UID]
		if attachment == nil {
			attachment = &instanceDNSAttachment{VPCUID: network.UID, Network: networkingv1alpha.NetworkRef{Name: network.Name, Namespace: namespace}, RecordSets: []any{}}
			byVPC[network.UID] = attachment
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
		if attachment.ValidUntil.IsZero() || observation.ValidUntil.Before(attachment.ValidUntil) {
			attachment.ValidUntil = observation.ValidUntil
		}
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
				"recordType": rrtype,
				"records":    []any{map[string]any{internalDNSNameField: "", "ttl": int64(30), field: map[string]any{"content": addr.String()}}},
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
	managed, err := findResolverContext(ctx, access.Reader, namespace, identity.ProjectUID, attachment.VPCUID)
	if err != nil {
		return false, err
	}
	if managed == nil {
		return false, nil
	}
	suffix, _, _ := unstructured.NestedString(managed.Object, internalDNSStatusField, "managedNamespace", "suffix")
	if strings.TrimSuffix(strings.ToLower(suffix), ".") != defaultInternalDNSDomain {
		return false, fmt.Errorf("DNS context %q must allocate the default domain %q", managed.GetName(), defaultInternalDNSDomain)
	}
	zoneRef, _, _ := unstructured.NestedMap(managed.Object, internalDNSStatusField, "managedNamespace", "dnsZoneRef")
	if stringValue(zoneRef, internalDNSNameField) == "" || stringValue(zoneRef, internalDNSUIDField) == "" {
		return false, nil
	}

	labels := map[string]string{
		internalDNSInstanceUID: string(instance.UID),
		internalDNSVPCUID:      string(attachment.VPCUID),
		internalDNSManagedBy:   internalDNSManager,
	}
	ownerName := allocatedInstanceDNSName(instance)
	for i := range attachment.RecordSets {
		rrset := attachment.RecordSets[i].(map[string]any)
		records := rrset["records"].([]any)
		for _, record := range records {
			record.(map[string]any)[internalDNSNameField] = ownerName
		}
	}

	registrationSpec := map[string]any{
		"dnsZoneRef":         zoneRef,
		internalDNSNameField: ownerName,
		"recordTypes":        []any{"A", internalDNSRecordTypeAAAA},
		"publicationPolicy":  "EligibleContributions",
		"ttlSeconds":         int64(30),
	}
	registration, changed, err := ensureDNSObject(ctx, access, dnsRegistrationGVK, namespace, resourceName, labels, owner, registrationSpec)
	if err != nil || changed {
		return false, err
	}
	registrationRef := objectReference(registration)

	grant, err := findContributionGrant(ctx, access.Reader, namespace, registration, identity.SourceClusterUID, r.PrincipalSubject, ownerName)
	if err != nil {
		return false, err
	}
	if grant == nil {
		return false, nil
	}
	writerEpoch, _, _ := unstructured.NestedInt64(grant.Object, internalDNSStatusField, "activeWriterEpoch")
	observedGrant, _, _ := unstructured.NestedInt64(grant.Object, internalDNSStatusField, "observedGrantGeneration")
	observedRegistration, _, _ := unstructured.NestedInt64(grant.Object, internalDNSStatusField, "observedRegistrationGeneration")
	if writerEpoch < 1 || observedGrant != grant.GetGeneration() || observedRegistration != registration.GetGeneration() {
		return false, nil
	}

	contributionSpec := map[string]any{
		"registrationRef": registrationRef,
		"grantRef":        objectReference(grant),
		"recordSets":      attachment.RecordSets,
	}
	contribution, changed, err := ensureDNSObject(ctx, access, dnsContributionGVK, namespace, resourceName, labels, owner, contributionSpec)
	if err != nil || changed {
		return false, err
	}
	boundEpoch, _, _ := unstructured.NestedInt64(contribution.Object, internalDNSStatusField, "writerEpoch")
	if boundEpoch != writerEpoch {
		return false, nil
	}

	return r.refreshObservation(ctx, access.Writer, contribution, writerEpoch, attachment.Eligible, attachment.ValidUntil)
}

func (r *InternalDNSPublisherReconciler) refreshObservation(ctx context.Context, writer client.Client, contribution *unstructured.Unstructured, writerEpoch int64, eligible bool, deadline time.Time) (bool, error) {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	validThrough := now.Add(r.leaseDuration())
	if eligible {
		if deadline.IsZero() || !deadline.After(now) {
			eligible = false
		} else if deadline.Before(validThrough) {
			validThrough = deadline
		}
	}
	oldEligible, _, _ := unstructured.NestedBool(contribution.Object, internalDNSStatusField, "eligible")
	observedGeneration, _, _ := unstructured.NestedInt64(contribution.Object, internalDNSStatusField, "observedGeneration")
	validUntilRaw, _, _ := unstructured.NestedString(contribution.Object, internalDNSStatusField, "validUntil")
	validUntil, _ := time.Parse(time.RFC3339Nano, validUntilRaw)
	if oldEligible == eligible && observedGeneration == contribution.GetGeneration() && validUntil.After(now.Add(r.leaseDuration()/3)) && !validUntil.After(validThrough) {
		return true, nil
	}

	base := contribution.DeepCopy()
	sequence, _, _ := unstructured.NestedInt64(contribution.Object, internalDNSStatusField, "sequence")
	owned := []struct {
		path  []string
		value any
	}{
		{[]string{internalDNSStatusField, "observedGeneration"}, contribution.GetGeneration()},
		{[]string{internalDNSStatusField, "writerEpoch"}, writerEpoch},
		{[]string{internalDNSStatusField, "sequence"}, sequence + 1},
		{[]string{internalDNSStatusField, "eligible"}, eligible},
		{[]string{internalDNSStatusField, internalDNSReasonField}, map[bool]string{true: "AddressAllocatedAndProgrammed", false: "AddressUnavailable"}[eligible]},
		{[]string{internalDNSStatusField, "validUntil"}, validThrough.UTC().Format(time.RFC3339Nano)},
	}
	for _, field := range owned {
		if err := unstructured.SetNestedField(contribution.Object, field.value, field.path...); err != nil {
			return false, err
		}
	}
	// Optimistic locking turns a concurrent DNS status write into a conflict and
	// retry. The retry reads the live sequence and platform-owned status fields;
	// it cannot overwrite a publication condition from a stale snapshot.
	if err := writer.Status().Patch(ctx, contribution, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return false, fmt.Errorf("refresh DNS contribution observation: %w", err)
	}
	return true, nil
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
		consumerID, _, _ := unstructured.NestedString(item.Object, "spec", "consumerID")
		if consumerID != string(projectUID)+"/"+string(vpcUID) || !unstructuredConditionCurrent(item, "Accepted") || !unstructuredConditionCurrent(item, "Ready") {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("multiple ready DNS resolver contexts match project UID %q and VPC UID %q", projectUID, vpcUID)
		}
		match = item.DeepCopy()
	}
	return match, nil
}

func unstructuredConditionCurrent(obj *unstructured.Unstructured, conditionType string) bool {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, internalDNSStatusField, "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if ok && condition["type"] == conditionType && condition[internalDNSStatusField] == string(metav1.ConditionTrue) && condition["observedGeneration"] == obj.GetGeneration() {
			return true
		}
	}
	return false
}

func findContributionGrant(ctx context.Context, reader client.Reader, namespace string, registration *unstructured.Unstructured, clusterUID, subject, ownerName string) (*unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(dnsGrantGVK.GroupVersion().WithKind(dnsGrantGVK.Kind + "List"))
	if err := reader.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list DNS contribution grants: %w", err)
	}
	var match *unstructured.Unstructured
	for i := range list.Items {
		grant := &list.Items[i]
		if !grant.GetDeletionTimestamp().IsZero() || grant.GetUID() == "" || grant.GetGeneration() < 1 || !unstructuredConditionCurrent(grant, "Active") {
			continue
		}
		ref, _, _ := unstructured.NestedMap(grant.Object, "spec", "registrationRef")
		principal, _, _ := unstructured.NestedMap(grant.Object, "spec", "principal")
		if !reflect.DeepEqual(ref, objectReference(registration)) || stringValue(principal, "clusterUID") != clusterUID || stringValue(principal, "subject") != subject {
			continue
		}
		scopes, _, _ := unstructured.NestedStringSlice(grant.Object, "spec", "nameScopes")
		types, _, _ := unstructured.NestedStringSlice(grant.Object, "spec", "recordTypes")
		if !containsDNSString(scopes, ownerName) || !containsDNSString(types, "A") || !containsDNSString(types, "AAAA") {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("multiple contribution grants authorize registration %q", registration.GetName())
		}
		match = grant.DeepCopy()
	}
	return match, nil
}

func containsDNSString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func ensureDNSObject(ctx context.Context, access InternalDNSProjectAccess, gvk schema.GroupVersionKind, namespace, name string, labels map[string]string, owner metav1.OwnerReference, spec map[string]any) (*unstructured.Unstructured, bool, error) {
	desired := newDNSObject(gvk, namespace, name)
	desired.SetLabels(labels)
	desired.SetOwnerReferences([]metav1.OwnerReference{owner})
	desired.Object["spec"] = spec
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
	currentSpec, _, _ := unstructured.NestedMap(current.Object, "spec")
	if authorityReferencesChanged(gvk, currentSpec, spec) {
		if err := access.Writer.Delete(ctx, current); client.IgnoreNotFound(err) != nil {
			return nil, false, err
		}
		return current, true, nil
	}
	if !reflect.DeepEqual(currentSpec, spec) || !reflect.DeepEqual(current.GetLabels(), labels) || !reflect.DeepEqual(current.GetOwnerReferences(), []metav1.OwnerReference{owner}) {
		current.Object["spec"] = spec
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

func authorityReferencesChanged(gvk schema.GroupVersionKind, current, desired map[string]any) bool {
	if gvk != dnsContributionGVK {
		return false
	}
	return !reflect.DeepEqual(current["registrationRef"], desired["registrationRef"]) || !reflect.DeepEqual(current["grantRef"], desired["grantRef"])
}

func (r *InternalDNSPublisherReconciler) deletePublishedResources(ctx context.Context, access InternalDNSProjectAccess, namespace string, instanceUID types.UID, keep map[string]struct{}) error {
	selector := client.MatchingLabels{internalDNSInstanceUID: string(instanceUID), internalDNSManagedBy: internalDNSManager}
	for _, gvk := range []schema.GroupVersionKind{dnsContributionGVK, dnsRegistrationGVK} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := access.Reader.List(ctx, list, client.InNamespace(namespace), selector); err != nil {
			return fmt.Errorf("list stale %s resources: %w", gvk.Kind, err)
		}
		for i := range list.Items {
			item := &list.Items[i]
			if _, retained := keep[item.GetName()]; retained {
				continue
			}
			if err := access.Writer.Delete(ctx, item); client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("delete stale %s %s/%s: %w", gvk.Kind, namespace, item.GetName(), err)
			}
		}
	}
	return nil
}

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

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func allocatedInstanceDNSName(instance *computev1alpha.Instance) string {
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
	if len(base) > 40 {
		base = strings.TrimRight(base[:40], "-")
	}
	return base + "-" + shortHash(string(instance.UID))
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

func (r *InternalDNSPublisherReconciler) leaseDuration() time.Duration {
	if r.LeaseDuration <= 0 {
		return defaultDNSLease
	}
	if r.LeaseDuration > maxDNSLease {
		return maxDNSLease
	}
	return r.LeaseDuration
}

func (r *InternalDNSPublisherReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	if strings.TrimSpace(r.PrincipalSubject) == "" {
		return errors.New("internal DNS principal subject is required")
	}
	if len(r.ProjectIdentities) == 0 {
		return errors.New("at least one internal DNS project identity is required")
	}
	if r.LeaseDuration > maxDNSLease {
		return fmt.Errorf("internal DNS lease duration %s exceeds the platform maximum %s", r.LeaseDuration, maxDNSLease)
	}
	r.mgr = mgr
	concurrency := r.MaxConcurrentReconciles
	if concurrency <= 0 {
		concurrency = defaultDNSConcurrency
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("internal-dns-instance-publisher").
		For(&computev1alpha.Instance{}, mcbuilder.WithEngageWithLocalCluster(false)).
		WithOptions(mccontroller.Options{MaxConcurrentReconciles: concurrency}).
		Complete(r)
}
