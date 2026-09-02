// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"testing"
)

func TestCapabilitiesSupports(t *testing.T) {
	capabilities := Capabilities{
		Class:    testClassAzurite,
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

// TestCapabilitiesClassDescription covers how a rejection refers to the class
// that refused an instance. A resolved class uses the name the catalog
// published, and an unresolved class uses a generic phrase.
func TestCapabilitiesClassDescription(t *testing.T) {
	tests := []struct {
		name  string
		class string
		want  string
	}{
		{name: "a published class is named", class: testClassBasalt, want: `the "basalt" runtime class`},
		{name: "no class was resolved", class: "", want: "the runtime class this instance runs in"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := (Capabilities{Class: test.class}).ClassDescription(); got != test.want {
				t.Errorf("ClassDescription() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestFeatureDescriptions checks that every feature a class can refuse has a
// customer-facing description to use in the rejection message.
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
