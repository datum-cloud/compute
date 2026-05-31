package stateful

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"

	"go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(v1alpha.AddToScheme(scheme))
}

func TestFreshDeployment(t *testing.T) {
	ctx := context.Background()
	control := New(Options{})

	deployment := getWorkloadDeployment("test-fresh-deploy", 2)

	// No instances
	var currentInstances []v1alpha.Instance
	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)
	assert.Len(t, actions, 2)

	assert.Equal(t, "test-fresh-deploy-0", actions[0].Object.GetName())
	assert.Equal(t, instancecontrol.ActionTypeCreate, actions[0].ActionType())
	assert.False(t, actions[0].IsSkipped())

	assert.Equal(t, "test-fresh-deploy-1", actions[1].Object.GetName())
	assert.Equal(t, instancecontrol.ActionTypeCreate, actions[1].ActionType())
	assert.True(t, actions[1].IsSkipped())
}

// TestUpdateWithAllReadyInstances verifies that a template change on Ready
// instances rolls them by delete+recreate (not an in-place update), ordered
// highest-ordinal-first with only the first action active. An in-place update
// would never roll the backing pod, since the unikraft provider bakes the pod
// at creation time and ignores spec changes on an existing pod.
func TestUpdateWithAllReadyInstances(t *testing.T) {
	ctx := context.Background()
	control := New(Options{})

	deployment := getWorkloadDeployment("test-deploy", 2)

	var currentInstances []v1alpha.Instance
	currentInstances = append(currentInstances, *getInstanceForDeployment(deployment, 0))
	currentInstances = append(currentInstances, *getInstanceForDeployment(deployment, 1))

	deployment.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "test-image-update"

	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)
	assert.Len(t, actions, 2)

	assert.Equal(t, "test-deploy-1", actions[0].Object.GetName())
	assert.Equal(t, instancecontrol.ActionTypeDelete, actions[0].ActionType())
	assert.False(t, actions[0].IsSkipped())

	assert.Equal(t, "test-deploy-0", actions[1].Object.GetName())
	assert.Equal(t, instancecontrol.ActionTypeDelete, actions[1].ActionType())
	assert.True(t, actions[1].IsSkipped())
}

func TestScaleUpWithNotReadyInstance(t *testing.T) {
	ctx := context.Background()
	control := New(Options{})

	deployment := getWorkloadDeployment("test-deploy", 3)

	var currentInstances []v1alpha.Instance
	currentInstances = append(currentInstances, *getInstanceForDeployment(deployment, 0))

	notReadyInstance := getInstanceForDeployment(deployment, 1)
	apimeta.SetStatusCondition(&notReadyInstance.Status.Conditions, metav1.Condition{
		Type:   v1alpha.InstanceReady,
		Status: metav1.ConditionFalse,
	})
	currentInstances = append(currentInstances, *notReadyInstance)

	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)
	assert.Len(t, actions, 2)

	assert.Equal(t, "test-deploy-1", actions[0].Object.GetName())
	assert.Equal(t, instancecontrol.ActionTypeWait, actions[0].ActionType())
	assert.False(t, actions[0].IsSkipped())

	assert.Equal(t, "test-deploy-2", actions[1].Object.GetName())
	assert.Equal(t, instancecontrol.ActionTypeCreate, actions[1].ActionType())
	assert.True(t, actions[1].IsSkipped())
}

func TestScaleUpWithDeletingReadyInstance(t *testing.T) {
	ctx := context.Background()
	control := New(Options{})

	deployment := getWorkloadDeployment("test-deploy", 3)

	var currentInstances []v1alpha.Instance
	currentInstances = append(currentInstances, *getInstanceForDeployment(deployment, 0))

	deletingInstance := getInstanceForDeployment(deployment, 1)
	deletingInstance.DeletionTimestamp = ptr.To(metav1.Now())
	currentInstances = append(currentInstances, *deletingInstance)

	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)
	assert.Len(t, actions, 2)

	assert.Equal(t, "test-deploy-1", actions[0].Object.GetName())
	assert.Equal(t, instancecontrol.ActionTypeWait, actions[0].ActionType())
	assert.False(t, actions[0].IsSkipped())

	assert.Equal(t, "test-deploy-2", actions[1].Object.GetName())
	assert.Equal(t, instancecontrol.ActionTypeCreate, actions[1].ActionType())
	assert.True(t, actions[1].IsSkipped())
}

