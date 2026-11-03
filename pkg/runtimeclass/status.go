// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// TranslateWaitingReason converts a Kubernetes container waiting reason into
// the Instance reason and message a customer reads on their instance.
//
// Kubernetes reasons such as ImagePullBackOff describe a Pod the customer did
// not create and expose how one class is implemented. Translating in one place
// also keeps two classes from describing the same failure differently, because
// the customer-facing wording belongs to the platform rather than to the
// runtime that observed the failure.
//
// An unrecognized reason maps to provisioning, so a new Kubernetes reason
// cannot reach a customer-facing message. TranslateWaitingReason drops the
// original reason and message, so the caller must log them for operators.
func TranslateWaitingReason(k8sReason string) (reason, message string) {
	switch k8sReason {
	case "ImagePullBackOff", "ErrImagePull", "ImageInspectError",
		"InvalidImageName", "RegistryUnavailable":
		return computev1alpha.InstanceReadyReasonImageUnavailable,
			"The instance image could not be pulled"
	case "CrashLoopBackOff":
		return computev1alpha.InstanceReadyReasonInstanceCrashing,
			"The instance is repeatedly failing to start"
	case "CreateContainerError", "CreateContainerConfigError":
		return computev1alpha.InstanceReadyReasonConfigurationError,
			"The instance could not be started due to a configuration error"
	default:
		return computev1alpha.InstanceReadyReasonProvisioning,
			"Instance is provisioning"
	}
}
