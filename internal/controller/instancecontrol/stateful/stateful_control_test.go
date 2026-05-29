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
	control := New()

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

func TestUpdateWithAllReadyInstances(t *testing.T) {
	ctx := context.Background()
	control := New()

	deployment := getWorkloadDeployment("test-deploy", 2)

	var currentInstances []v1alpha.Instance
	currentInstances = append(currentInstances, *getInstanceForDeployment(deployment, 0))
	currentInstances = append(currentInstances, *getInstanceForDeployment(deployment, 1))

	deployment.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "test-image-update"

	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)
	assert.Len(t, actions, 2)

	assert.Equal(t, "test-deploy-1", actions[0].Object.GetName())
	assert.Equal(t, instancecontrol.ActionTypeUpdate, actions[0].ActionType())
	assert.False(t, actions[0].IsSkipped())

	assert.Equal(t, "test-deploy-0", actions[1].Object.GetName())
	assert.Equal(t, instancecontrol.ActionTypeUpdate, actions[1].ActionType())
	assert.True(t, actions[1].IsSkipped())
}

func TestScaleUpWithNotReadyInstance(t *testing.T) {
	ctx := context.Background()
	control := New()

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
	control := New()

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
	control := New()

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

// TestInstanceLabels_PropagatedOnUpdate verifies that when an existing instance
// is updated (rolling update path), the four new labels are refreshed from the
// deployment so they remain accurate after spec changes.
func TestInstanceLabels_PropagatedOnUpdate(t *testing.T) {
	ctx := context.Background()
	control := New()

	deployment := getWorkloadDeployment("test-labels-update", 1)

	// Build a ready existing instance.
	currentInstances := []v1alpha.Instance{*getInstanceForDeployment(deployment, 0)}

	// Trigger a rolling update by changing the image.
	deployment.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "updated-image"

	actions, err := control.GetActions(ctx, scheme, deployment, currentInstances)

	assert.NoError(t, err)
	assert.Len(t, actions, 1)
	assert.Equal(t, instancecontrol.ActionTypeUpdate, actions[0].ActionType())

	instance, ok := actions[0].Object.(*v1alpha.Instance)
	assert.True(t, ok)

	assert.Equal(t, deployment.GetName(), instance.Labels[v1alpha.WorkloadDeploymentNameLabel],
		"WorkloadDeploymentNameLabel must be refreshed on update")
	assert.Equal(t, deployment.Spec.CityCode, instance.Labels[v1alpha.CityCodeLabel],
		"CityCodeLabel must be refreshed on update")
	assert.Equal(t, deployment.Spec.WorkloadRef.Name, instance.Labels[v1alpha.WorkloadNameLabel],
		"WorkloadNameLabel must be refreshed on update")
	assert.Equal(t, deployment.Spec.PlacementName, instance.Labels[v1alpha.PlacementNameLabel],
		"PlacementNameLabel must be refreshed on update")
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
