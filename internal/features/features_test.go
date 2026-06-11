// SPDX-License-Identifier: AGPL-3.0-only

package features

import (
	"testing"
)

// TestNetworkingIntegration_DefaultEnabled verifies that the NetworkingIntegration
// feature gate defaults to enabled so that existing production deployments are
// unaffected when the flag is not set.
func TestNetworkingIntegration_DefaultEnabled(t *testing.T) {
	// Use a fresh gate so this test is independent of any global state mutations.
	gate := MutableFeatureGate.DeepCopy()
	if !gate.Enabled(NetworkingIntegration) {
		t.Error("NetworkingIntegration default = false, want true")
	}
}

// TestNetworkingIntegration_CanBeDisabled verifies that setting
// NetworkingIntegration=false via the feature gate string disables the
// integration, allowing operators to run compute without VPC/NSO.
func TestNetworkingIntegration_CanBeDisabled(t *testing.T) {
	gate := MutableFeatureGate.DeepCopy()
	if err := gate.Set("NetworkingIntegration=false"); err != nil {
		t.Fatalf("Set(NetworkingIntegration=false): %v", err)
	}
	if gate.Enabled(NetworkingIntegration) {
		t.Error("NetworkingIntegration = true after Set=false, want false")
	}
}

// TestNetworkingIntegration_ExplicitlyEnabled verifies that the gate can be
// explicitly set to true (round-trip).
func TestNetworkingIntegration_ExplicitlyEnabled(t *testing.T) {
	gate := MutableFeatureGate.DeepCopy()
	if err := gate.Set("NetworkingIntegration=true"); err != nil {
		t.Fatalf("Set(NetworkingIntegration=true): %v", err)
	}
	if !gate.Enabled(NetworkingIntegration) {
		t.Error("NetworkingIntegration = false after Set=true, want true")
	}
}
