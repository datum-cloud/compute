package stateful

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

// These class names are intentionally not names the platform ships. The label
// must copy whatever class the deployment resolved. A shipped name would let a
// test pass even if the control plane re-derived the name itself.
const (
	testClassAzurite = "azurite"
	testClassBasalt  = "basalt"
)

// TestInstanceLabels_RuntimeClassStamped verifies that an instance carries the
// runtime class its deployment resolved. Providers select instances by this
// label, so any other value routes the instance to the wrong provider.
func TestInstanceLabels_RuntimeClassStamped(t *testing.T) {
	tests := []struct {
		name      string
		specClass string
		wantLabel string
	}{
		{
			name:      "a resolved class is stamped",
			specClass: testClassAzurite,
			wantLabel: testClassAzurite,
		},
		{
			name:      "a different resolved class is stamped",
			specClass: testClassBasalt,
			wantLabel: testClassBasalt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := createdInstance(t, tt.specClass)
			assert.Equal(t, tt.wantLabel, instance.Labels[v1alpha.RuntimeClassLabel])
		})
	}
}

// TestInstanceLabels_RuntimeClassAbsentWhenUnresolved verifies that an instance
// with no resolved runtime class carries no class label. The control plane must
// not supply a class the catalog never assigned, because a class-selecting
// provider would then claim an instance it does not serve.
func TestInstanceLabels_RuntimeClassAbsentWhenUnresolved(t *testing.T) {
	instance := createdInstance(t, "")

	_, stamped := instance.Labels[v1alpha.RuntimeClassLabel]
	assert.False(t, stamped, "an instance with no resolved class must carry no class label")
}

// TestInstanceLabels_PreClassesSetIsUnchanged verifies the exact label set on
// an instance whose deployment resolved no runtime class. Providers scope their
// informer cache by label selector, so adding or removing a key changes which
// instances a running provider owns. An unowned instance never starts and never
// finishes terminating.
func TestInstanceLabels_PreClassesSetIsUnchanged(t *testing.T) {
	deployment := getWorkloadDeployment("test-pre-classes-labels", 1)

	assert.Equal(t, map[string]string{
		v1alpha.InstanceIndexLabel:          "0",
		v1alpha.WorkloadUIDLabel:            "test-workload-uid",
		v1alpha.WorkloadDeploymentUIDLabel:  "test-wd-uid",
		v1alpha.WorkloadDeploymentNameLabel: "test-pre-classes-labels",
		v1alpha.CityCodeLabel:               "DFW",
		v1alpha.WorkloadNameLabel:           "test-workload",
		v1alpha.PlacementNameLabel:          "test-placement",
		labelServiceKey:                     labelServiceValue,
	}, desiredControllerLabels(0, deployment))
}

// TestInstanceLabels_RuntimeClassBackfilled verifies that an instance missing
// the runtime class label is reported as needing a backfill. Providers select
// on the label, so an instance without one is never claimed.
func TestInstanceLabels_RuntimeClassBackfilled(t *testing.T) {
	deployment := getWorkloadDeployment("test-runtime-class-backfill", 1)
	deployment.Spec.Template.Spec.Runtime.Class = testClassAzurite
	desired := desiredControllerLabels(0, deployment)

	current := map[string]string{}
	for k, v := range desired {
		current[k] = v
	}
	delete(current, v1alpha.RuntimeClassLabel)

	assert.True(t, labelsNeedBackfill(current, desired))
}

func createdInstance(t *testing.T, specClass string) *v1alpha.Instance {
	t.Helper()

	deployment := getWorkloadDeployment("test-runtime-class-label", 1)
	deployment.Spec.Template.Spec.Runtime.Class = specClass

	actions, err := New().GetActions(context.Background(), scheme, deployment,
		deployment.Spec.ScaleSettings.MinReplicas, nil)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	require.Equal(t, instancecontrol.ActionTypeCreate, actions[0].ActionType())

	instance, ok := actions[0].Object.(*v1alpha.Instance)
	require.True(t, ok)
	return instance
}
