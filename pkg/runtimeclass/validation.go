// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation/field"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

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

	containersPath := fldPath.Child("containers")
	for i, container := range sandbox.Containers {
		containerPath := containersPath.Index(i)

		if len(container.EnvFrom) > 0 && !capabilities.Supports(FeatureEnvFrom) {
			allErrs = append(allErrs, unsupported(containerPath.Child("envFrom"), capabilities, FeatureEnvFrom))
		}

		for j, attachment := range container.VolumeAttachments {
			allErrs = append(allErrs, validateVolumeAttachment(attachment, capabilities,
				containerPath.Child("volumeAttachments").Index(j))...)
		}
	}

	return allErrs
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
