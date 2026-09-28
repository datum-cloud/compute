// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"bytes"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func placementWithScale(name string, s computev1alpha.HorizontalScaleSettings) computev1alpha.WorkloadPlacement {
	return computev1alpha.WorkloadPlacement{Name: name, ScaleSettings: s}
}

// TestResolveScaleSettings pins the merge baseline: the single existing
// placement's settings (any name), a fresh default with none, and a warning
// — not a silent drop — when several placements collapse into one.
func TestResolveScaleSettings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		existing   []computev1alpha.WorkloadPlacement
		wantErr    string
		wantMin    int32
		wantMax    *int32
		wantNote   string // substring expected in the printed plan output
		wantNoNote bool
	}{
		{
			name:     "no existing placements defaults to min=1",
			args:     nil,
			existing: nil,
			wantMin:  1,
		},
		{
			name:     "a single placement under any name is the baseline",
			args:     nil,
			existing: []computev1alpha.WorkloadPlacement{placementWithScale("us", computev1alpha.HorizontalScaleSettings{MinReplicas: 3})},
			wantMin:  3,
		},
		{
			name: "flags override the single placement's baseline",
			args: []string{"--min=5"},
			existing: []computev1alpha.WorkloadPlacement{
				placementWithScale("default", computev1alpha.HorizontalScaleSettings{MinReplicas: 3}),
			},
			wantMin: 5,
		},
		{
			name: "autoscaling on the single placement survives an unrelated flag change",
			args: []string{"--min=2"},
			existing: []computev1alpha.WorkloadPlacement{
				placementWithScale("default", computev1alpha.HorizontalScaleSettings{
					MinReplicas: 1,
					MaxReplicas: new(int32(10)),
					Metrics: []computev1alpha.MetricSpec{
						{Resource: &computev1alpha.ResourceMetricSource{Name: corev1.ResourceCPU, Target: computev1alpha.MetricTarget{AverageUtilization: new(int32(70))}}},
					},
				}),
			},
			wantMin: 2,
			wantMax: new(int32(10)),
		},
		{
			name: "multiple placements warn before their autoscaling is dropped",
			args: nil,
			existing: []computev1alpha.WorkloadPlacement{
				placementWithScale("us", computev1alpha.HorizontalScaleSettings{MinReplicas: 2}),
				placementWithScale("eu", computev1alpha.HorizontalScaleSettings{MinReplicas: 2, MaxReplicas: new(int32(8))}),
			},
			wantMin:  1, // falls back to the zero-value default, not either placement's
			wantNote: `replacing 2 placements with one; placement "eu"'s autoscaling settings will be dropped`,
		},
		{
			name: "multiple placements with no autoscaling print no warning",
			args: nil,
			existing: []computev1alpha.WorkloadPlacement{
				placementWithScale("us", computev1alpha.HorizontalScaleSettings{MinReplicas: 2}),
				placementWithScale("eu", computev1alpha.HorizontalScaleSettings{MinReplicas: 2}),
			},
			wantMin:    1,
			wantNoNote: true,
		},
		{
			name:     "max without a metric is rejected",
			args:     []string{"--max=10"},
			existing: nil,
			wantErr:  "requires at least one of --cpu-percent or --memory-percent",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, opts := command()
			if err := cmd.Flags().Parse(append([]string{testWorkload, imageFlag}, tc.args...)); err != nil {
				t.Fatalf("parsing flags: %v", err)
			}

			var out bytes.Buffer
			got, err := resolveScaleSettings(cmd, &out, opts, tc.existing)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want an error containing %q, got settings %+v", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.MinReplicas != tc.wantMin {
				t.Errorf("MinReplicas = %d, want %d", got.MinReplicas, tc.wantMin)
			}
			if (got.MaxReplicas == nil) != (tc.wantMax == nil) {
				t.Fatalf("MaxReplicas = %v, want %v", got.MaxReplicas, tc.wantMax)
			}
			if got.MaxReplicas != nil && *got.MaxReplicas != *tc.wantMax {
				t.Errorf("MaxReplicas = %d, want %d", *got.MaxReplicas, *tc.wantMax)
			}

			printed := out.String()
			if tc.wantNote != "" && !strings.Contains(printed, tc.wantNote) {
				t.Errorf("output = %q, want it to contain %q", printed, tc.wantNote)
			}
			if tc.wantNoNote && printed != "" {
				t.Errorf("output = %q, want no warning printed", printed)
			}
		})
	}
}
