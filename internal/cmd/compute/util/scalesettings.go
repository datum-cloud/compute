package util

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// AddScaleFlags registers the --min/--max/--cpu-percent/--memory-percent flags
// shared by "deploy" and "scale" on cmd, backed by the given variables.
func AddScaleFlags(cmd *cobra.Command, min, max, cpuPercent, memoryPercent *int32, minDefault int32) {
	cmd.Flags().Int32Var(min, "min", minDefault, "Minimum number of instances per location")
	cmd.Flags().Int32Var(max, "max", 0, "Maximum number of instances to autoscale up to (0 disables autoscaling)")
	cmd.Flags().Int32Var(cpuPercent, "cpu-percent", 0, "Target average CPU utilization percentage to autoscale on (0 removes the metric)")
	cmd.Flags().Int32Var(memoryPercent, "memory-percent", 0, "Target average memory utilization percentage to autoscale on (0 removes the metric)")
}

// MergeScaleSettings applies the scale-related flags that were explicitly set
// on cmd onto current, leaving any untouched fields as-is, and returns the
// resulting settings once validated.
func MergeScaleSettings(
	cmd *cobra.Command,
	current computev1alpha.HorizontalScaleSettings,
	min, max, cpuPercent, memoryPercent int32,
) (computev1alpha.HorizontalScaleSettings, error) {
	flags := cmd.Flags()
	settings := *current.DeepCopy()

	if flags.Changed("min") {
		settings.MinReplicas = min
	}

	if flags.Changed("max") {
		if max == 0 {
			settings.MaxReplicas = nil
			settings.Metrics = nil
		} else {
			settings.MaxReplicas = &max
		}
	}

	if flags.Changed("cpu-percent") {
		setResourceMetricPercent(&settings, corev1.ResourceCPU, cpuPercent)
	}

	if flags.Changed("memory-percent") {
		setResourceMetricPercent(&settings, corev1.ResourceMemory, memoryPercent)
	}

	if settings.InstanceManagementPolicy == "" {
		settings.InstanceManagementPolicy = computev1alpha.OrderedReadyInstanceManagementPolicyType
	}

	if err := validateScaleSettings(settings); err != nil {
		return settings, err
	}

	return settings, nil
}

// FormatScaleSettings renders a HorizontalScaleSettings for CLI output, e.g.
// "min=2, max=10 (cpu@70%)", or "min=2" when no max is set.
func FormatScaleSettings(s computev1alpha.HorizontalScaleSettings) string {
	out := fmt.Sprintf("min=%d", s.MinReplicas)
	if s.MaxReplicas == nil {
		return out
	}
	out += fmt.Sprintf(", max=%d", *s.MaxReplicas)
	if ann := MetricsAnnotation(s.Metrics); ann != "" {
		out += " " + ann
	} else {
		out += " (autoscaling disabled)"
	}
	return out
}

// MetricsAnnotation renders a placement's autoscaling metrics as a
// parenthesized, human-readable list, e.g. "(cpu@70%, memory@80%)", or ""
// when there are none.
func MetricsAnnotation(metrics []computev1alpha.MetricSpec) string {
	var parts []string
	for _, m := range metrics {
		if m.Resource == nil {
			continue
		}
		target := m.Resource.Target
		switch {
		case target.AverageUtilization != nil:
			parts = append(parts, fmt.Sprintf("%s@%d%%", m.Resource.Name, *target.AverageUtilization))
		case target.AverageValue != nil:
			parts = append(parts, fmt.Sprintf("%s@%s avg", m.Resource.Name, target.AverageValue.String()))
		case target.Value != nil:
			parts = append(parts, fmt.Sprintf("%s@%s", m.Resource.Name, target.Value.String()))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// setResourceMetricPercent sets, replaces, or (when percent is 0) removes the
// resource metric with the given name in settings.Metrics.
func setResourceMetricPercent(settings *computev1alpha.HorizontalScaleSettings, name corev1.ResourceName, percent int32) {
	filtered := settings.Metrics[:0]
	for _, m := range settings.Metrics {
		if m.Resource == nil || m.Resource.Name != name {
			filtered = append(filtered, m)
		}
	}
	settings.Metrics = filtered

	if percent == 0 {
		return
	}

	settings.Metrics = append(settings.Metrics, computev1alpha.MetricSpec{
		Resource: &computev1alpha.ResourceMetricSource{
			Name: name,
			Target: computev1alpha.MetricTarget{
				AverageUtilization: &percent,
			},
		},
	})
}

// validateScaleSettings enforces the invariants the HPA controller relies on:
// autoscaling is enabled iff both a max replica count and at least one metric
// are set, and the max must not be below the min.
func validateScaleSettings(settings computev1alpha.HorizontalScaleSettings) error {
	hasMax := settings.MaxReplicas != nil
	hasMetrics := len(settings.Metrics) > 0

	if hasMax != hasMetrics {
		if hasMax {
			return fmt.Errorf("--max requires at least one of --cpu-percent or --memory-percent to enable autoscaling")
		}
		return fmt.Errorf("--cpu-percent/--memory-percent require --max to enable autoscaling")
	}

	if hasMax && *settings.MaxReplicas < settings.MinReplicas {
		return fmt.Errorf("--max (%d) must be >= --min (%d)", *settings.MaxReplicas, settings.MinReplicas)
	}

	return nil
}
