// SPDX-License-Identifier: AGPL-3.0-only

// Package runtimeclass holds the parts of a runtime class contract that every
// class honors identically: what a class declares it can serve, how an
// instance is checked against that declaration, and the words a customer is
// told when an instance cannot run.
//
// A runtime class is a published promise, so the pieces a customer experiences
// the same way no matter which class they chose are defined once here and
// shared by the compute control plane and by every provider. What a provider
// keeps for itself is realization: how its runtime is targeted and configured,
// its plumbing, its lifecycle, and the capacity it advertises.
//
// Nothing in this package branches on a class name. A class is described by
// the Capabilities carried on its RuntimeClass object, so adding a class is a
// catalog change rather than an edit to shared code.
package runtimeclass

import (
	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Feature is an optional part of the instance API that a runtime class may or
// may not be able to serve. It is the API's type: the declaration a class
// publishes and the check a provider runs against an instance have to be the
// same vocabulary, or a class could promise something no provider can read.
type Feature = computev1alpha.RuntimeClassFeature

// The features a runtime class may declare. These alias the API constants so
// providers can keep importing them from here without depending on the exact
// shape of the catalog object.
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
// form the shared validation works against. It is built from the RuntimeClass
// object rather than compiled in, so the platform's published contract and the
// check an instance is held to cannot drift apart.
type Capabilities struct {
	// Class is the runtime class these capabilities describe. It appears in
	// every rejection so a customer learns which tier turned their instance
	// down, and which one to move it to.
	Class string

	// Features are the optional parts of the instance API the class serves.
	// Anything absent is unsupported, so a class that forgets to declare a
	// feature rejects it loudly rather than serving it by accident.
	Features []Feature
}

// CapabilitiesFrom reads a class's declaration off its catalog entry.
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

// ClassName is the class these capabilities describe, resolving an unset Class
// to the platform default so messages never quote an empty class name.
func (c Capabilities) ClassName() string {
	return computev1alpha.EffectiveRuntimeClass(c.Class)
}

// EffectiveClass is the class an instance runs in: the one it selected, or the
// platform default when it selected none. Callers choosing which Capabilities
// to validate an instance against resolve the class through this so an
// unselected class is treated the same everywhere. What "unset" resolves to is
// the API's rule, not this package's, so it is asked for rather than repeated.
func EffectiveClass(spec computev1alpha.InstanceSpec) string {
	return computev1alpha.EffectiveRuntimeClass(spec.Runtime.Class)
}
