// SPDX-License-Identifier: AGPL-3.0-only

package watch

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

func TestRolloutStopsWhenCredentialsAreRefused(t *testing.T) {
	pollInterval = 10 * time.Millisecond
	t.Cleanup(func() { pollInterval = 2 * time.Second })

	scheme := runtime.NewScheme()
	if err := computev1alpha.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		refuse error
	}{
		{"token expired", k8serrors.NewUnauthorized("jwt token is not active")},
		{"access revoked", k8serrors.NewForbidden(schema.GroupResource{Resource: "workloaddeployments"}, "", errors.New("denied"))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var lists int
			c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					lists++
					if lists == 1 {
						return c.List(ctx, list, opts...)
					}
					return tc.refuse
				},
			}).Build()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			var out bytes.Buffer
			err := Rollout(ctx, c, &out, "p", types.UID("uid"))
			if ctx.Err() != nil {
				t.Fatal("watch kept polling after the credentials were refused")
			}
			if err == nil || !strings.Contains(err.Error(), "datumctl login") {
				t.Fatalf("Rollout() error = %v, want one telling the user to sign in again", err)
			}
			if lists != 2 {
				t.Errorf("List called %d times, want 2", lists)
			}
		})
	}
}
