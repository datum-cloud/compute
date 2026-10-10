// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *InternalDNSPublisherReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *InternalDNSPublisherReconciler) observeInterface(ctx context.Context, reader client.Reader, namespace string, instance *computev1alpha.Instance, nic computev1alpha.InstanceNetworkInterface) (InternalDNSObservedInterface, error) {
	if r.ObserveInterface != nil {
		return r.ObserveInterface(ctx, reader, namespace, instance, nic)
	}
	return ObserveInternalDNSInterface(ctx, reader, namespace, instance, nic, r.ObservationSources[instance.Labels[computev1alpha.LocationLabel]], r.now())
}

func (r *InternalDNSPublisherReconciler) publicationStatus(ctx context.Context, reader client.Reader, namespace string, instance *computev1alpha.Instance, attachment instanceDNSAttachment, resourceName string, authorized bool, reconcileErr error) (computev1alpha.InstanceDNSStatus, error) {
	result := computev1alpha.InstanceDNSStatus{Network: attachment.Network, NetworkUID: string(attachment.VPCUID)}
	for _, previous := range instance.Status.DNS {
		if previous.NetworkUID == result.NetworkUID {
			result.Conditions = append([]metav1.Condition(nil), previous.Conditions...)
		}
	}
	condition := metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "PendingPublication", Message: "Waiting for DNS publication", ObservedGeneration: instance.Generation}
	registration := newDNSObject(dnsRegistrationGVK, namespace, resourceName)
	err := reader.Get(ctx, client.ObjectKeyFromObject(registration), registration)
	if err != nil && !apierrors.IsNotFound(err) {
		return result, err
	}
	if err == nil {
		canonical, _, _ := unstructured.NestedString(registration.Object, "status", "canonicalFQDN")
		canonical = strings.TrimSuffix(strings.ToLower(canonical), ".")
		if canonical == allocatedInstanceDNSName(instance)+"."+defaultInternalDNSDomain {
			result.Hostnames = []string{canonical}
		}
		contribution := newDNSObject(dnsContributionGVK, namespace, resourceName)
		if err := reader.Get(ctx, client.ObjectKeyFromObject(contribution), contribution); err != nil && !apierrors.IsNotFound(err) {
			return result, err
		}
		deadlineRaw, _, _ := unstructured.NestedString(contribution.Object, "status", "validUntil")
		deadline, _ := time.Parse(time.RFC3339Nano, deadlineRaw)
		if authorized && len(result.Hostnames) > 0 && unstructuredConditionCurrent(registration, "Published") && unstructuredConditionCurrent(contribution, "Published") && deadline.After(r.now()) && attachment.Eligible && attachment.ObservationError == nil {
			condition.Status, condition.Reason, condition.Message = metav1.ConditionTrue, "Published", "Private name is published"
		}
	}
	if attachment.ObservationError != nil {
		condition.Reason, condition.Message = "NetworkObservationUnavailable", "No current network observation is available"
	} else if !attachment.Eligible {
		condition.Reason, condition.Message = "AddressUnavailable", "No currently eligible private host address is available"
	}
	if reconcileErr != nil {
		condition.Reason, condition.Message = "PublicationFailed", "DNS publication could not be reconciled"
	}
	apimeta.SetStatusCondition(&result.Conditions, condition)
	return result, nil
}

func (r *InternalDNSPublisherReconciler) reportPublication(ctx context.Context, writer client.Client, instance *computev1alpha.Instance, status []computev1alpha.InstanceDNSStatus) error {
	if reflect.DeepEqual(instance.Status.DNS, status) {
		return nil
	}
	base := instance.DeepCopy()
	instance.Status.DNS = status
	if err := writer.Status().Patch(ctx, instance, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("report Instance DNS publication: %w", err)
	}
	return nil
}
