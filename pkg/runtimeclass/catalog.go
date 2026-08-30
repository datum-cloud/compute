// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"sort"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Catalog is the set of execution tiers a control plane offers. It is a plain
// slice of the objects a caller has already read, so nothing here needs a
// client: the compute webhook lists the catalog from the project control plane
// it is admitting into, and a provider lists it from wherever it watches, and
// both answer the same questions the same way.
type Catalog []computev1alpha.RuntimeClass

// Find returns the class published under the given name, or nil when the
// catalog does not offer it.
func (c Catalog) Find(name string) *computev1alpha.RuntimeClass {
	for i := range c {
		if c[i].Name == name {
			return &c[i]
		}
	}
	return nil
}

// Default returns the class an instance runs in when it selects none, or nil
// when the catalog does not say.
//
// The marker on the class is the whole answer. Exactly one class carrying it is
// the intended state; a catalog where none does, or several do, has not stated
// a default, and there is no name to fall back to that would not be the
// platform overruling the catalog it publishes. Guessing would silently place
// new workloads in a tier with different isolation, startup, and cost from the
// one the catalog meant, so the answer is nil and the caller makes the customer
// choose — which is a refusal naming the classes on offer, not an outage.
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

// Names returns the published class names in sorted order, for the "supported
// values" a customer is shown when they name a class that is not offered.
func (c Catalog) Names() []string {
	names := make([]string, 0, len(c))
	for i := range c {
		names = append(names, c[i].Name)
	}
	sort.Strings(names)
	return names
}

// ClaimedBy returns the classes a controller implements. A provider uses this
// to find the classes it is responsible for reporting on, rather than matching
// on a class name it would then have to be recompiled to change.
func (c Catalog) ClaimedBy(controllerName computev1alpha.RuntimeClassControllerName) Catalog {
	var claimed Catalog
	for i := range c {
		if c[i].Spec.ControllerName == controllerName {
			claimed = append(claimed, c[i])
		}
	}
	return claimed
}

// Acceptance is a class's controller's verdict on whether the class can be
// honored, which is a different question from whether any cell has capacity for
// it.
type Acceptance int

const (
	// AcceptancePending means no controller has reported on the class yet. It
	// is the state a class sits in between being published and the provider
	// that implements it reconciling — including a provider rollout — so it is
	// not evidence that the class is broken.
	AcceptancePending Acceptance = iota

	// AcceptanceAccepted means the class's controller claimed it and can serve
	// everything it declares.
	AcceptanceAccepted

	// AcceptanceRejected means the class's controller claimed it and reported
	// that it cannot honor what the class declares. Instances in this class
	// will not run until the catalog or the provider changes.
	AcceptanceRejected
)

// AcceptanceOf reports the controller's verdict on a class, along with the
// message it gave, which is written for a customer to read.
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
