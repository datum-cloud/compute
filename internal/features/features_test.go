// SPDX-License-Identifier: AGPL-3.0-only

package features

import (
	"testing"
)

// TestNetworkingIntegration_DefaultDisabled verifies that the NetworkingIntegration
// feature gate defaults to disabled so cells, which carry no networking CRDs, come
// up crash-safe when the flag is not set; deployments running NSO opt in explicitly.
func TestNetworkingIntegration_DefaultDisabled(t *testing.T) {
	// Copy the gate so the test does not depend on mutations to global state.
	gate := MutableFeatureGate.DeepCopy()
	if gate.Enabled(NetworkingIntegration) {
		t.Error("NetworkingIntegration default = true, want false")
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

// TestRuntimeClasses_DefaultDisabled verifies that the RuntimeClasses feature
// gate defaults to disabled, so a control plane rejects an execution tier
// selection before providers that serve the tier are deployed.
func TestRuntimeClasses_DefaultDisabled(t *testing.T) {
	// Copy the gate so the test does not depend on mutations to global state.
	gate := MutableFeatureGate.DeepCopy()
	if gate.Enabled(RuntimeClasses) {
		t.Error("RuntimeClasses default = true, want false")
	}
}

// TestRuntimeClasses_CanBeDisabled verifies that setting RuntimeClasses=false
// keeps runtime class selection off.
func TestRuntimeClasses_CanBeDisabled(t *testing.T) {
	gate := MutableFeatureGate.DeepCopy()
	if err := gate.Set("RuntimeClasses=false"); err != nil {
		t.Fatalf("Set(RuntimeClasses=false): %v", err)
	}
	if gate.Enabled(RuntimeClasses) {
		t.Error("RuntimeClasses = true after Set=false, want false")
	}
}

// TestRuntimeClasses_ExplicitlyEnabled verifies that setting
// RuntimeClasses=true turns runtime class selection on.
func TestRuntimeClasses_ExplicitlyEnabled(t *testing.T) {
	gate := MutableFeatureGate.DeepCopy()
	if err := gate.Set("RuntimeClasses=true"); err != nil {
		t.Fatalf("Set(RuntimeClasses=true): %v", err)
	}
	if !gate.Enabled(RuntimeClasses) {
		t.Error("RuntimeClasses = false after Set=true, want true")
	}
}
