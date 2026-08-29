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
// the Capabilities its provider declares, so adding a class is a declaration
// rather than an edit to shared code.
package runtimeclass

import (
	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Feature is an optional part of the instance API that a runtime class may or
// may not be able to serve. Features name customer-visible capabilities, not
// implementation mechanisms, because they end up in the published class
// contract and in the message a customer reads when a class rejects their
// instance.
type Feature string

const (
	// FeatureSandboxRuntime is the ability to run an instance shaped as a
	// sandbox of containers.
	FeatureSandboxRuntime Feature = "sandboxRuntime"

	// FeatureVirtualMachineRuntime is the ability to run an instance shaped as
	// a virtual machine booting a customer-supplied image.
	FeatureVirtualMachineRuntime Feature = "virtualMachineRuntime"

	// FeatureConfigMapVolumes is the ability to present a ConfigMap to an
	// instance as a volume.
	FeatureConfigMapVolumes Feature = "configMapVolumes"

	// FeatureSecretVolumes is the ability to present a Secret to an instance
	// as a volume.
	FeatureSecretVolumes Feature = "secretVolumes"

	// FeatureDiskVolumes is the ability to back a volume with a persistent
	// disk. A class whose root filesystem lives in RAM typically cannot.
	FeatureDiskVolumes Feature = "diskVolumes"

	// FeatureDeviceVolumeAttachments is the ability to attach a volume as a
	// raw device — an attachment with no mount path — leaving the guest to
	// format and mount it.
	FeatureDeviceVolumeAttachments Feature = "deviceVolumeAttachments"

	// FeatureEnvFrom is the ability to populate a container's environment from
	// a whole ConfigMap or Secret rather than key by key.
	FeatureEnvFrom Feature = "envFrom"

	// FeatureImagePullSecrets is the ability to authenticate to a registry
	// with customer-supplied credentials when pulling an instance image.
	FeatureImagePullSecrets Feature = "imagePullSecrets"
)

// featureDescriptions carries the customer-facing phrase for each feature.
// Rejections quote this rather than the Feature constant so the message reads
// as product language instead of an API key.
var featureDescriptions = map[Feature]string{
	FeatureSandboxRuntime:          "container sandbox instances",
	FeatureVirtualMachineRuntime:   "virtual machine instances",
	FeatureConfigMapVolumes:        "ConfigMap-backed volumes",
	FeatureSecretVolumes:           "Secret-backed volumes",
	FeatureDiskVolumes:             "disk-backed volumes",
	FeatureDeviceVolumeAttachments: "volumes attached as raw devices",
	FeatureEnvFrom:                 "environment variables sourced from a whole ConfigMap or Secret",
	FeatureImagePullSecrets:        "image pull secrets",
}

// Description returns the customer-facing phrase for the feature, falling back
// to the feature name so a newly added feature is still readable if its
// description was forgotten.
func (f Feature) Description() string {
	if description, ok := featureDescriptions[f]; ok {
		return description
	}
	return string(f)
}

// Capabilities is a runtime class's declaration of what it can serve. It is
// supplied by the provider that realizes the class — the platform never infers
// it — which is what keeps class-specific behavior out of shared code.
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
