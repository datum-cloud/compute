// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

var sysctlNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)+$`)

// ValidateInstanceSpec reports every part of the instance spec the class
// cannot serve.
//
// Ignoring an unsupported request, such as dropping a disk volume while the
// instance still starts, leaves the customer to discover the gap from runtime
// behavior. ValidateInstanceSpec instead returns one error per unsupported
// request, naming the class and the feature, so the customer sees the full gap
// in a single apply.
//
// fldPath is the path of the instance spec being validated. For example, use
// spec for an Instance and spec.template.spec for a workload's template.
func ValidateInstanceSpec(
	spec computev1alpha.InstanceSpec,
	capabilities Capabilities,
	fldPath *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	runtimePath := fldPath.Child("runtime")

	if spec.Runtime.Sandbox != nil {
		allErrs = append(allErrs, validateSandbox(spec.Runtime.Sandbox, capabilities, runtimePath.Child("sandbox"))...)
	}

	if spec.Runtime.VirtualMachine != nil {
		vmPath := runtimePath.Child("virtualMachine")
		if !capabilities.Supports(FeatureVirtualMachineRuntime) {
			allErrs = append(allErrs, unsupported(vmPath, capabilities, FeatureVirtualMachineRuntime))
		}
		for i, attachment := range spec.Runtime.VirtualMachine.VolumeAttachments {
			allErrs = append(allErrs, validateVolumeAttachment(attachment, capabilities,
				vmPath.Child("volumeAttachments").Index(i))...)
		}
	}

	volumesPath := fldPath.Child("volumes")
	for i, volume := range spec.Volumes {
		volumePath := volumesPath.Index(i)
		switch {
		case volume.ConfigMap != nil:
			if !capabilities.Supports(FeatureConfigMapVolumes) {
				allErrs = append(allErrs, unsupported(volumePath.Child("configMap"), capabilities, FeatureConfigMapVolumes))
			}
		case volume.Secret != nil:
			if !capabilities.Supports(FeatureSecretVolumes) {
				allErrs = append(allErrs, unsupported(volumePath.Child("secret"), capabilities, FeatureSecretVolumes))
			}
		case volume.Disk != nil:
			if !capabilities.Supports(FeatureDiskVolumes) {
				allErrs = append(allErrs, unsupported(volumePath.Child("disk"), capabilities, FeatureDiskVolumes))
			}
		}
	}

	return allErrs
}

// ValidateInstanceTemplateSpec validates the instance a workload's template
// would produce. Rejecting the workload tells the customer at apply time
// instead of leaving them to diagnose instances that never start.
func ValidateInstanceTemplateSpec(
	template computev1alpha.InstanceTemplateSpec,
	capabilities Capabilities,
	fldPath *field.Path,
) field.ErrorList {
	return ValidateInstanceSpec(template.Spec, capabilities, fldPath.Child("spec"))
}

func validateSandbox(
	sandbox *computev1alpha.SandboxRuntime,
	capabilities Capabilities,
	fldPath *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	if !capabilities.Supports(FeatureSandboxRuntime) {
		allErrs = append(allErrs, unsupported(fldPath, capabilities, FeatureSandboxRuntime))
	}

	if len(sandbox.ImagePullSecrets) > 0 && !capabilities.Supports(FeatureImagePullSecrets) {
		allErrs = append(allErrs, unsupported(fldPath.Child("imagePullSecrets"), capabilities, FeatureImagePullSecrets))
	}

	allErrs = append(allErrs, validateSandboxSysctls(sandbox.Sysctls, capabilities, fldPath.Child("sysctls"))...)

	containersPath := fldPath.Child("containers")
	for i, container := range sandbox.Containers {
		containerPath := containersPath.Index(i)

		if len(container.EnvFrom) > 0 && !capabilities.Supports(FeatureEnvFrom) {
			allErrs = append(allErrs, unsupported(containerPath.Child("envFrom"), capabilities, FeatureEnvFrom))
		}

		// Capabilities are the only security option checked against the class.
		// A class publishes a default for privilege escalation and the seccomp
		// profile but no limit on either, so neither is refused here whatever the
		// container states. Bounding them would need a published limit to reject
		// against, which the class contract does not yet express.
		allErrs = append(allErrs, validateContainerCapabilities(container.SecurityContext, capabilities,
			containerPath.Child("securityContext", "capabilities"))...)

		for j, attachment := range container.VolumeAttachments {
			allErrs = append(allErrs, validateVolumeAttachment(attachment, capabilities,
				containerPath.Child("volumeAttachments").Index(j))...)
		}
	}

	return allErrs
}

// validateSandboxSysctls checks every exact name/value pair against the class's
// published list. A class cannot accept a prefix, and a supported name with an
// unsupported value is rejected separately so the customer sees which half of
// the request to change.
func validateSandboxSysctls(
	requested []computev1alpha.SandboxSysctl,
	capabilities Capabilities,
	fldPath *field.Path,
) field.ErrorList {
	if len(requested) == 0 {
		return nil
	}
	if !capabilities.Supports(FeatureSandboxSysctls) {
		return field.ErrorList{unsupported(fldPath, capabilities, FeatureSandboxSysctls)}
	}

	allErrs := field.ErrorList{}
	seen := map[string]struct{}{}
	for i, request := range requested {
		requestPath := fldPath.Index(i)
		if !sysctlNamePattern.MatchString(request.Name) {
			allErrs = append(allErrs, field.Invalid(requestPath.Child("name"), request.Name,
				"must be a dotted sysctl name with non-empty components"))
			continue
		}
		if _, exists := seen[request.Name]; exists {
			allErrs = append(allErrs, field.Duplicate(requestPath.Child("name"), request.Name))
			continue
		}
		seen[request.Name] = struct{}{}
		if request.Value == "" {
			allErrs = append(allErrs, field.Required(requestPath.Child("value"), "must not be empty"))
			continue
		}
		supported := capabilities.SupportedSysctl(request.Name)
		if supported == nil {
			allErrs = append(allErrs, field.Forbidden(requestPath.Child("name"), fmt.Sprintf(
				"sysctl %s is not supported by %s, which supports %s",
				request.Name, capabilities.ClassDescription(), supportedSysctlNames(capabilities.SupportedSysctls),
			)))
			continue
		}
		if !capabilities.SupportsSysctl(request.Name, request.Value) {
			allErrs = append(allErrs, field.NotSupported(
				requestPath.Child("value"), request.Value, sortedSysctlValues(supported.AllowedValues)))
		}
	}
	return allErrs
}

func supportedSysctlNames(supported []Sysctl) string {
	if len(supported) == 0 {
		return "none"
	}
	names := make([]string, 0, len(supported))
	for _, sysctl := range supported {
		names = append(names, sysctl.Name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func sortedSysctlValues(values []computev1alpha.SysctlValue) []string {
	sorted := make([]string, 0, len(values))
	for _, value := range values {
		sorted = append(sorted, string(value))
	}
	slices.Sort(sorted)
	return sorted
}

// validateContainerCapabilities checks the capabilities a container adds
// against what the class grants. Only added capabilities are checked, because a
// drop can only reduce privilege and every class can honor it.
func validateContainerCapabilities(
	securityContext *computev1alpha.SandboxSecurityContext,
	capabilities Capabilities,
	fldPath *field.Path,
) field.ErrorList {
	if securityContext == nil || securityContext.Capabilities == nil || len(securityContext.Capabilities.Add) == 0 {
		return nil
	}

	addPath := fldPath.Child("add")
	if !capabilities.Supports(FeatureContainerCapabilities) {
		return field.ErrorList{unsupported(addPath, capabilities, FeatureContainerCapabilities)}
	}

	allErrs := field.ErrorList{}
	for i, requested := range securityContext.Capabilities.Add {
		if capabilities.Grants(requested) {
			continue
		}
		allErrs = append(allErrs, field.Forbidden(addPath.Index(i), fmt.Sprintf(
			"capability %s is not granted by %s, which grants %s",
			requested, capabilities.ClassDescription(), grantedList(capabilities.GrantableCapabilities),
		)))
	}
	return allErrs
}

// grantedList renders a class's grantable capabilities in sorted order so the
// rejection reads the same on every apply.
func grantedList(granted []Capability) string {
	if len(granted) == 0 {
		return "none"
	}
	names := make([]string, 0, len(granted))
	for _, capability := range granted {
		names = append(names, string(capability))
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// validateVolumeAttachment checks the single attachment property a class can
// refuse. An attachment with no mount path is a raw device passed to the guest,
// which a class that owns the guest filesystem cannot present.
func validateVolumeAttachment(
	attachment computev1alpha.VolumeAttachment,
	capabilities Capabilities,
	fldPath *field.Path,
) field.ErrorList {
	if attachment.MountPath != nil {
		return nil
	}
	if capabilities.Supports(FeatureDeviceVolumeAttachments) {
		return nil
	}
	return field.ErrorList{unsupported(fldPath, capabilities, FeatureDeviceVolumeAttachments)}
}

// unsupported builds the customer-facing rejection for a feature a class does
// not serve. The message names the class that refused the request and describes
// the feature in product terms so the customer knows what to change.
func unsupported(fldPath *field.Path, capabilities Capabilities, feature Feature) *field.Error {
	return field.Forbidden(fldPath, fmt.Sprintf(
		"%s are not supported by %s",
		feature.Description(), capabilities.ClassDescription(),
	))
}