func TestScaleDownWithAllReadyInstances(t *testing.T) {
	ctx := context.Background()
	control := New(Options{})

	deployment := getWorkloadDeployment("test-deploy", 1)

	var currentInstances []v1alpha.Instance
	currentInstances = append(currentInstances, *getInstanceForDeployment(deployment, 0))
	currentInstances = append(currentInstances, *getInstanceForDeployment(deployment, 1))

	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)
	assert.Len(t, actions, 1)

	assert.Equal(t, "test-deploy-1", actions[0].Object.GetName())
	assert.Equal(t, instancecontrol.ActionTypeDelete, actions[0].ActionType())
	assert.False(t, actions[0].IsSkipped())
}

// TestNetworkingEnabledAddsNetworkGate verifies that when networking is enabled
// (the default), newly created Instances receive both the Network and Quota
// scheduling gates so that they are held until the network is provisioned.
func TestNetworkingEnabledAddsNetworkGate(t *testing.T) {
	ctx := context.Background()
	control := NewWithOptions(Options{NetworkingEnabled: true})

	deployment := getWorkloadDeployment("test-deploy-net-on", 1)

	var currentInstances []v1alpha.Instance
	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)
	assert.Len(t, actions, 1)
	assert.Equal(t, instancecontrol.ActionTypeCreate, actions[0].ActionType())

	instance, ok := actions[0].Object.(*v1alpha.Instance)
	assert.True(t, ok)
	assert.NotNil(t, instance.Spec.Controller)

	gateNames := make([]string, 0, len(instance.Spec.Controller.SchedulingGates))
	for _, g := range instance.Spec.Controller.SchedulingGates {
		gateNames = append(gateNames, g.Name)
	}
	assert.Contains(t, gateNames, instancecontrol.NetworkSchedulingGate.String(),
		"Network gate must be present when networking is enabled")
	assert.Contains(t, gateNames, instancecontrol.QuotaSchedulingGate.String(),
		"Quota gate must be present")
}

// TestNetworkingDisabledOmitsNetworkGate verifies that when networking is
// disabled, newly created Instances do NOT receive the Network scheduling gate,
// so they are not blocked on network provisioning. The Quota gate is still
// added so quota enforcement remains active.
func TestNetworkingDisabledOmitsNetworkGate(t *testing.T) {
	ctx := context.Background()
	control := NewWithOptions(Options{NetworkingEnabled: false})

	deployment := getWorkloadDeployment("test-deploy-net-off", 1)

	var currentInstances []v1alpha.Instance
	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)
	assert.Len(t, actions, 1)
	assert.Equal(t, instancecontrol.ActionTypeCreate, actions[0].ActionType())

	instance, ok := actions[0].Object.(*v1alpha.Instance)
	assert.True(t, ok)
	assert.NotNil(t, instance.Spec.Controller)

	gateNames := make([]string, 0, len(instance.Spec.Controller.SchedulingGates))
	for _, g := range instance.Spec.Controller.SchedulingGates {
		gateNames = append(gateNames, g.Name)
	}
	assert.NotContains(t, gateNames, instancecontrol.NetworkSchedulingGate.String(),
		"Network gate must NOT be present when networking is disabled")
	assert.Contains(t, gateNames, instancecontrol.QuotaSchedulingGate.String(),
		"Quota gate must still be present when networking is disabled")
}

// Add more test functions below for different scenarios.

