// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func TestCapabilitiesSupports(t *testing.T) {
	capabilities := Capabilities{
		Class:    computev1alpha.RuntimeClassUnikernel,
		Features: []Feature{FeatureSandboxRuntime, FeatureConfigMapVolumes},
	}

	tests := []struct {
		name    string
		feature Feature
		want    bool
	}{
		{name: "declared feature", feature: FeatureSandboxRuntime, want: true},
		{name: "second declared feature", feature: FeatureConfigMapVolumes, want: true},
		{name: "undeclared feature", feature: FeatureDiskVolumes, want: false},
		{name: "undeclared runtime shape", feature: FeatureVirtualMachineRuntime, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := capabilities.Supports(test.feature); got != test.want {
				t.Errorf("Supports(%q) = %v, want %v", test.feature, got, test.want)
			}
		})
	}
}

func TestCapabilitiesClassName(t *testing.T) {
	tests := []struct {
		name  string
		class string
		want  string
	}{
		{
			name:  "declared class",
			class: computev1alpha.RuntimeClassGeneralPurpose,
			want:  computev1alpha.RuntimeClassGeneralPurpose,
		},
		{name: "unset class falls back to the platform default", class: "", want: computev1alpha.DefaultRuntimeClass},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := (Capabilities{Class: test.class}).ClassName(); got != test.want {
				t.Errorf("ClassName() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEffectiveClass(t *testing.T) {
	tests := []struct {
		name  string
		class string
		want  string
	}{
		{
			name:  "selected class",
			class: computev1alpha.RuntimeClassGeneralPurpose,
			want:  computev1alpha.RuntimeClassGeneralPurpose,
		},
		{name: "no class selected", class: "", want: computev1alpha.DefaultRuntimeClass},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := computev1alpha.InstanceSpec{
				Runtime: computev1alpha.InstanceRuntimeSpec{Class: test.class},
			}
			if got := EffectiveClass(spec); got != test.want {
				t.Errorf("EffectiveClass() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestFeatureDescriptions keeps rejections readable: every feature a class can
// refuse has to have product language to refuse it in.
func TestFeatureDescriptions(t *testing.T) {
	features := []Feature{
		FeatureSandboxRuntime,
		FeatureVirtualMachineRuntime,
		FeatureConfigMapVolumes,
		FeatureSecretVolumes,
		FeatureDiskVolumes,
		FeatureDeviceVolumeAttachments,
		FeatureEnvFrom,
		FeatureImagePullSecrets,
	}

	for _, feature := range features {
		t.Run(string(feature), func(t *testing.T) {
			if feature.Description() == string(feature) {
				t.Errorf("feature %q has no customer-facing description", feature)
			}
		})
	}

	if got := Feature("undescribed").Description(); got != "undescribed" {
		t.Errorf("Description() = %q, want the feature name as a fallback", got)
	}
}
