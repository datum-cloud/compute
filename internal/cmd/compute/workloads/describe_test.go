// SPDX-License-Identifier: AGPL-3.0-only

package workloads

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func TestAutoscaleAnnotation(t *testing.T) {
	tests := []struct {
		name string
		s    computev1alpha.HorizontalScaleSettings
		want string
	}{
		{
			name: "no max, no annotation",
			s:    computev1alpha.HorizontalScaleSettings{MinReplicas: 2},
			want: "",
		},
		{
			name: "max with no metric is flagged as disabled",
			s:    computev1alpha.HorizontalScaleSettings{MinReplicas: 2, MaxReplicas: new(int32(10))},
			want: " (autoscaling disabled)",
		},
		{
			name: "single cpu utilization metric",
			s: computev1alpha.HorizontalScaleSettings{
				MinReplicas: 2,
				MaxReplicas: new(int32(10)),
				Metrics: []computev1alpha.MetricSpec{
					{Resource: &computev1alpha.ResourceMetricSource{
						Name:   corev1.ResourceCPU,
						Target: computev1alpha.MetricTarget{AverageUtilization: new(int32(70))},
					}},
				},
			},
			want: " (cpu@70%)",
		},
		{
			name: "multiple metrics joined in order",
			s: computev1alpha.HorizontalScaleSettings{
				MinReplicas: 2,
				MaxReplicas: new(int32(10)),
				Metrics: []computev1alpha.MetricSpec{
					{Resource: &computev1alpha.ResourceMetricSource{
						Name:   corev1.ResourceCPU,
						Target: computev1alpha.MetricTarget{AverageUtilization: new(int32(70))},
					}},
					{Resource: &computev1alpha.ResourceMetricSource{
						Name:   corev1.ResourceMemory,
						Target: computev1alpha.MetricTarget{AverageUtilization: new(int32(80))},
					}},
				},
			},
			want: " (cpu@70%, memory@80%)",
		},
		{
			name: "average value target",
			s: computev1alpha.HorizontalScaleSettings{
				MinReplicas: 2,
				MaxReplicas: new(int32(10)),
				Metrics: []computev1alpha.MetricSpec{
					{Resource: &computev1alpha.ResourceMetricSource{
						Name:   corev1.ResourceMemory,
						Target: computev1alpha.MetricTarget{AverageValue: new(resource.MustParse("500Mi"))},
					}},
				},
			},
			want: " (memory@500Mi avg)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := autoscaleAnnotation(tt.s); got != tt.want {
				t.Errorf("autoscaleAnnotation() = %q, want %q", got, tt.want)
			}
		})
	}
}