// TestInstanceLabels_FourNewLabelsStamped verifies that all four new
// self-describing labels are stamped on newly created Instances, with values
// sourced from the WorkloadDeployment spec.
func TestInstanceLabels_FourNewLabelsStamped(t *testing.T) {
	ctx := context.Background()
	control := New()

	deployment := getWorkloadDeployment("test-labels-deploy", 1)

	var currentInstances []v1alpha.Instance
	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)
	assert.Len(t, actions, 1)
	assert.Equal(t, instancecontrol.ActionTypeCreate, actions[0].ActionType())

	instance, ok := actions[0].Object.(*v1alpha.Instance)
	assert.True(t, ok)

	assert.Equal(t, deployment.GetName(), instance.Labels[v1alpha.WorkloadDeploymentNameLabel],
		"WorkloadDeploymentNameLabel must equal deployment name")
	assert.Equal(t, deployment.Spec.CityCode, instance.Labels[v1alpha.CityCodeLabel],
		"CityCodeLabel must equal deployment.Spec.CityCode")
	assert.Equal(t, deployment.Spec.WorkloadRef.Name, instance.Labels[v1alpha.WorkloadNameLabel],
		"WorkloadNameLabel must equal deployment.Spec.WorkloadRef.Name")
	assert.Equal(t, deployment.Spec.PlacementName, instance.Labels[v1alpha.PlacementNameLabel],
		"PlacementNameLabel must equal deployment.Spec.PlacementName")
}

// TestInstanceLabels_RefreshedOnRecreate verifies that when a template change
// rolls an instance, the recreated instance carries the four self-describing
// labels sourced from the WorkloadDeployment. A template change no longer
// updates the instance in place; it deletes the drifted instance and recreates
// it via the create path on the following reconcile, which stamps the labels.
func TestInstanceLabels_RefreshedOnRecreate(t *testing.T) {
	ctx := context.Background()
	control := New()

	deployment := getWorkloadDeployment("test-labels-update", 1)

	// A ready existing instance on the old template hash.
	currentInstances := []v1alpha.Instance{*getInstanceForDeployment(deployment, 0)}

	// Trigger a roll by changing the image.
	deployment.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "updated-image"

	// First reconcile: the drifted instance is deleted (recreate), not updated.
	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)
	assert.NoError(t, err)
	assert.Len(t, actions, 1)
	assert.Equal(t, instancecontrol.ActionTypeDelete, actions[0].ActionType())
	assert.Equal(t, "test-labels-update-0", actions[0].Object.GetName())

	// Next reconcile, after the old instance has been fully deleted and is gone:
	// the empty slot is refilled by the create path, which stamps the labels.
	actions, err = control.GetActions(ctx, scheme, deployment, nil)
	assert.NoError(t, err)
	assert.Len(t, actions, 1)
	assert.Equal(t, instancecontrol.ActionTypeCreate, actions[0].ActionType())

	instance, ok := actions[0].Object.(*v1alpha.Instance)
	assert.True(t, ok)

	assert.Equal(t, deployment.GetName(), instance.Labels[v1alpha.WorkloadDeploymentNameLabel],
		"WorkloadDeploymentNameLabel must be set on the recreated instance")
	assert.Equal(t, deployment.Spec.CityCode, instance.Labels[v1alpha.CityCodeLabel],
		"CityCodeLabel must be set on the recreated instance")
	assert.Equal(t, deployment.Spec.WorkloadRef.Name, instance.Labels[v1alpha.WorkloadNameLabel],
		"WorkloadNameLabel must be set on the recreated instance")
	assert.Equal(t, deployment.Spec.PlacementName, instance.Labels[v1alpha.PlacementNameLabel],
		"PlacementNameLabel must be set on the recreated instance")
}

