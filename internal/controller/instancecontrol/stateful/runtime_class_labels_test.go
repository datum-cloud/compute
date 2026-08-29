package stateful

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

// TestInstanceLabels_RuntimeClassStamped asserts every created instance states
// the class it runs in, including when the spec left the class unset. A
// provider claims instances by this label, so an unlabeled instance is one no
// provider owns.
func TestInstanceLabels_RuntimeClassStamped(t *testing.T) {
	tests := []struct {
		name      string
		specClass string
		wantLabel string
	}{
		{
			name:      "class unset — labeled with the class served before classes existed",
			specClass: "",
			wantLabel: v1alpha.DefaultRuntimeClass,
		},
		{
			name:      "fast path selected",
			specClass: v1alpha.RuntimeClassUnikernel,
			wantLabel: v1alpha.RuntimeClassUnikernel,
		},
		{
			name:      "general purpose selected",
			specClass: v1alpha.RuntimeClassGeneralPurpose,
			wantLabel: v1alpha.RuntimeClassGeneralPurpose,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployment := getWorkloadDeployment("test-runtime-class-label", 1)
			deployment.Spec.Template.Spec.Runtime.Class = tt.specClass

			actions, err := New().GetActions(context.Background(), scheme, deployment,
				deployment.Spec.ScaleSettings.MinReplicas, nil)
			require.NoError(t, err)
			require.Len(t, actions, 1)
			require.Equal(t, instancecontrol.ActionTypeCreate, actions[0].ActionType())

			instance, ok := actions[0].Object.(*v1alpha.Instance)
			require.True(t, ok)
			assert.Equal(t, tt.wantLabel, instance.Labels[v1alpha.RuntimeClassLabel])
		})
	}
}

// TestInstanceLabels_RuntimeClassBackfilled asserts an instance created before
// the class label existed is recognized as needing it. Providers select on the
// label, so an instance left without one is never claimed.
func TestInstanceLabels_RuntimeClassBackfilled(t *testing.T) {
	deployment := getWorkloadDeployment("test-runtime-class-backfill", 1)
	desired := desiredControllerLabels(0, deployment)

	current := map[string]string{}
	for k, v := range desired {
		current[k] = v
	}
	delete(current, v1alpha.RuntimeClassLabel)

	assert.True(t, labelsNeedBackfill(current, desired))
}
