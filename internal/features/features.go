// SPDX-License-Identifier: AGPL-3.0-only

// Package features defines the feature gates for the compute operator. Feature
// gates follow the Kubernetes component-base convention: each feature is
// declared as a Feature constant, registered with a FeatureSpec that includes
// its default enablement state, and toggled at runtime via the --feature-gates
// flag exposed by the binary.
//
// cmd/main.go defines the --feature-gates string flag itself and applies its
// value with:
//
//	features.MutableFeatureGate.Set(featureGatesFlag)
//
// Enablement is read through the read-only view:
//
//	if features.FeatureGate.Enabled(features.NetworkingIntegration) { ... }
package features

import (
	"k8s.io/component-base/featuregate"
)

const (
	// NetworkingIntegration controls whether the compute operator integrates with
	// the network-services-operator (VPC) for interface addressing and the
	// Network scheduling gate on Instances.
	//
	// When disabled:
	//   - No NetworkInterfaceClaim objects are created, and none are read.
	//   - The Network scheduling gate is not added to newly created Instances.
	//   - Any existing Network scheduling gate is actively removed.
	//   - The networking step is treated as immediately ready so Instances
	//     proceed to the runtime without addresses of their own.
	//
	// This flag exists so operators can run compute on edge/lab cells where
	// VPC/NSO is not yet functional. The default is disabled: cells carry no
	// networking.datumapis.com CRDs, and registering a watch for an absent CRD
	// wedges the manager's cache sync and crash-loops it. Deployments that run
	// network-services-operator opt in with
	// --feature-gates=NetworkingIntegration=true.
	//
	// alpha: v0.1
	NetworkingIntegration featuregate.Feature = "NetworkingIntegration"
)

// MutableFeatureGate is the mutable feature gate for the compute operator.
// cmd/main.go applies the --feature-gates flag value via MutableFeatureGate.Set
// at startup. Enablement should be read from FeatureGate (the read-only view)
// after startup.
var MutableFeatureGate featuregate.MutableFeatureGate = featuregate.NewFeatureGate()

// FeatureGate is the read-only view of the compute operator feature gate.
// Use this for enablement checks rather than MutableFeatureGate to avoid
// accidental mutations after startup.
var FeatureGate featuregate.FeatureGate = MutableFeatureGate

func init() {
	if err := MutableFeatureGate.Add(map[featuregate.Feature]featuregate.FeatureSpec{
		NetworkingIntegration: {Default: false, PreRelease: featuregate.Alpha},
	}); err != nil {
		panic(err)
	}
}