// TestInstanceLocation_SetWhenDeploymentStatusLocationPresent verifies that when
// deployment.Status.Location is set, the new Instance receives it as Spec.Location.
func TestInstanceLocation_SetWhenDeploymentStatusLocationPresent(t *testing.T) {
	ctx := context.Background()
	control := New()

	deployment := getWorkloadDeployment("test-location-set", 1)
	deployment.Status.Location = &networkingv1alpha.LocationReference{
		Name:      "loc-dfw-1",
		Namespace: "networking-system",
	}

	var currentInstances []v1alpha.Instance
	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)
	assert.Len(t, actions, 1)

	instance, ok := actions[0].Object.(*v1alpha.Instance)
	assert.True(t, ok)
	assert.NotNil(t, instance.Spec.Location,
		"Spec.Location must be set when deployment.Status.Location is non-nil")
	assert.Equal(t, "loc-dfw-1", instance.Spec.Location.Name)
	assert.Equal(t, "networking-system", instance.Spec.Location.Namespace)
}

// TestInstanceLocation_NilWhenDeploymentStatusLocationAbsent verifies that when
// deployment.Status.Location is nil (no Location object matches the city code),
// instance creation still succeeds and Spec.Location remains nil — no regression
// on the "create instances regardless of Location" contract.
func TestInstanceLocation_NilWhenDeploymentStatusLocationAbsent(t *testing.T) {
	ctx := context.Background()
	control := New()

	deployment := getWorkloadDeployment("test-location-nil", 1)
	// deployment.Status.Location is intentionally not set (nil)

	var currentInstances []v1alpha.Instance
	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err, "instance creation must succeed even when Status.Location is nil")
	assert.Len(t, actions, 1, "exactly one create action must be produced")

	instance, ok := actions[0].Object.(*v1alpha.Instance)
	assert.True(t, ok)
	assert.Nil(t, instance.Spec.Location,
		"Spec.Location must remain nil when deployment.Status.Location is not set")
	assert.Equal(t, instancecontrol.ActionTypeCreate, actions[0].ActionType(),
		"action must be a Create, proving instance creation is not gated on Location")
}

