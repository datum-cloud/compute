// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"strings"
	"testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// The customer-facing messages are spelled out here rather than referenced
// from the translator, so the test still fails if the wording changes.
const (
	wantImageUnavailableMessage   = "The instance image could not be pulled"
	wantConfigurationErrorMessage = "The instance could not be started due to a configuration error"
	wantProvisioningMessage       = "Instance is provisioning"
)

func TestTranslateWaitingReason(t *testing.T) {
	tests := []struct {
		name        string
		k8sReason   string
		wantReason  string
		wantMessage string
	}{
		{
			name:        "image pull backoff",
			k8sReason:   "ImagePullBackOff",
			wantReason:  computev1alpha.InstanceReadyReasonImageUnavailable,
			wantMessage: wantImageUnavailableMessage,
		},
		{
			name:        "image pull error",
			k8sReason:   "ErrImagePull",
			wantReason:  computev1alpha.InstanceReadyReasonImageUnavailable,
			wantMessage: wantImageUnavailableMessage,
		},
		{
			name:        "image inspect error",
			k8sReason:   "ImageInspectError",
			wantReason:  computev1alpha.InstanceReadyReasonImageUnavailable,
			wantMessage: wantImageUnavailableMessage,
		},
		{
			name:        "invalid image name",
			k8sReason:   "InvalidImageName",
			wantReason:  computev1alpha.InstanceReadyReasonImageUnavailable,
			wantMessage: wantImageUnavailableMessage,
		},
		{
			name:        "registry unavailable",
			k8sReason:   "RegistryUnavailable",
			wantReason:  computev1alpha.InstanceReadyReasonImageUnavailable,
			wantMessage: wantImageUnavailableMessage,
		},
		{
			name:        "crash loop",
			k8sReason:   "CrashLoopBackOff",
			wantReason:  computev1alpha.InstanceReadyReasonInstanceCrashing,
			wantMessage: "The instance is repeatedly failing to start",
		},
		{
			name:        "container create error",
			k8sReason:   "CreateContainerError",
			wantReason:  computev1alpha.InstanceReadyReasonConfigurationError,
			wantMessage: wantConfigurationErrorMessage,
		},
		{
			name:        "container config error",
			k8sReason:   "CreateContainerConfigError",
			wantReason:  computev1alpha.InstanceReadyReasonConfigurationError,
			wantMessage: wantConfigurationErrorMessage,
		},
		{
			name:        "container creating",
			k8sReason:   "ContainerCreating",
			wantReason:  computev1alpha.InstanceReadyReasonProvisioning,
			wantMessage: wantProvisioningMessage,
		},
		{
			name:        "pod initializing",
			k8sReason:   "PodInitializing",
			wantReason:  computev1alpha.InstanceReadyReasonProvisioning,
			wantMessage: wantProvisioningMessage,
		},
		{
			name:        "an unrecognized reason never leaks through",
			k8sReason:   "SomeNewKubeletReason",
			wantReason:  computev1alpha.InstanceReadyReasonProvisioning,
			wantMessage: wantProvisioningMessage,
		},
		{
			name:        "no reason at all",
			k8sReason:   "",
			wantReason:  computev1alpha.InstanceReadyReasonProvisioning,
			wantMessage: wantProvisioningMessage,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reason, message := TranslateWaitingReason(test.k8sReason)
			if reason != test.wantReason {
				t.Errorf("reason = %q, want %q", reason, test.wantReason)
			}
			if message != test.wantMessage {
				t.Errorf("message = %q, want %q", message, test.wantMessage)
			}

			// Customers read these strings on their instance: neither the
			// Kubernetes reason nor Kubernetes nouns belong in them.
			if test.k8sReason != "" && strings.Contains(reason+message, test.k8sReason) {
				t.Errorf("translation leaked the Kubernetes reason %q", test.k8sReason)
			}
			if strings.Contains(strings.ToLower(reason+message), "pod") {
				t.Errorf("translation says Pod; customer-facing language says Instance: %q / %q", reason, message)
			}
		})
	}
}
