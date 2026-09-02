// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"sort"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Catalog is the set of execution tiers a control plane offers. A Catalog is a
// slice of objects the caller already read, so no method here needs a client.
// The compute webhook builds one from the project control plane it admits into,
// and a provider builds one from the cluster it watches. Both then get the same
// answers from the same methods.
type Catalog []computev1alpha.RuntimeClass

// Find returns the class published under the given name, or nil when the
// catalog does not offer that name.
func (c Catalog) Find(name string) *computev1alpha.RuntimeClass {
	for i := range c {
		if c[i].Name == name {
			return &c[i]
		}
	}
	return nil
}

// Default returns the class an instance runs in when it selects none. It
// returns nil when the catalog does not mark exactly one class as the default.
//
// The default marker on a class is the only source of the answer. A catalog
// with zero or several marked classes has not stated a default, and any
// fallback name compiled in here would override the catalog. A wrong guess
// would place new workloads in a tier with different isolation, startup time,
// and cost, so Default returns nil and the caller asks the customer to choose
// from the published names.
func (c Catalog) Default() *computev1alpha.RuntimeClass {
	var marked *computev1alpha.RuntimeClass
	for i := range c {
		if !c[i].Spec.Default {
			continue
		}
		if marked != nil {
			return nil
		}
		marked = &c[i]
	}
	return marked
}

// Names returns the published class names in sorted order. Callers show the
// list as the supported values when a customer names a class the catalog does
// not offer.
func (c Catalog) Names() []string {
	names := make([]string, 0, len(c))
	for i := range c {
		names = append(names, c[i].Name)
	}
	sort.Strings(names)
	return names
}

// ClaimedBy returns the classes a controller implements. A provider calls
// ClaimedBy to find the classes it must report on, so changing which classes a
// provider serves is a catalog edit rather than a rebuild.
func (c Catalog) ClaimedBy(controllerName computev1alpha.RuntimeClassControllerName) Catalog {
	var claimed Catalog
	for i := range c {
		if c[i].Spec.ControllerName == controllerName {
			claimed = append(claimed, c[i])
		}
	}
	return claimed
}

// Acceptance is the controller's report on whether it can serve a class.
// Acceptance is independent of whether any cell has capacity for the class.
type Acceptance int

const (
	// AcceptancePending means no controller has reported on the class yet. A
	// class stays pending between publication and the first reconcile by its
	// provider, including during a provider rollout, so pending does not
	// indicate a broken class.
	AcceptancePending Acceptance = iota

	// AcceptanceAccepted means the class's controller claimed the class and can
	// serve everything the class declares.
	AcceptanceAccepted

	// AcceptanceRejected means the class's controller claimed the class and
	// reported that it cannot serve what the class declares. Instances in the
	// class do not run until the catalog or the provider changes.
	AcceptanceRejected
)

// AcceptanceOf returns the controller's report on a class and the
// customer-facing message the controller supplied.
func AcceptanceOf(class *computev1alpha.RuntimeClass) (Acceptance, string) {
	if class == nil {
		return AcceptancePending, ""
	}
	condition := meta.FindStatusCondition(class.Status.Conditions, computev1alpha.RuntimeClassConditionAccepted)
	if condition == nil {
		return AcceptancePending, ""
	}
	switch condition.Status {
	case metav1.ConditionTrue:
		return AcceptanceAccepted, condition.Message
	case metav1.ConditionFalse:
		return AcceptanceRejected, condition.Message
	default:
		return AcceptancePending, condition.Message
	}
}
