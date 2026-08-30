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
// selected against the catalog the control plane publishes, and holds the
// instance to what that tier declares it can serve.
//
// Resolution replaces a compiled-in list of class names on purpose: the catalog
// is data, so a tier can be added, retired, or have its contract corrected
// without a schema change. The cost is that the catalog has to be readable to
// admit a workload at all — the caller supplies it, and a caller that could not
// read it fails the request rather than admitting a workload into a tier
// nothing has agreed to run.
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

	// A class is only a promise the platform can keep once providers serve it
	// and cells advertise it. Until the feature is enabled there is exactly one
	// tier to run in, the catalog is not consulted, and accepting anything else
	// would accept a workload with nowhere to run.
	if !features.FeatureGate.Enabled(features.RuntimeClasses) {
		if len(class) > 0 && class != computev1alpha.DefaultRuntimeClass {
			allErrs = append(allErrs, field.Forbidden(classPath, fmt.Sprintf(
				"runtime classes are not enabled on this control plane, only %q may be selected",
				computev1alpha.DefaultRuntimeClass,
			)))
		}
		return allErrs
	}

	catalog := opts.RuntimeClasses
	offered := catalog.Names()

	// The mutating webhook stamps the catalog's default class, so an empty
	// value here means the catalog offered no default to stamp. Letting it
	// through would admit a workload whose tier nobody has stated.
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

	// A class whose controller has looked at it and said it cannot honor what
	// the class declares will never run an instance, so the workload is turned
	// down now rather than at placement. A class no controller has reported on
	// yet is admitted: that is the ordinary state during a provider rollout or
	// immediately after a class is published, and refusing it would make every
	// provider restart an outage for new workloads. If no provider ever claims
	// the class the workload is reported as unplaceable, which is where a
	// missing provider is visible either way.
	if acceptance, message := runtimeclass.AcceptanceOf(selected); acceptance == runtimeclass.AcceptanceRejected {
		reason := fmt.Sprintf("the %q runtime class cannot currently run instances", class)
		if len(message) > 0 {
			reason = fmt.Sprintf("%s: %s", reason, message)
		}
		return append(allErrs, field.Forbidden(classPath, reason))
	}

	// Every rejection below is sourced from what the class publishes, so a tier
	// that cannot serve part of the instance API says so once, in the catalog,
	// rather than in a table the platform keeps privately in sync.
	allErrs = append(allErrs, runtimeclass.ValidateInstanceSpec(spec, runtimeclass.CapabilitiesFrom(selected), fieldPath)...)

	return allErrs
}

// offeredClassesMessage appends the classes a customer can choose from, so a
// rejection tells them what to write instead of only what not to.
func offeredClassesMessage(reason string, offered []string) string {
	if len(offered) == 0 {
		return reason + ", and offers no runtime classes"
	}
	return fmt.Sprintf("%s; available runtime classes: %v", reason, offered)
}