// TestLabelBackfill_NotReadyMatchingHash verifies that a not-Ready instance
// with an unchanged template hash receives a PatchLabels action when it is
// missing controller-managed labels. The action must not be a rollout recreate,
// must not alter spec/template, and must not block subsequent instances.
func TestLabelBackfill_NotReadyMatchingHash(t *testing.T) {
	ctx := context.Background()
	control := New()

	deployment := getWorkloadDeployment("test-backfill-notready", 2)

	// Instance 0: not-Ready, correct template hash, but missing city-code/workload-name labels.
	instance0 := getInstanceForDeployment(deployment, 0)
	apimeta.SetStatusCondition(&instance0.Status.Conditions, metav1.Condition{
		Type:               v1alpha.InstanceReady,
		Status:             metav1.ConditionFalse,
		Reason:             "NotReady",
		Message:            "Instance is not ready",
		LastTransitionTime: metav1.Now(),
	})
	// Simulate pre-existing instance that only has the index label (missing the newer labels).
	instance0.Labels = map[string]string{
		v1alpha.InstanceIndexLabel: "0",
	}

	// Instance 1: needs to be created (nil in desiredInstances), so we only provide instance0.
	currentInstances := []v1alpha.Instance{*instance0}

	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)

	// Collect actions by type.
	var waitActions, createActions, recreateActions, patchActions []instancecontrol.Action
	for _, a := range actions {
		switch a.ActionType() {
		case instancecontrol.ActionTypeWait:
			waitActions = append(waitActions, a)
		case instancecontrol.ActionTypeCreate:
			createActions = append(createActions, a)
		case instancecontrol.ActionTypeDelete:
			recreateActions = append(recreateActions, a)
		case instancecontrol.ActionTypePatchLabels:
			patchActions = append(patchActions, a)
		}
	}

	// The not-Ready instance must still produce a Wait (rollout is gated).
	assert.Len(t, waitActions, 1, "not-Ready instance must still produce a Wait action")
	assert.Equal(t, "test-backfill-notready-0", waitActions[0].Object.GetName())

	// The missing instance-1 create is skipped (ordered policy, Wait is first).
	assert.Len(t, createActions, 1, "instance-1 create action must be present")
	assert.True(t, createActions[0].IsSkipped(), "create for instance-1 must be skipped while instance-0 is waiting")

	// No rollout recreate actions must be produced.
	assert.Empty(t, recreateActions, "no rollout recreate must be produced for a matching-hash instance")

	// A PatchLabels action must be produced for instance-0.
	assert.Len(t, patchActions, 1, "exactly one PatchLabels action for the label-drifted instance")
	assert.Equal(t, "test-backfill-notready-0", patchActions[0].Object.GetName())
	assert.False(t, patchActions[0].IsSkipped(), "PatchLabels must not be skipped by the rollout skip-loop")

	// The patched object must carry all desired labels.
	patched, ok := patchActions[0].Object.(*v1alpha.Instance)
	assert.True(t, ok)
	assert.Equal(t, deployment.GetName(), patched.Labels[v1alpha.WorkloadDeploymentNameLabel])
	assert.Equal(t, deployment.Spec.CityCode, patched.Labels[v1alpha.CityCodeLabel])
	assert.Equal(t, deployment.Spec.WorkloadRef.Name, patched.Labels[v1alpha.WorkloadNameLabel])
	assert.Equal(t, deployment.Spec.PlacementName, patched.Labels[v1alpha.PlacementNameLabel])

	// The patched object's spec and template-hash must be unchanged.
	assert.Equal(t, instancecontrol.ComputeHash(deployment.Spec.Template), patched.Spec.Controller.TemplateHash,
		"template hash must be unchanged by the label backfill")
	assert.Equal(t, deployment.Spec.Template.Spec.Runtime, patched.Spec.Runtime,
		"spec must be unchanged by the label backfill")
}

// TestLabelBackfill_Idempotent verifies that an instance already carrying all
// correct controller-managed labels produces no PatchLabels action.
func TestLabelBackfill_Idempotent(t *testing.T) {
	ctx := context.Background()
	control := New()

	deployment := getWorkloadDeployment("test-backfill-idempotent", 1)

	// Instance already has all controller-managed labels set correctly.
	instance := getInstanceForDeployment(deployment, 0)
	instance.Labels = map[string]string{
		v1alpha.InstanceIndexLabel:          "0",
		v1alpha.WorkloadUIDLabel:            string(deployment.Spec.WorkloadRef.UID),
		v1alpha.WorkloadDeploymentUIDLabel:  string(deployment.GetUID()),
		v1alpha.WorkloadDeploymentNameLabel: deployment.GetName(),
		v1alpha.CityCodeLabel:               deployment.Spec.CityCode,
		v1alpha.WorkloadNameLabel:           deployment.Spec.WorkloadRef.Name,
		v1alpha.PlacementNameLabel:          deployment.Spec.PlacementName,
	}

	currentInstances := []v1alpha.Instance{*instance}
	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)

	for _, a := range actions {
		assert.NotEqual(t, instancecontrol.ActionTypePatchLabels, a.ActionType(),
			"no PatchLabels action must be produced when all labels are already correct")
	}
}

