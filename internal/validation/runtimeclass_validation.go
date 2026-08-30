// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation/field"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	"go.datum.net/compute/pkg/runtimeclass"
)

// validateRuntimeClassSelection resolves the execution tier an instance
// selected against the catalog the control plane publishes, then validates the
// instance against what that tier declares it can serve.
//
// Resolution reads the catalog instead of a compiled-in list of class names, so
// a tier can be added, retired, or have its contract corrected without a schema
// change. The caller supplies the catalog and must fail the request if it could
// not read the catalog.
//
// fieldPath is the path of the instance spec being validated.
func validateRuntimeClassSelection(
	spec computev1alpha.InstanceSpec,
	fieldPath *field.Path,
	opts WorkloadValidationOptions,
) field.ErrorList {
	allErrs := field.ErrorList{}
	classPath := fieldPath.Child("runtime", "class")
	class := spec.Runtime.Class

	// A class is placeable only after providers serve it and cells advertise
	// it. While the gate is off the control plane publishes no catalog, so a
	// selected class would have nowhere to run.
	if !features.FeatureGate.Enabled(features.RuntimeClasses) {
		if len(class) > 0 {
			allErrs = append(allErrs, field.Forbidden(classPath,
				"runtime classes are not enabled on this control plane, so a runtime class may not be selected"))
		}
		return allErrs
	}

	catalog := opts.RuntimeClasses
	offered := catalog.Names()

	// The mutating webhook stamps the catalog's default class. An empty value
	// here means the catalog publishes no default, so the instance has no
	// execution tier.
	if len(class) == 0 {
		return append(allErrs, field.Invalid(classPath, class, offeredClassesMessage(
			"a runtime class is required because this control plane publishes no default", offered,
		)))
	}

	selected := catalog.Find(class)
	if selected == nil {
		if len(offered) == 0 {
			return append(allErrs, field.Invalid(classPath, class,
				"this control plane offers no runtime classes"))
		}
		return append(allErrs, field.NotSupported(classPath, class, offered))
	}

	// A class whose controller reports that it cannot honor the class contract
	// will never run an instance, so reject the workload here rather than at
	// placement. A class that no controller has reported on yet is admitted,
	// because that state is normal during a provider rollout and immediately
	// after a class is published. If no provider ever claims the class,
	// placement reports the workload as unplaceable.
	if acceptance, message := runtimeclass.AcceptanceOf(selected); acceptance == runtimeclass.AcceptanceRejected {
		reason := fmt.Sprintf("the %q runtime class cannot currently run instances", class)
		if len(message) > 0 {
			reason = fmt.Sprintf("%s: %s", reason, message)
		}
		return append(allErrs, field.Forbidden(classPath, reason))
	}

	// Every rejection below comes from what the class publishes, so a tier that
	// cannot serve part of the instance API declares that once, in the
	// catalog.
	allErrs = append(allErrs, runtimeclass.ValidateInstanceSpec(spec, runtimeclass.CapabilitiesFrom(selected), fieldPath)...)

	return allErrs
}

// offeredClassesMessage appends the classes a caller can choose from, so a
// rejection states which values are valid.
func offeredClassesMessage(reason string, offered []string) string {
	if len(offered) == 0 {
		return reason + ", and offers no runtime classes"
	}
	return fmt.Sprintf("%s; available runtime classes: %v", reason, offered)
}
