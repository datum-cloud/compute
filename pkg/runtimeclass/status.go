// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// TranslateWaitingReason converts a Kubernetes container waiting reason into
// the Instance-domain reason and message a customer reads on their instance.
//
// Customers never see Kubernetes-internal jargon (ImagePullBackOff,
// CrashLoopBackOff) in an instance status: it names a Pod they did not create
// and leaks how one class happens to be realized. Translating centrally is
// also what keeps two classes from describing the same failure in two
// vocabularies — the customer-facing words for a failure belong to the
// platform, not to whichever runtime observed it.
//
// Anything unrecognized is reported as provisioning rather than surfaced raw,
// so a new Kubernetes reason can never leak into a customer-facing message.
// The caller is responsible for logging the original reason and message for
// operator visibility, since that detail is dropped here.
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
