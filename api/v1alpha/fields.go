package v1alpha

import "k8s.io/apimachinery/pkg/fields"

const (
	// InstanceRuntimeClassField is the field selector path the Instance CRD
	// declares as selectable. The API server rejects a selector on any path a
	// CRD does not declare, so this constant and the selectablefield marker on
	// Instance must name the same path.
	InstanceRuntimeClassField = "spec.runtime.class"
)

// InstanceRuntimeClassFieldSelector returns the field selector a provider uses
// to claim only the Instances whose runtime class it serves. The API server
// applies the selector, so the provider never receives an Instance belonging to
// another class.
//
// Prefer this over the label selector returned by InstanceRuntimeClassSelector.
// A label is a mirror of the spec that some other controller has to write and
// keep current, and an Instance whose label was never written is invisible to
// every provider.
//
// A provider MUST apply the selector to its informer cache through
// cache.Options.ByObject, not only as a controller-runtime event predicate. A
// predicate filters events after the cache has already stored every Instance in
// the cell. That has OOM crash-looped a provider in this system, and a crashed
// provider stops running delete reconciles, which leaves instances stuck in
// Terminating.
//
// The Instance CRD must already declare the field as selectable in every
// control plane the provider connects to. An API server rejects a selector on
// an undeclared field, so the CRD rolls out first.
func InstanceRuntimeClassFieldSelector(class string) fields.Selector {
	return fields.OneTermEqualSelector(InstanceRuntimeClassField, class)
}

// InstanceExcludingRuntimeClassFieldSelector returns the field selector the
// default provider uses to claim the Instances it serves. The default provider
// serves both its own class and every Instance whose class is unset, and an
// unset class reads as the empty string, so an equality selector on the class
// name misses the unset Instances.
//
// Field selector terms combine with AND and support only = and !=, so
// absent-or-equals cannot be written as one selector. The default provider
// instead excludes each class it does not serve, which leaves it matching its
// own class and the empty string:
//
//	InstanceExcludingRuntimeClassFieldSelector("datum-general-purpose")
//
// A provider with more than one class to exclude passes every one of them. The
// result matches an Instance only when the Instance's class is in none of them.
// The caller owns the list, which means the default provider has to learn about
// a newly published class before it stops claiming that class's Instances.
func InstanceExcludingRuntimeClassFieldSelector(classes ...string) fields.Selector {
	requirements := make([]fields.Selector, 0, len(classes))
	for _, class := range classes {
		requirements = append(requirements, fields.OneTermNotEqualSelector(InstanceRuntimeClassField, class))
	}
	return fields.AndSelectors(requirements...)
}
