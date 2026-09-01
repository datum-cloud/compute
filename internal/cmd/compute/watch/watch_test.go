// SPDX-License-Identifier: AGPL-3.0-only

package watch

import (
	"testing"
	"time"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func TestResolveDesired(t *testing.T) {
	tests := []struct {
		name            string
		statusDesired   int32
		specMinReplicas int32
		want            int32
	}{
		{
			name:            "unreconciled status falls back to spec min",
			statusDesired:   0,
			specMinReplicas: 1,
			want:            1,
		},
		{
			name:            "genuine scale-to-zero",
			statusDesired:   0,
			specMinReplicas: 0,
			want:            0,
		},
		{
			name:            "controller desired above min (e.g. autoscaled)",
			statusDesired:   3,
			specMinReplicas: 1,
			want:            3,
		},
		{
			// Once the controller has spoken, trust its value even if below spec min.
			name:            "controller desired below min — trust controller",
			statusDesired:   1,
			specMinReplicas: 2,
			want:            1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := computev1alpha.WorkloadDeployment{}
			d.Status.DesiredReplicas = tc.statusDesired
			d.Spec.ScaleSettings.MinReplicas = tc.specMinReplicas

			got := resolveDesired(d)
			if got != tc.want {
				t.Errorf("resolveDesired() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestComputePhase(t *testing.T) {
	tests := []struct {
		name     string
		desired  int32
		ready    int32
		current  int32
		replicas int32
		want     deploymentPhase
	}{
		{
			name:     "zero desired is Done",
			desired:  0,
			ready:    0,
			current:  0,
			replicas: 0,
			want:     phaseDone,
		},
		{
			name:     "fresh create with no instances yet is Pending",
			desired:  1,
			ready:    0,
			current:  0,
			replicas: 0,
			want:     phasePending,
		},
		{
			name:     "instance scheduled but not ready is Updating",
			desired:  1,
			ready:    0,
			current:  1,
			replicas: 1,
			want:     phaseUpdating,
		},
		{
			name:     "single instance ready is Done",
			desired:  1,
			ready:    1,
			current:  1,
			replicas: 1,
			want:     phaseDone,
		},
		{
			// OLD replicas still draining after scale-down must not report Done.
			name:     "scale-down with old replicas still draining is Updating",
			desired:  1,
			ready:    1,
			current:  1,
			replicas: 5,
			want:     phaseUpdating,
		},
		{
			name:     "partial readiness is Updating",
			desired:  3,
			ready:    1,
			current:  2,
			replicas: 3,
			want:     phaseUpdating,
		},
		{
			name:     "all replicas ready is Done",
			desired:  2,
			ready:    2,
			current:  2,
			replicas: 2,
			want:     phaseDone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computePhase(tc.desired, tc.ready, tc.current, tc.replicas)
			if got != tc.want {
				t.Errorf("computePhase(%d, %d, %d, %d) = %q, want %q",
					tc.desired, tc.ready, tc.current, tc.replicas, got, tc.want)
			}
		})
	}
}

func TestUpdateDeploymentState(t *testing.T) {
	const key = "DFW"

	makeDeployment := func() computev1alpha.WorkloadDeployment {
		var d computev1alpha.WorkloadDeployment
		d.Spec.PlacementName = "default"
		d.Spec.LocationRef.Name = key
		return d
	}

	t.Run("stalled updating for 40s is promoted to Blocked", func(t *testing.T) {
		states := map[string]*deploymentState{}
		stalledAt := time.Now().Add(-40 * time.Second)
		states[key] = &deploymentState{
			phase:        phaseUpdating,
			ready:        1,
			current:      2,
			stalledSince: stalledAt,
		}

		got := updateDeploymentState(states, key, makeDeployment(), true, states[key], 3, 1, 2, phaseUpdating)

		if got != phaseBlocked {
			t.Errorf("updateDeploymentState() phase = %q, want %q", got, phaseBlocked)
		}
		if states[key].phase != phaseBlocked {
			t.Errorf("states[key].phase = %q, want %q", states[key].phase, phaseBlocked)
		}
	})

	t.Run("first observation of Updating is not yet Blocked", func(t *testing.T) {
		states := map[string]*deploymentState{}

		got := updateDeploymentState(states, key, makeDeployment(), false, nil, 3, 1, 2, phaseUpdating)

		if got != phaseUpdating {
			t.Errorf("updateDeploymentState() phase = %q, want %q", got, phaseUpdating)
		}
		if states[key].phase != phaseUpdating {
			t.Errorf("states[key].phase = %q, want %q", states[key].phase, phaseUpdating)
		}
	})
}
