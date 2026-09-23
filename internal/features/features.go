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

	// RuntimeClasses controls whether a workload may select the execution tier
	// its instances run in, through the runtime class field on the instance
	// template.
	//
	// When the gate is disabled, admission does not default the runtime class
	// and rejects any class selection.
	//
	// The gate defaults to disabled because a non-default class is placeable
	// only after providers that serve it are deployed and cells advertise it.
	// Deployments whose cells serve more than one class opt in with
	// --feature-gates=RuntimeClasses=true.
	//
	// Disabling the gate again is safe only while no non-default class is
	// generally available. Workloads that already selected one become
	// unplaceable.
	//
	// alpha: v0.1
	RuntimeClasses featuregate.Feature = "RuntimeClasses"

	// InstanceTypes controls whether the compute operator maintains and
	// accepts instance types.
	//
	// The controller and webhook now read and maintain instance types in each
	// project's own control plane, through the multicluster manager, rather
	// than in one shared cluster. A project only carries the InstanceType CRD
	// once the compute ServiceConfiguration's provisioning has reached it
	// (datum-infra), so the two sides of this need to roll out together.
	//
	// The controller is not registered while the gate is off: unlike the
	// webhook, its startup blocks on a cache sync, and a project without the
	// CRD yet would wedge that sync (this is what crashed the manager in
	// staging before the project-plane read was added).
	//
	// The webhook is always registered, so a stray write can never be
	// silently admitted unvalidated. Instead it rejects every InstanceType
	// create, update, and delete with an explained reason while the gate is
	// off, the same way RuntimeClasses gates a selection instead of leaving
	// it unenforced.
	//
	// The gate defaults to disabled so this can roll out to every environment
	// safely; deployments opt in per-environment, once the datum-infra
	// provisioning change has also reached that environment, with
	// --feature-gates=InstanceTypes=true.
	//
	// alpha: v0.1
	InstanceTypes featuregate.Feature = "InstanceTypes"
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
		RuntimeClasses:        {Default: false, PreRelease: featuregate.Alpha},
		InstanceTypes:         {Default: false, PreRelease: featuregate.Alpha},
	}); err != nil {
		panic(err)
	}
}