// TestLabelBackfill_ReadyInstanceCorrected verifies that a Ready instance with
// correct template hash but drifted labels receives a PatchLabels action
// without triggering a rollout recreate.
func TestLabelBackfill_ReadyInstanceCorrected(t *testing.T) {
	ctx := context.Background()
	control := New()

	deployment := getWorkloadDeployment("test-backfill-ready", 1)

	// Ready instance with matching hash but missing city-code label.
	instance := getInstanceForDeployment(deployment, 0)
	// Remove the city-code label to simulate drift.
	delete(instance.Labels, v1alpha.CityCodeLabel)

	currentInstances := []v1alpha.Instance{*instance}
	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)

	var recreateActions, patchActions []instancecontrol.Action
	for _, a := range actions {
		switch a.ActionType() {
		case instancecontrol.ActionTypeDelete:
			recreateActions = append(recreateActions, a)
		case instancecontrol.ActionTypePatchLabels:
			patchActions = append(patchActions, a)
		}
	}

	// No rollout recreate must be produced — template hash matches.
	assert.Empty(t, recreateActions, "no rollout recreate must be produced for a matching-hash ready instance")

	// A PatchLabels action must be produced.
	assert.Len(t, patchActions, 1, "PatchLabels action must be produced for the label-drifted ready instance")
	patched, ok := patchActions[0].Object.(*v1alpha.Instance)
	assert.True(t, ok)
	assert.Equal(t, deployment.Spec.CityCode, patched.Labels[v1alpha.CityCodeLabel],
		"city-code label must be corrected by the backfill")
}

// TestLabelBackfill_DoesNotAffectRollingUpdate verifies that a genuine template
// change on a Ready instance still produces the normal ordered roll (a recreate
// Delete per instance) and that the PatchLabels path does not interfere with or
// duplicate it.
func TestLabelBackfill_DoesNotAffectRollingUpdate(t *testing.T) {
	ctx := context.Background()
	control := New()

	deployment := getWorkloadDeployment("test-backfill-rolling", 2)

	// Two ready instances with all correct labels and matching current hash.
	instance0 := getInstanceForDeployment(deployment, 0)
	instance0.Labels = map[string]string{
		v1alpha.InstanceIndexLabel:          "0",
		v1alpha.WorkloadUIDLabel:            string(deployment.Spec.WorkloadRef.UID),
		v1alpha.WorkloadDeploymentUIDLabel:  string(deployment.GetUID()),
		v1alpha.WorkloadDeploymentNameLabel: deployment.GetName(),
		v1alpha.CityCodeLabel:               deployment.Spec.CityCode,
		v1alpha.WorkloadNameLabel:           deployment.Spec.WorkloadRef.Name,
		v1alpha.PlacementNameLabel:          deployment.Spec.PlacementName,
	}
	instance1 := getInstanceForDeployment(deployment, 1)
	instance1.Labels = map[string]string{
		v1alpha.InstanceIndexLabel:          "1",
		v1alpha.WorkloadUIDLabel:            string(deployment.Spec.WorkloadRef.UID),
		v1alpha.WorkloadDeploymentUIDLabel:  string(deployment.GetUID()),
		v1alpha.WorkloadDeploymentNameLabel: deployment.GetName(),
		v1alpha.CityCodeLabel:               deployment.Spec.CityCode,
		v1alpha.WorkloadNameLabel:           deployment.Spec.WorkloadRef.Name,
		v1alpha.PlacementNameLabel:          deployment.Spec.PlacementName,
	}

	// Trigger a template change.
	deployment.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "rolling-update-image"

	currentInstances := []v1alpha.Instance{*instance0, *instance1}
	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)

	var recreateActions, patchActions []instancecontrol.Action
	for _, a := range actions {
		switch a.ActionType() {
		case instancecontrol.ActionTypeDelete:
			recreateActions = append(recreateActions, a)
		case instancecontrol.ActionTypePatchLabels:
			patchActions = append(patchActions, a)
		}
	}

	// Two recreate (Delete) actions expected (one per instance), ordered highest-to-lowest.
	assert.Len(t, recreateActions, 2, "both instances must produce recreate actions on template change")
	assert.Equal(t, "test-backfill-rolling-1", recreateActions[0].Object.GetName(),
		"recreate actions must be ordered highest ordinal first")
	assert.Equal(t, "test-backfill-rolling-0", recreateActions[1].Object.GetName())
	assert.False(t, recreateActions[0].IsSkipped(), "first recreate must be active")
	assert.True(t, recreateActions[1].IsSkipped(), "second recreate must be skipped (ordered rollout)")

	// No PatchLabels — all labels are already correct.
	assert.Empty(t, patchActions, "no PatchLabels when all labels are already correct")
}

