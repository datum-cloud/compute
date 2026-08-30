package stateful

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

// The class names here are invented, and deliberately not the ones the platform
// ships. Stamping the class must copy whatever the deployment resolved, so a
// test built on the shipped names could not tell that apart from this plane
// re-deriving a name it knows.
const (
	testClassAzurite = "azurite"
	testClassBasalt  = "basalt"
)

// TestInstanceLabels_RuntimeClassStamped asserts an instance states the class
// its deployment resolved, and only that class. A provider claims instances by
// this label, so a label carrying anything other than the resolved class hands
// the instance to the wrong provider.
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

// TestInstanceLabels_RuntimeClassAbsentWhenUnresolved pins the behavior of a
// control plane where runtime class selection has never been enabled: nothing
// resolves a class, and the instance carries no class label, exactly as it did
// before classes existed. Stamping a name here would be this plane inventing a
// tier the catalog never assigned, and would hand the instance to a
// class-selecting provider that may not be the one serving it.
func TestInstanceLabels_RuntimeClassAbsentWhenUnresolved(t *testing.T) {
	instance := createdInstance(t, "")

	_, stamped := instance.Labels[v1alpha.RuntimeClassLabel]
	assert.False(t, stamped, "an instance with no resolved class must carry no class label")
}

// TestInstanceLabels_PreClassesSetIsUnchanged pins the exact set of labels an
// instance carries where no class was ever resolved, which is every instance on
// a control plane that has not enabled runtime classes. Providers scope their
// informer cache by label selector, so a key that appears or disappears here
// silently changes which instances a running provider owns — and an un-owned
// instance never starts and never finishes terminating.
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

// TestInstanceLabels_RuntimeClassBackfilled asserts an instance created before
// the class label existed is recognized as needing it. Providers select on the
// label, so an instance left without one is never claimed.
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
