package validation

import (
	"context"
	"slices"

	k8scorev1 "k8s.io/api/core/v1"
	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Great reference:
//   https://github.com/kubernetes/kubernetes/blob/master/pkg/apis/core/validation/validation.go

func ValidateWorkloadCreate(w *computev1alpha.Workload, opts WorkloadValidationOptions) field.ErrorList {
	allErrs := field.ErrorList{}

	// allErrs = append(allErrs, validateWorkloadMetadata(w)...)
	allErrs = append(allErrs, validateWorkloadSpec(w.Spec, opts)...)

	return allErrs
}

type WorkloadValidationOptions struct {
	Client           client.Client
	AdmissionRequest admission.Request
	Context          context.Context
	Workload         *computev1alpha.Workload
	ValidCityCodes   []string
}

func validateWorkloadSpec(spec computev1alpha.WorkloadSpec, opts WorkloadValidationOptions) field.ErrorList {
	allErrs := field.ErrorList{}

	specPath := field.NewPath("spec")

	allErrs = append(allErrs, validateInstanceTemplate(spec.Template, specPath.Child("template"), opts)...)
	allErrs = append(allErrs, validateWorkloadPlacements(spec.Placements, specPath.Child("placements"), opts)...)

	return allErrs
}

func validateWorkloadPlacements(placements []computev1alpha.WorkloadPlacement, fieldPath *field.Path, opts WorkloadValidationOptions) field.ErrorList {
	allErrs := field.ErrorList{}

	if len(placements) == 0 {
		allErrs = append(allErrs, field.Required(fieldPath, ""))
	} else {
		for i, p := range placements {
			allErrs = append(allErrs, validateWorkloadPlacement(p, fieldPath.Index(i), opts)...)
		}
	}

	return allErrs
}

func validateWorkloadPlacement(placement computev1alpha.WorkloadPlacement, fieldPath *field.Path, opts WorkloadValidationOptions) field.ErrorList {
	allErrs := field.ErrorList{}

	nameField := fieldPath.Child("name")
	if len(placement.Name) == 0 {
		allErrs = append(allErrs, field.Required(nameField, ""))
	} else {
		for _, msg := range apimachineryvalidation.NameIsDNSLabel(placement.Name, false) {
			allErrs = append(allErrs, field.Invalid(nameField, placement.Name, msg))
		}
	}

	cityCodesPath := fieldPath.Child("cityCodes")
	if len(placement.CityCodes) == 0 {
		allErrs = append(allErrs, field.Required(cityCodesPath, ""))
	} else {
		for i, cityCode := range placement.CityCodes {
			if !slices.Contains(opts.ValidCityCodes, cityCode) {
				allErrs = append(allErrs, field.NotSupported(cityCodesPath.Index(i), cityCode, opts.ValidCityCodes))
			}
		}
	}

	allErrs = append(allErrs, validateScaleSettings(placement.ScaleSettings, fieldPath.Child("scaleSettings"))...)

	return allErrs
}

func validateScaleSettings(placement computev1alpha.HorizontalScaleSettings, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	// No scale-from-zero yet
	minReplicasField := fieldPath.Child("minReplicas")
	if placement.MinReplicas <= 0 {
		allErrs = append(allErrs, field.Invalid(minReplicasField, placement.MinReplicas, "must be greater than 0"))
	} else if placement.MinReplicas > 1000 {
		// TODO(jreese) entitlement backed constraints
		allErrs = append(allErrs, field.Invalid(minReplicasField, int(placement.MinReplicas), "must be less than or equal to 1000"))
	}

	metricsFieldPath := fieldPath.Child("metrics")
	if placement.MaxReplicas != nil {
		if len(placement.Metrics) == 0 {
			allErrs = append(allErrs, field.Required(metricsFieldPath, "must provide scaling metrics when maxReplicas is provided"))
		} else {
			allErrs = append(allErrs, validateScaleSettingMetrics(placement.Metrics, metricsFieldPath)...)
		}
	}
	return allErrs
}

func validateScaleSettingMetrics(metrics []computev1alpha.MetricSpec, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	for i, m := range metrics {
		metricField := fieldPath.Index(i)
		allErrs = append(allErrs, validateMetricSpec(m, metricField)...)
	}

	return allErrs
}

func validateMetricSpec(metric computev1alpha.MetricSpec, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	resourceField := fieldPath.Child("resource")
	if metric.Resource == nil {
		allErrs = append(allErrs, field.Required(resourceField, ""))
	} else {
		allErrs = append(allErrs, validateResourceMetricSource(*metric.Resource, resourceField)...)
	}

	return allErrs
}

var supportedResourceMetrics = sets.New(k8scorev1.ResourceCPU)

func validateResourceMetricSource(source computev1alpha.ResourceMetricSource, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if !supportedResourceMetrics.Has(source.Name) {
		allErrs = append(allErrs, field.NotSupported(fieldPath.Child("name"), source.Name, sets.List(supportedResourceMetrics)))
	}

	allErrs = append(allErrs, validateMetricTarget(source.Target, fieldPath.Child("target"))...)

	return allErrs
}

func validateMetricTarget(target computev1alpha.MetricTarget, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	numValues := 0

	if target.Value != nil {
		if numValues > 0 {
			allErrs = append(allErrs, field.Forbidden(fieldPath.Child("value"), "may not specify more than 1 target value"))
		} else {
			numValues++
			if target.Value.Sign() != 1 {
				allErrs = append(allErrs, field.Invalid(fieldPath.Child("value"), target.Value, "must be positive"))
			}
		}
	}

	if target.AverageValue != nil {
		if numValues > 0 {
			allErrs = append(allErrs, field.Forbidden(fieldPath.Child("averageValue"), "may not specify more than 1 target value"))
		} else {
			numValues++
			if target.AverageValue.Sign() != 1 {
				allErrs = append(allErrs, field.Invalid(fieldPath.Child("averageValue"), target.AverageValue, "must be positive"))
			}
		}
	}

	if target.AverageUtilization != nil {
		if numValues > 0 {
			allErrs = append(allErrs, field.Forbidden(fieldPath.Child("averageUtilization"), "may not specify more than 1 target value"))
		} else {
			numValues++
			if *target.AverageUtilization < 1 {
				allErrs = append(allErrs, field.Invalid(fieldPath.Child("averageUtilization"), target.AverageUtilization, "must be greater than 0"))
			}
		}
	}

	if numValues == 0 {
		allErrs = append(allErrs, field.Required(fieldPath, "must specify a target value"))
	}

	return allErrs
}
