// SPDX-License-Identifier: AGPL-3.0-only

// Package runtimeclass holds the parts of a runtime class contract that every
// class implements identically. Those parts are the declaration of what a
// class can serve, the check of an instance against that declaration, and the
// customer-facing text shown when an instance cannot run.
//
// The compute control plane and every provider share this package so that a
// customer sees the same behavior regardless of which class they choose. Each
// provider owns the rest: how it targets and configures its runtime, its
// lifecycle, and the capacity it advertises.
//
// No code in this package branches on a class name. A RuntimeClass object
// describes its own class through Capabilities, so adding a class is a catalog
// change rather than an edit to shared code.
package runtimeclass

import (
	"fmt"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Feature is an optional part of the instance API that a runtime class may be
// able to serve. Feature aliases the API type so that a class's declaration and
// a provider's check use one vocabulary. Separate types would let a class
// declare a feature no provider can interpret.
type Feature = computev1alpha.RuntimeClassFeature

// The features a runtime class may declare. These alias the API constants so
// providers can import them from here without depending on the exact shape of
// the catalog object.
const (
	FeatureSandboxRuntime          = computev1alpha.RuntimeClassFeatureSandboxRuntime
	FeatureVirtualMachineRuntime   = computev1alpha.RuntimeClassFeatureVirtualMachineRuntime
	FeatureConfigMapVolumes        = computev1alpha.RuntimeClassFeatureConfigMapVolumes
	FeatureSecretVolumes           = computev1alpha.RuntimeClassFeatureSecretVolumes
	FeatureDiskVolumes             = computev1alpha.RuntimeClassFeatureDiskVolumes
	FeatureDeviceVolumeAttachments = computev1alpha.RuntimeClassFeatureDeviceVolumeAttachments
	FeatureEnvFrom                 = computev1alpha.RuntimeClassFeatureEnvFrom
	FeatureImagePullSecrets        = computev1alpha.RuntimeClassFeatureImagePullSecrets
)

// Capabilities is a runtime class's declaration of what it can serve, in the
// form the shared validation works against. Capabilities comes from the
// RuntimeClass object rather than from compiled-in values, so the published
// contract and the check applied to an instance cannot drift apart.
type Capabilities struct {
	// Class is the runtime class these capabilities describe. Every rejection
	// names the class so a customer knows which class refused the instance.
	Class string

	// Features are the optional parts of the instance API the class serves. An
	// absent feature is unsupported, so a class that omits a feature rejects
	// requests for it rather than serving it by accident.
	Features []Feature
}

// CapabilitiesFrom reads a class's declaration from its catalog entry.
func CapabilitiesFrom(class *computev1alpha.RuntimeClass) Capabilities {
	if class == nil {
		return Capabilities{}
	}
	return Capabilities{
		Class:    class.Name,
		Features: class.Spec.Capabilities.Features,
	}
}

// Supports reports whether the class serves the feature.
func (c Capabilities) Supports(feature Feature) bool {
	for _, supported := range c.Features {
		if supported == feature {
			return true
		}
	}
	return false
}

// ClassDescription returns the phrase a rejection uses to refer to the class
// that refused an instance. An instance admitted before the catalog existed has
// no resolved class name, so ClassDescription returns a generic phrase instead
// of an empty quoted string.
func (c Capabilities) ClassDescription() string {
	if c.Class == "" {
		return "the runtime class this instance runs in"
	}
	return fmt.Sprintf("the %q runtime class", c.Class)
}
