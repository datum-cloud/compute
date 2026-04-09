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

// Add more test functions below for different scenarios.

func getWorkloadDeployment(name string, minReplicas int32) *v1alpha.WorkloadDeployment {
	instance := getInstanceTemplate(name, 0)
	deployment := &v1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: v1alpha.WorkloadDeploymentSpec{
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
