// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"fmt"

	apiequality "k8s.io/apimachinery/pkg/api/equality"

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
		// Admission stamps the selected class onto the stored workload, so a
		// workload stored while the gate was on carries a class the customer
		// never typed. Refusing it on the way back out would wedge the workload
		// against every later update, so only a newly named class is refused.
		if len(class) > 0 && class != storedRuntimeClass(opts.OldWorkload) {
			allErrs = append(allErrs, field.Forbidden(classPath,
				"runtime classes are not enabled on this control plane, so a runtime class may not be selected"))
		}
		// Only a runtime class grants capabilities, so with no catalog a
		// request to add one could never be honored. A drop only reduces
		// privilege and needs no class.
		//
		// Both rules skip a container whose security context is unchanged from
		// the stored object. Admission writes a class's published default onto a
		// workload, so turning the gate off leaves stored workloads holding
		// values these rules would otherwise reject. Rejecting them would make
		// the workload permanently unupdatable, including the controller's
		// finalizer patch, and so undeletable.
		stored := storedSecurityContexts(opts.OldWorkload)
		allErrs = append(allErrs, validateCapabilityAddsWithoutClasses(spec, stored, fieldPath)...)
		allErrs = append(allErrs, validatePrivilegeWideningWithoutClasses(spec, stored, fieldPath)...)
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
	if availability, message := runtimeclass.AvailabilityOf(selected); availability == runtimeclass.AvailabilityUnavailable {
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

// validateCapabilityAddsWithoutClasses rejects every capability a container
// adds on a control plane that publishes no runtime classes.
func validateCapabilityAddsWithoutClasses(
	spec computev1alpha.InstanceSpec,
	stored map[string]*computev1alpha.SandboxSecurityContext,
	fieldPath *field.Path,
) field.ErrorList {
	if spec.Runtime.Sandbox == nil {
		return nil
	}

	allErrs := field.ErrorList{}
	containersPath := fieldPath.Child("runtime", "sandbox", "containers")
	for i, container := range spec.Runtime.Sandbox.Containers {
		if container.SecurityContext == nil || container.SecurityContext.Capabilities == nil ||
			len(container.SecurityContext.Capabilities.Add) == 0 {
			continue
		}
		if unchangedSecurityContext(container, stored) {
			continue
		}
		allErrs = append(allErrs, field.Forbidden(
			containersPath.Index(i).Child("securityContext", "capabilities", "add"),
			"runtime classes are not enabled on this control plane, so no capability can be added"))
	}
	return allErrs
}

// validatePrivilegeWideningWithoutClasses rejects the security options that
// loosen a container's confinement on a control plane that publishes no runtime
// classes.
//
// The rule is scoped to the absence of a catalog, not to any per-class limit. A
// class publishes a default for both options and no limit on either, so with the
// gate on a container may state whatever it likes. With no catalog there is no
// published execution tier at all, and loosening confinement outside one is
// refused for the same reason adding a capability is.
//
// Tightening confinement needs no tier, so a container may still turn privilege
// escalation off or ask for the runtime's own seccomp profile anywhere.
func validatePrivilegeWideningWithoutClasses(
	spec computev1alpha.InstanceSpec,
	stored map[string]*computev1alpha.SandboxSecurityContext,
	fieldPath *field.Path,
) field.ErrorList {
	if spec.Runtime.Sandbox == nil {
		return nil
	}

	allErrs := field.ErrorList{}
	containersPath := fieldPath.Child("runtime", "sandbox", "containers")
	for i, container := range spec.Runtime.Sandbox.Containers {
		if container.SecurityContext == nil {
			continue
		}
		if unchangedSecurityContext(container, stored) {
			continue
		}
		securityPath := containersPath.Index(i).Child("securityContext")

		if allowed := container.SecurityContext.AllowPrivilegeEscalation; allowed != nil && *allowed {
			allErrs = append(allErrs, field.Forbidden(securityPath.Child("allowPrivilegeEscalation"),
				"runtime classes are not enabled on this control plane, so privilege escalation cannot be allowed"))
		}

		if profile := container.SecurityContext.SeccompProfile; profile != nil &&
			profile.Type == computev1alpha.SeccompProfileTypeUnconfined {
			allErrs = append(allErrs, field.Forbidden(securityPath.Child("seccompProfile", "type"),
				"runtime classes are not enabled on this control plane, so seccomp cannot be disabled"))
		}
	}
	return allErrs
}

// storedRuntimeClass returns the class the stored workload already selects, and
// an empty string on create.
func storedRuntimeClass(old *computev1alpha.Workload) string {
	if old == nil {
		return ""
	}
	return old.Spec.Template.Spec.Runtime.Class
}

// storedSecurityContexts indexes the security context of each sandbox container
// in the stored workload by container name. It returns nil on create, where
// every value in the request is one the customer just stated.
func storedSecurityContexts(old *computev1alpha.Workload) map[string]*computev1alpha.SandboxSecurityContext {
	if old == nil || old.Spec.Template.Spec.Runtime.Sandbox == nil {
		return nil
	}

	stored := make(map[string]*computev1alpha.SandboxSecurityContext)
	for _, container := range old.Spec.Template.Spec.Runtime.Sandbox.Containers {
		stored[container.Name] = container.SecurityContext
	}
	return stored
}

// unchangedSecurityContext reports whether the container carries the security
// context already stored for it, which makes the value one to leave alone
// rather than one to reject.
func unchangedSecurityContext(
	container computev1alpha.SandboxContainer,
	stored map[string]*computev1alpha.SandboxSecurityContext,
) bool {
	previous, ok := stored[container.Name]
	return ok && apiequality.Semantic.DeepEqual(previous, container.SecurityContext)
}

// offeredClassesMessage appends the classes a caller can choose from, so a
// rejection states which values are valid.
func offeredClassesMessage(reason string, offered []string) string {
	if len(offered) == 0 {
		return reason + ", and offers no runtime classes"
	}
	return fmt.Sprintf("%s; available runtime classes: %v", reason, offered)
}
