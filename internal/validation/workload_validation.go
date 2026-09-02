package validation

import (
	"context"
	"fmt"
	"slices"

	k8scorev1 "k8s.io/api/core/v1"
	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/pkg/runtimeclass"
)

// Great reference:
//   https://github.com/kubernetes/kubernetes/blob/master/pkg/apis/core/validation/validation.go

func ValidateWorkloadCreate(w *computev1alpha.Workload, opts WorkloadValidationOptions) field.ErrorList {
	allErrs := field.ErrorList{}

	// allErrs = append(allErrs, validateWorkloadMetadata(w)...)
	allErrs = append(allErrs, validateWorkloadSpec(w.Spec, opts)...)

	return allErrs
}

// ValidateWorkloadUpdate validates a workload update. It applies the
// create-time rules, plus the rules that need the previous state.
func ValidateWorkloadUpdate(w, oldWorkload *computev1alpha.Workload, opts WorkloadValidationOptions) field.ErrorList {
	allErrs := ValidateWorkloadCreate(w, opts)

	allErrs = append(allErrs, validateWorkloadSpecUpdate(w.Spec, oldWorkload.Spec, field.NewPath("spec"), opts)...)

	return allErrs
}

func validateWorkloadSpecUpdate(spec, oldSpec computev1alpha.WorkloadSpec, fieldPath *field.Path, opts WorkloadValidationOptions) field.ErrorList {
	allErrs := field.ErrorList{}

	classPath := fieldPath.Child("template", "spec", "runtime", "class")
	class := spec.Template.Spec.Runtime.Class
	oldClass := oldSpec.Template.Spec.Runtime.Class

	// Changing tiers changes the isolation boundary, image compatibility,
	// startup behavior, and price of every instance, and no provider can move
	// an instance across tiers in place. A tier change requires a new workload.
	//
	// An absent previous class is not a tier change by itself. It may be filled
	// in with the class the workload already runs in, which is the class the
	// catalog marks as default, because that is the class a workload selecting
	// nothing runs in. Any other name moves the workload between tiers. A
	// control plane that publishes no default, including one with the gate off,
	// has no such class, so nothing may be filled in.
	if len(oldClass) > 0 {
		allErrs = append(allErrs, apimachineryvalidation.ValidateImmutableField(class, oldClass, classPath)...)
	} else if len(class) > 0 {
		if defaultClass := opts.RuntimeClasses.Default(); defaultClass == nil {
			allErrs = append(allErrs, field.Forbidden(classPath,
				"may not be set on an existing workload, because this control plane publishes no default runtime class to compare it against"))
		} else if class != defaultClass.Name {
			allErrs = append(allErrs, field.Forbidden(classPath, fmt.Sprintf(
				"may only be set to %q on an existing workload, which is the class it already runs in",
				defaultClass.Name,
			)))
		}
	}

	return allErrs
}

type WorkloadValidationOptions struct {
	Client           client.Client
	AdmissionRequest admission.Request
	Context          context.Context
	Workload         *computev1alpha.Workload
	ValidCityCodes   []string

	// RuntimeClasses is the catalog of execution tiers this control plane
	// publishes, read by the caller. The catalog is empty when runtime class
	// selection is disabled. When selection is enabled, an empty catalog means
	// the control plane offers no tier, so validation rejects the workload.
	RuntimeClasses runtimeclass.Catalog
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
