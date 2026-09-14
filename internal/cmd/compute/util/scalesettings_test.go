package util

import (
	"testing"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	maxTenFlag       = "--max=10"
	cpu70PercentFlag = "--cpu-percent=70"
)

func resourceMetric(name corev1.ResourceName, percent int32) computev1alpha.MetricSpec {
	return computev1alpha.MetricSpec{
		Resource: &computev1alpha.ResourceMetricSource{
			Name:   name,
			Target: computev1alpha.MetricTarget{AverageUtilization: new(percent)},
		},
	}
}

func TestMergeScaleSettings(t *testing.T) {
	tests := []struct {
		name        string
		current     computev1alpha.HorizontalScaleSettings
		args        []string
		wantErr     bool
		wantMin     int32
		wantMax     *int32
		wantMetrics map[corev1.ResourceName]int32
	}{
		{
			name:    "min only leaves autoscaling untouched",
			current: computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
			args:    []string{"--min=4"},
			wantMin: 4,
			wantMax: nil,
		},
		{
			name:    "max without a metric is rejected",
			current: computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
			args:    []string{maxTenFlag},
			wantErr: true,
		},
		{
			name:    "metric without max is rejected",
			current: computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
			args:    []string{cpu70PercentFlag},
			wantErr: true,
		},
		{
			name:        "max and cpu-percent together enable autoscaling",
			current:     computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
			args:        []string{maxTenFlag, cpu70PercentFlag},
			wantMin:     1,
			wantMax:     new(int32(10)),
			wantMetrics: map[corev1.ResourceName]int32{corev1.ResourceCPU: 70},
		},
		{
			name: "setting max alone succeeds when a metric already exists",
			current: computev1alpha.HorizontalScaleSettings{
				MinReplicas: 1,
				MaxReplicas: new(int32(5)),
				Metrics:     []computev1alpha.MetricSpec{resourceMetric(corev1.ResourceCPU, 70)},
			},
			args:        []string{maxTenFlag},
			wantMin:     1,
			wantMax:     new(int32(10)),
			wantMetrics: map[corev1.ResourceName]int32{corev1.ResourceCPU: 70},
		},
		{
			name: "setting a metric alone succeeds when max already exists",
			current: computev1alpha.HorizontalScaleSettings{
				MinReplicas: 1,
				MaxReplicas: new(int32(5)),
			},
			args:        []string{cpu70PercentFlag},
			wantMin:     1,
			wantMax:     new(int32(5)),
			wantMetrics: map[corev1.ResourceName]int32{corev1.ResourceCPU: 70},
		},
		{
			name: "max=0 disables autoscaling",
			current: computev1alpha.HorizontalScaleSettings{
				MinReplicas: 1,
				MaxReplicas: new(int32(5)),
				Metrics:     []computev1alpha.MetricSpec{resourceMetric(corev1.ResourceCPU, 70)},
			},
			args:        []string{"--max=0"},
			wantMin:     1,
			wantMax:     nil,
			wantMetrics: map[corev1.ResourceName]int32{},
		},
		{
			name: "cpu-percent=0 removes only the cpu metric",
			current: computev1alpha.HorizontalScaleSettings{
				MinReplicas: 1,
				MaxReplicas: new(int32(5)),
				Metrics: []computev1alpha.MetricSpec{
					resourceMetric(corev1.ResourceCPU, 70),
					resourceMetric(corev1.ResourceMemory, 80),
				},
			},
			args:        []string{"--cpu-percent=0"},
			wantMin:     1,
			wantMax:     new(int32(5)),
			wantMetrics: map[corev1.ResourceName]int32{corev1.ResourceMemory: 80},
		},
		{
			name:    "max below min is rejected",
			current: computev1alpha.HorizontalScaleSettings{MinReplicas: 5},
			args:    []string{"--max=3", cpu70PercentFlag},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var min, max, cpuPercent, memoryPercent int32
			cmd := &cobra.Command{}
			AddScaleFlags(cmd, &min, &max, &cpuPercent, &memoryPercent, 1)
			if err := cmd.ParseFlags(tt.args); err != nil {
				t.Fatalf("parsing flags %v: %v", tt.args, err)
			}

			got, err := MergeScaleSettings(cmd, tt.current, min, max, cpuPercent, memoryPercent)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.MinReplicas != tt.wantMin {
				t.Errorf("MinReplicas = %d, want %d", got.MinReplicas, tt.wantMin)
			}
			if (got.MaxReplicas == nil) != (tt.wantMax == nil) {
				t.Fatalf("MaxReplicas = %v, want %v", got.MaxReplicas, tt.wantMax)
			}
			if got.MaxReplicas != nil && *got.MaxReplicas != *tt.wantMax {
				t.Errorf("MaxReplicas = %d, want %d", *got.MaxReplicas, *tt.wantMax)
			}

			gotMetrics := map[corev1.ResourceName]int32{}
			for _, m := range got.Metrics {
				gotMetrics[m.Resource.Name] = *m.Resource.Target.AverageUtilization
			}
			if tt.wantMetrics != nil {
				if len(gotMetrics) != len(tt.wantMetrics) {
					t.Fatalf("Metrics = %+v, want %+v", gotMetrics, tt.wantMetrics)
				}
				for name, percent := range tt.wantMetrics {
					if gotMetrics[name] != percent {
						t.Errorf("Metrics[%s] = %d, want %d", name, gotMetrics[name], percent)
					}
				}
			}
		})
	}
}
