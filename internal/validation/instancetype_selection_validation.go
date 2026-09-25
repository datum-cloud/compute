// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"k8s.io/apimachinery/pkg/util/validation/field"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	"go.datum.net/compute/pkg/instancetype"
	instancetypecatalog "go.datum.net/compute/pkg/instancetypecatalog"
)

// validateInstanceTypeSelection resolves the instance type a workload selected
// against the catalog the project control plane publishes, and rejects the
// workload when the type cannot serve new instances. It is the instanceType
// analog of validateRuntimeClassSelection.
//
// fieldPath is the path of the instance spec being validated.
func validateInstanceTypeSelection(
	spec computev1alpha.InstanceSpec,
	fieldPath *field.Path,
	opts WorkloadValidationOptions,
) field.ErrorList {
	allErrs := field.ErrorList{}
	typePath := fieldPath.Child("runtime", "resources", "instanceType")
	selected := spec.Runtime.Resources.InstanceType

	// While the gate is off there are no InstanceType objects, but the platform
	// still runs instances on the hardcoded catalog pkg/instancetype publishes,
	// which is also what the quota path resolves sizing from. A type that catalog
	// serves has somewhere to run off-gate, so it is allowed (the renderer stamps
	// the fallback name, and old code accepted it). Every other type would have
	// nowhere to run. A workload stored while the gate was on may carry a type
	// the hardcoded catalog does not serve; refusing that one on the way back out
	// would wedge the workload against every later update, so it is left alone.
	if !features.FeatureGate.Enabled(features.InstanceTypes) {
		if len(selected) == 0 || selected == storedInstanceType(opts.OldWorkload) {
			return allErrs
		}
		if _, ok := instancetype.Lookup(selected); !ok {
			return append(allErrs, field.NotSupported(typePath, selected, instancetype.Names()))
		}
		return allErrs
	}

	catalog := opts.InstanceTypes
	offered := catalog.Names()

	// An empty instanceType selects the platform's hardcoded fallback type, for
	// backwards compatibility with workloads written before the catalog existed.
	// Customers are expected to name a type, so the webhook warns on create; that
	// warning does not reject the workload.
	if selected == "" {
		return allErrs
	}

	selectedType := catalog.Find(selected)
	if selectedType == nil {
		if len(offered) == 0 {
			return append(allErrs, field.Invalid(typePath, selected,
				"this control plane offers no instance types"))
		}
		return append(allErrs, field.NotSupported(typePath, selected, offered))
	}

	switch selectedType.Spec.Lifecycle.Phase {
	case computev1alpha.InstanceTypePhaseDisabled:
		return append(allErrs, field.Forbidden(typePath,
			instancetypecatalog.DisabledMessage(selected, selectedType.Spec.Lifecycle.ReplacementInstanceType)))
	case computev1alpha.InstanceTypePhaseDeprecated:
		// Deprecated types still serve new instances with a warning; the webhook
		// surfaces the migration target as an admission warning.
		return allErrs
	}

	return allErrs
}

// storedInstanceType returns the instance type the stored workload already
// selects, and an empty string on create.
func storedInstanceType(old *computev1alpha.Workload) string {
	if old == nil {
		return ""
	}
	return old.Spec.Template.Spec.Runtime.Resources.InstanceType
}
