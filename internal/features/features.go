// SPDX-License-Identifier: AGPL-3.0-only

// Package features defines the feature gates for the compute operator. Feature
// gates follow the Kubernetes component-base convention: each feature is
// declared as a Feature constant, registered with a FeatureSpec that includes
// its default enablement state, and toggled at runtime via the --feature-gates
// flag exposed by the binary.
//
// Usage in cmd/main.go:
//
//	features.MutableFeatureGate.AddFlag(flag.CommandLine)
//
// Usage in controllers:
//
//	if features.MutableFeatureGate.Enabled(features.NetworkingIntegration) { ... }
package features

import (
	"k8s.io/component-base/featuregate"
)

const (
	// NetworkingIntegration controls whether the compute operator integrates with
	// the network-services-operator (VPC) for NetworkBinding provisioning and the
	// Network scheduling gate on Instances.
	//
	// When disabled:
	//   - No NetworkBinding objects are created.
	//   - The Network scheduling gate is not added to newly created Instances.
	//   - Any existing Network scheduling gate is actively removed.
	//   - The networking step is treated as immediately ready so Instances
	//     proceed to the runtime without a NetworkBinding.
	//
	// This flag exists so operators can run compute on edge/lab cells where
	// VPC/NSO is not yet functional. The default is true (enabled) so that
	// existing production deployments are unaffected.
	//
	// alpha: v0.1
	NetworkingIntegration featuregate.Feature = "NetworkingIntegration"
)

// MutableFeatureGate is the mutable feature gate for the compute operator.
// Call MutableFeatureGate.AddFlag to register the --feature-gates flag before
// flag.Parse(). Controllers should read from FeatureGate (the read-only view)
// after startup.
var MutableFeatureGate featuregate.MutableFeatureGate = featuregate.NewFeatureGate()

// FeatureGate is the read-only view of the compute operator feature gate.
// Use this in controllers and reconcilers rather than MutableFeatureGate to
// avoid accidental mutations after startup.
var FeatureGate featuregate.FeatureGate = MutableFeatureGate

func init() {
	if err := MutableFeatureGate.Add(map[featuregate.Feature]featuregate.FeatureSpec{
		NetworkingIntegration: {Default: true, PreRelease: featuregate.Alpha},
	}); err != nil {
		panic(err)
	}
}
