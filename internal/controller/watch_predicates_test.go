// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	caseNoChange       = "no change"
	caseFinalizerAdded = "finalizer added"
	caseDeleting       = "deleting"
	msgStarting        = "starting"
)

func baseInstance() *computev1alpha.Instance {
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web-0", Namespace: "ns", UID: "uid-1", Generation: 1,
			Labels: map[string]string{"a": "b"},
		},
		Status: computev1alpha.InstanceStatus{
			Conditions: []metav1.Condition{
				{Type: computev1alpha.InstanceReady, Status: metav1.ConditionFalse, Reason: computev1alpha.InstanceReadyReasonProvisioning, Message: msgStarting},
				{Type: computev1alpha.InstanceAvailable, Status: metav1.ConditionFalse, Reason: "Starting", Message: msgStarting},
				{Type: computev1alpha.InstanceRuntimeReady, Status: metav1.ConditionFalse, Reason: claimTestPendingReason},
			},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterfaceStatus{{
				Name: defaultInterfaceName,
				Conditions: []metav1.Condition{
					{Type: computev1alpha.InstanceNetworkInterfaceAllocated, Status: metav1.ConditionFalse, Reason: claimTestPendingReason},
				},
			}},
		},
	}
}

func passesUpdate(p predicate.Predicate, old, updated client.Object) bool {
	return p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated})
}

func TestInstanceChangedForDeployment(t *testing.T) {
	p := instanceChangedForDeployment()

	cases := []struct {
		name   string
		mutate func(*computev1alpha.Instance)
		want   bool
	}{
		{caseNoChange, func(*computev1alpha.Instance) {}, false},
		{"resource version only", func(i *computev1alpha.Instance) { i.ResourceVersion = "2" }, false},
		{caseFinalizerAdded, func(i *computev1alpha.Instance) { i.Finalizers = []string{"x"} }, false},
		{"annotation added", func(i *computev1alpha.Instance) { i.Annotations = map[string]string{"x": "y"} }, false},
		{"interface status", func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces[0].Addresses = []computev1alpha.InstanceNetworkInterfaceAddress{{Address: "fd00::1"}}
		}, false},
		{"dns status", func(i *computev1alpha.Instance) {
			i.Status.DNS = []computev1alpha.InstanceDNSStatus{{}}
		}, false},
		{"runtime ready", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceRuntimeReady, Status: metav1.ConditionTrue, Reason: "RuntimeReady"})
		}, false},
		{"ready message only", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceReady, Status: metav1.ConditionFalse, Reason: computev1alpha.InstanceReadyReasonProvisioning, Message: "still starting"})
		}, false},
		{"ready true", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceReady, Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceReadyReasonAvailable})
		}, true},
		{"programmed appears", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceProgrammed, Status: metav1.ConditionTrue, Reason: "Programmed"})
		}, true},
		{"quota denied", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceQuotaGranted, Status: metav1.ConditionFalse, Reason: "QuotaExceeded"})
		}, true},
		{"referenced data blocked", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.ReferencedDataReady, Status: metav1.ConditionFalse, Reason: "AwaitingPropagation"})
		}, true},
		{"available reason", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceAvailable, Status: metav1.ConditionFalse, Reason: computev1alpha.InstanceReadyReasonInstanceCrashing, Message: msgStarting})
		}, true},
		{"available message", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceAvailable, Status: metav1.ConditionFalse, Reason: "Starting", Message: "crashed"})
		}, true},
		{"observed template hash", func(i *computev1alpha.Instance) {
			i.Status.Controller = &computev1alpha.InstanceControllerStatus{ObservedTemplateHash: "h"}
		}, true},
		{"suspended", func(i *computev1alpha.Instance) { i.Status.Suspended = true }, true},
		{"spec change", func(i *computev1alpha.Instance) { i.Generation = 2 }, true},
		{"label change", func(i *computev1alpha.Instance) { i.Labels["a"] = "c" }, true},
		{"owner change", func(i *computev1alpha.Instance) {
			i.OwnerReferences = []metav1.OwnerReference{{UID: "wd"}}
		}, true},
		{caseDeleting, func(i *computev1alpha.Instance) { now := metav1.Now(); i.DeletionTimestamp = &now }, true},
		{"replaced", func(i *computev1alpha.Instance) { i.UID = "uid-2" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := baseInstance()
			updated := old.DeepCopy()
			updated.ResourceVersion = "2"
			tc.mutate(updated)
			assert.Equal(t, tc.want, passesUpdate(p, old, updated))
		})
	}

	// A periodic resync redelivers the same object and must still pass.
	assert.True(t, passesUpdate(p, baseInstance(), baseInstance()))
	assert.True(t, p.Create(event.CreateEvent{Object: baseInstance()}))
	assert.True(t, p.Delete(event.DeleteEvent{Object: baseInstance()}))
}

