// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *InternalDNSPublisherReconciler) observeInterface(ctx context.Context, reader client.Reader, namespace string, instance *computev1alpha.Instance, nic computev1alpha.InstanceNetworkInterface) (InternalDNSObservedInterface, error) {
	if r.ObserveInterface != nil {
		return r.ObserveInterface(ctx, reader, namespace, instance, nic)
	}
	return ObserveInternalDNSInterface(ctx, reader, namespace, instance, nic, r.ObservationSources[instance.Labels[computev1alpha.LocationLabel]])
}

func (r *InternalDNSPublisherReconciler) publicationStatus(ctx context.Context, reader client.Reader, identity InternalDNSProjectIdentity, namespace string, instance *computev1alpha.Instance, attachment instanceDNSAttachment, resourceName string, observed bool, reconcileErr error) (computev1alpha.InstanceDNSStatus, error) {
	result := computev1alpha.InstanceDNSStatus{Network: attachment.Network}
	for _, previous := range instance.Status.DNS {
		if previous.Network.Name == result.Network.Name && previous.Network.Namespace == result.Network.Namespace {
			result.Conditions = append([]metav1.Condition(nil), previous.Conditions...)
		}
	}
	condition := metav1.Condition{Type: internalDNSConditionReady, Status: metav1.ConditionFalse, Reason: "PendingPublication", Message: "Waiting for DNS publication", ObservedGeneration: instance.Generation}
	resolver, err := findResolverContext(ctx, reader, namespace, identity.ProjectUID, attachment.VPCUID)
	if err != nil {
		return result, err
	}
	registrationName := resourceName
	if resolver != nil {
		registrationName = instanceDNSReservationName(resolver)
	}
	registration := newDNSObject(dnsRegistrationGVK, namespace, registrationName)
	err = reader.Get(ctx, client.ObjectKeyFromObject(registration), registration)
	if err != nil && !apierrors.IsNotFound(err) {
		return result, err
	}
	if err == nil {
		canonical, _, _ := unstructured.NestedString(registration.Object, internalDNSStatusField, "canonicalFQDN")
		canonical = strings.TrimSuffix(strings.ToLower(canonical), ".")
		matches, err := publicationMatchesContext(ctx, reader, identity, namespace, attachment, registration, canonical)
		if err != nil {
			return result, err
		}
		if matches {
			result.Hostnames = []string{allocatedInstanceDNSName(instance) + "." + strings.TrimPrefix(canonical, instanceDNSScope+".")}
		}
		if registrationHasNameConflict(registration) {
			condition.Reason, condition.Message = internalDNSNameConflict, "Another resource already owns this DNS name"
		}
		contribution := newDNSObject(dnsContributionGVK, namespace, resourceName)
		if err := reader.Get(ctx, client.ObjectKeyFromObject(contribution), contribution); err != nil && !apierrors.IsNotFound(err) {
			return result, err
		}
		contributionRegistration, _, _ := unstructured.NestedMap(contribution.Object, internalDNSSpecField, internalDNSRegistrationRefField)
		if observed && len(result.Hostnames) > 0 && reflect.DeepEqual(contributionRegistration, objectReference(registration)) && unstructuredConditionCurrent(registration, internalDNSConditionAccepted) && unstructuredConditionCurrent(registration, internalDNSConditionPublished) && unstructuredConditionCurrent(contribution, internalDNSConditionPublished) && attachment.Eligible && attachment.ObservationError == nil {
			condition.Status, condition.Reason, condition.Message = metav1.ConditionTrue, internalDNSConditionPublished, "Private name is published"
		}
	}
	if errors.Is(attachment.ObservationError, errNetworkObservationUnconfigured) {
		condition.Reason, condition.Message = "NetworkObservationUnconfigured", "Private DNS is not available in this location"
	} else if attachment.ObservationError != nil {
		condition.Status = metav1.ConditionUnknown
		condition.Reason, condition.Message = "NetworkObservationUnavailable", "No current network observation is available"
	} else if !attachment.Eligible {
		condition.Reason, condition.Message = "AddressUnavailable", "No currently eligible private host address is available"
	}
	if reconcileErr != nil && attachment.ObservationError == nil {
		condition.Reason, condition.Message = "PublicationFailed", "DNS publication could not be reconciled"
	}
	apimeta.SetStatusCondition(&result.Conditions, condition)
	return result, nil
}

func registrationHasNameConflict(registration *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(registration.Object, internalDNSStatusField, internalDNSConditionsField)
	for _, value := range conditions {
		accepted, ok := value.(map[string]any)
		if ok && accepted[internalDNSConditionTypeField] == internalDNSConditionAccepted && accepted[internalDNSStatusField] == string(metav1.ConditionFalse) && accepted[internalDNSObservedGenerationField] == registration.GetGeneration() && accepted[internalDNSReasonField] == internalDNSNameConflict {
			return true
		}
	}
	return false
}

func publicationMatchesContext(ctx context.Context, reader client.Reader, identity InternalDNSProjectIdentity, namespace string, attachment instanceDNSAttachment, registration *unstructured.Unstructured, canonical string) (bool, error) {
	resolver, err := findResolverContext(ctx, reader, namespace, identity.ProjectUID, attachment.VPCUID)
	if err != nil || resolver == nil {
		return false, err
	}
	suffix, zone, err := allocatedDNSNamespace(resolver)
	if err != nil {
		return false, err
	}
	registeredZone, _, _ := unstructured.NestedMap(registration.Object, internalDNSSpecField, internalDNSZoneRefField)
	return canonical == instanceDNSScope+"."+suffix && referenceName(registeredZone) == referenceName(zone), nil
}

func (r *InternalDNSPublisherReconciler) reportPublication(ctx context.Context, writer client.Client, reader client.Reader, instance *computev1alpha.Instance, status []computev1alpha.InstanceDNSStatus) error {
	if reflect.DeepEqual(instance.Status.DNS, status) {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &computev1alpha.Instance{}
		if err := reader.Get(ctx, client.ObjectKeyFromObject(instance), latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		if latest.UID != instance.UID || latest.Generation != instance.Generation {
			return apierrors.NewConflict(schema.GroupResource{Group: "compute.datumapis.com", Resource: "instances"}, instance.Name, fmt.Errorf("instance changed during DNS publication"))
		}
		if reflect.DeepEqual(latest.Status.DNS, status) {
			return nil
		}
		base := latest.DeepCopy()
		latest.Status.DNS = status
		if err := writer.Status().Patch(ctx, latest, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return fmt.Errorf("report Instance DNS publication: %w", err)
		}
		return nil
	})
}
