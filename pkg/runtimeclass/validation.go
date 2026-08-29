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
// A class publishes a contract, so quietly skipping a feature a customer asked
// for — a disk volume dropped while the instance otherwise starts — breaks
// that contract in a way the customer only discovers from behavior that does
// not match what they wrote. Every unsupported request is returned instead,
// naming the class and the feature, so the whole gap is visible at once rather
// than one rejection per apply.
//
// The caller selects which class's Capabilities to validate against, normally
// via EffectiveClass. fldPath is the path of the instance spec being validated
// (`spec` for an Instance, `spec.template.spec` for a workload's template).
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
// would produce. Rejecting at the workload is what lets a customer learn at
// apply time instead of from instances that never come up.
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

// validateVolumeAttachment checks the one property of an attachment a class
// can refuse: an attachment with no mount path is a raw device handed to the
// guest, which a class that owns the guest's filesystem cannot present.
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
// not serve. It names the class so the customer knows which tier said no, and
// the feature in product language so they know what to change.
func unsupported(fldPath *field.Path, capabilities Capabilities, feature Feature) *field.Error {
	return field.Forbidden(fldPath, fmt.Sprintf(
		"%s are not supported by the %q runtime class",
		feature.Description(), capabilities.ClassName(),
	))
}