func TestNetworkInterfaceClaimChangedForDeployment(t *testing.T) {
	p := networkInterfaceClaimChangedForDeployment()
	base := func() *networkingv1alpha.NetworkInterfaceClaim {
		return &networkingv1alpha.NetworkInterfaceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "web-0-eth0", UID: "c1", Generation: 1},
			Status: networkingv1alpha.NetworkInterfaceClaimStatus{
				Conditions: []metav1.Condition{
					{Type: networkingv1alpha.NetworkInterfaceClaimBound, Status: metav1.ConditionFalse, Reason: claimTestPendingReason},
				},
			},
		}
	}

	cases := []struct {
		name   string
		mutate func(*networkingv1alpha.NetworkInterfaceClaim)
		want   bool
	}{
		{caseNoChange, func(*networkingv1alpha.NetworkInterfaceClaim) {}, false},
		{caseFinalizerAdded, func(c *networkingv1alpha.NetworkInterfaceClaim) { c.Finalizers = []string{"x"} }, false},
		{"unrelated condition", func(c *networkingv1alpha.NetworkInterfaceClaim) {
			apimeta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: "Other", Status: metav1.ConditionTrue, Reason: "X"})
		}, false},
		{"bound", func(c *networkingv1alpha.NetworkInterfaceClaim) {
			apimeta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: networkingv1alpha.NetworkInterfaceClaimBound, Status: metav1.ConditionTrue, Reason: "Bound"})
		}, true},
		{"allocated appears", func(c *networkingv1alpha.NetworkInterfaceClaim) {
			apimeta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: networkingv1alpha.NetworkInterfaceClaimAllocated, Status: metav1.ConditionTrue, Reason: "Allocated"})
		}, true},
		{"prepared rejected", func(c *networkingv1alpha.NetworkInterfaceClaim) {
			apimeta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: networkingv1alpha.NetworkInterfaceClaimPrepared, Status: metav1.ConditionFalse, Reason: "Rejected"})
		}, true},
		{"rejection message", func(c *networkingv1alpha.NetworkInterfaceClaim) {
			apimeta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: networkingv1alpha.NetworkInterfaceClaimBound, Status: metav1.ConditionFalse, Reason: claimTestPendingReason, Message: "no capacity"})
		}, true},
		{"label change", func(c *networkingv1alpha.NetworkInterfaceClaim) { c.Labels = map[string]string{"a": "b"} }, true},
		{caseDeleting, func(c *networkingv1alpha.NetworkInterfaceClaim) { now := metav1.Now(); c.DeletionTimestamp = &now }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := base()
			updated := old.DeepCopy()
			updated.ResourceVersion = "2"
			tc.mutate(updated)
			assert.Equal(t, tc.want, passesUpdate(p, old, updated))
		})
	}
}

func TestInstanceChangedForDNSPublication(t *testing.T) {
	p := instanceChangedForDNSPublication()

	cases := []struct {
		name   string
		mutate func(*computev1alpha.Instance)
		want   bool
	}{
		{caseNoChange, func(*computev1alpha.Instance) {}, false},
		{caseFinalizerAdded, func(i *computev1alpha.Instance) { i.Finalizers = []string{"x"} }, false},
		{"programmed", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceProgrammed, Status: metav1.ConditionTrue, Reason: "Programmed"})
		}, false},
		{"ready message only", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceReady, Status: metav1.ConditionFalse, Reason: computev1alpha.InstanceReadyReasonProvisioning, Message: "still starting"})
		}, false},
		{"interface condition message", func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces[0].Conditions[0].Message = "waiting"
		}, false},
		{"external address", func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces[0].ExternalAddresses = []computev1alpha.InstanceNetworkInterfaceExternalAddress{{}}
		}, false},
		{"ready true", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceReady, Status: metav1.ConditionTrue, Reason: computev1alpha.InstanceReadyReasonAvailable})
		}, true},
		{"runtime ready", func(i *computev1alpha.Instance) {
			apimeta.SetStatusCondition(&i.Status.Conditions, metav1.Condition{Type: computev1alpha.InstanceRuntimeReady, Status: metav1.ConditionTrue, Reason: "RuntimeReady"})
		}, true},
		{"interface allocated", func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces[0].Conditions[0].Status = metav1.ConditionTrue
		}, true},
		{"interface address", func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces[0].Addresses = []computev1alpha.InstanceNetworkInterfaceAddress{{Address: "fd00::1"}}
		}, true},
		{"interface ref", func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces[0].NetworkInterfaceRef = &networkingv1alpha.LocalNetworkInterfaceRef{Name: "nic"}
		}, true},
		{"interface added", func(i *computev1alpha.Instance) {
			i.Status.NetworkInterfaces = append(i.Status.NetworkInterfaces, computev1alpha.InstanceNetworkInterfaceStatus{Name: "secondary"})
		}, true},
		{"dns status", func(i *computev1alpha.Instance) {
			i.Status.DNS = []computev1alpha.InstanceDNSStatus{{}}
		}, true},
		{"annotation", func(i *computev1alpha.Instance) { i.Annotations = map[string]string{"x": "y"} }, false},
		{"label", func(i *computev1alpha.Instance) { i.Labels["a"] = "c" }, true},
		{caseDeleting, func(i *computev1alpha.Instance) { now := metav1.Now(); i.DeletionTimestamp = &now }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := baseInstance()
			updated := old.DeepCopy()
			updated.ResourceVersion = "2"
			tc.mutate(updated)
			assert.Equal(t, tc.want, passesUpdate(p, old, updated))
		})
	}
}