func getWorkloadDeployment(name string, minReplicas int32) *v1alpha.WorkloadDeployment {
	instance := getInstanceTemplate(name, 0)
	deployment := &v1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       "test-wd-uid",
		},
		Spec: v1alpha.WorkloadDeploymentSpec{
			WorkloadRef: v1alpha.WorkloadReference{
				Name: "test-workload",
				UID:  "test-workload-uid",
			},
			PlacementName: "test-placement",
			CityCode:      "DFW",
			ScaleSettings: v1alpha.HorizontalScaleSettings{
				MinReplicas:              minReplicas,
				InstanceManagementPolicy: v1alpha.OrderedReadyInstanceManagementPolicyType,
			},
			Template: v1alpha.InstanceTemplateSpec{
				ObjectMeta: instance.ObjectMeta,
				Spec:       instance.Spec,
			},
		},
	}

	return deployment
}

func getInstanceForDeployment(deployment *v1alpha.WorkloadDeployment, ordinal int) *v1alpha.Instance {
	instance := getInstance(deployment.Name, ordinal)
	instance.Spec.Controller = &v1alpha.InstanceController{
		TemplateHash: instancecontrol.ComputeHash(deployment.Spec.Template),
	}

	// Stamp all controller-managed labels so that the label-backfill path is a
	// no-op for instances built by this helper. Tests that specifically exercise
	// label drift should manipulate the labels directly after calling this helper.
	if instance.Labels == nil {
		instance.Labels = map[string]string{}
	}
	instance.Labels[v1alpha.InstanceIndexLabel] = strconv.Itoa(ordinal)
	instance.Labels[v1alpha.WorkloadUIDLabel] = string(deployment.Spec.WorkloadRef.UID)
	instance.Labels[v1alpha.WorkloadDeploymentUIDLabel] = string(deployment.GetUID())
	instance.Labels[v1alpha.WorkloadDeploymentNameLabel] = deployment.GetName()
	instance.Labels[v1alpha.CityCodeLabel] = deployment.Spec.CityCode
	instance.Labels[v1alpha.WorkloadNameLabel] = deployment.Spec.WorkloadRef.Name
	instance.Labels[v1alpha.PlacementNameLabel] = deployment.Spec.PlacementName

	return instance
}

func getInstance(name string, ordinal int) *v1alpha.Instance {
	instance := getInstanceTemplate(name, ordinal)
	instance.CreationTimestamp = metav1.Now()
	instance.Labels = map[string]string{
		v1alpha.InstanceIndexLabel: strconv.Itoa(ordinal),
	}

	instance.Status = v1alpha.InstanceStatus{
		Conditions: []metav1.Condition{
			{
				Type:               v1alpha.InstanceReady,
				Status:             metav1.ConditionTrue,
				Reason:             "Ready",
				Message:            "Instance is ready",
				LastTransitionTime: metav1.Now(),
			},
		},
	}

	return instance
}

func getInstanceTemplate(name string, ordinal int) *v1alpha.Instance {
	instanceName := fmt.Sprintf("%s-%d", name, ordinal)
	instance := &v1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName,
			Namespace: "default",
		},
		Spec: v1alpha.InstanceSpec{
			Runtime: v1alpha.InstanceRuntimeSpec{
				Resources: v1alpha.InstanceRuntimeResources{
					InstanceType: "datumcloud/d1-standard-2",
				},
				Sandbox: &v1alpha.SandboxRuntime{
					Containers: []v1alpha.SandboxContainer{
						{
							Name:  "test",
							Image: "test",
						},
					},
				},
			},
		},
	}

	return instance
}
