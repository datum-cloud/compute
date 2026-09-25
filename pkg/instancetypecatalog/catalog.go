// SPDX-License-Identifier: AGPL-3.0-only

package instancetypecatalog

import (
	"fmt"
	"sort"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Catalog is the set of instance types a control plane offers. A Catalog is a
// slice of objects the caller already read, so no method here needs a client.
// The compute webhook builds one from the project control plane it admits into,
// and a controller builds one from the cluster it watches. Both then get the
// same answers from the same methods.
//
// Unlike pkg/runtimeclass, the catalog publishes no default instance type:
// customers must name the tier they want, and a workload that omits instanceType
// keeps the platform's hardcoded fallback for backwards compatibility.
type Catalog []computev1alpha.InstanceType

// Find returns the instance type published under the given name, or nil when
// the catalog does not offer that name.
func (c Catalog) Find(name string) *computev1alpha.InstanceType {
	for i := range c {
		if c[i].Name == name {
			return &c[i]
		}
	}
	return nil
}

// Names returns the published instance type names in sorted order. Callers show
// the list as the supported values when a customer names a type the catalog
// does not offer.
func (c Catalog) Names() []string {
	names := make([]string, 0, len(c))
	for i := range c {
		names = append(names, c[i].Name)
	}
	sort.Strings(names)
	return names
}

// DeprecatedMessage renders the migration guidance announced for a deprecated
// instance type, naming the replacement when the type lists one. The admission
// webhook warns with this text and the workload controller reports it as the
// InstanceTypeDeprecated condition message, so the wording is defined once.
func DeprecatedMessage(name, replacement string) string {
	if len(replacement) == 0 {
		return fmt.Sprintf("InstanceType '%s' is deprecated.", name)
	}
	return fmt.Sprintf("InstanceType '%s' is deprecated; please migrate to '%s'.", name, replacement)
}

// DisabledMessage renders the rejection text announced for a disabled instance
// type, naming the replacement when the type lists one. Workload admission
// refuses a disabled type with this text (e2e matches on it) and the workload
// controller reports it as the InstanceTypeDisabled condition message, so the
// wording is defined once.
func DisabledMessage(name, replacement string) string {
	if len(replacement) == 0 {
		return fmt.Sprintf("InstanceType '%s' is disabled and lists no replacement; please update your workload to another instance type", name)
	}
	return fmt.Sprintf("InstanceType '%s' is disabled; please update your workload to '%s'.", name, replacement)
}

// LifecycleRecommendedMessage renders the migration guidance for whichever
// lifecycle phase a type is in. It is the union of DeprecatedMessage and
// DisabledMessage, chosen by phase; an active type renders an empty string.
func LifecycleRecommendedMessage(phase computev1alpha.InstanceTypeLifecyclePhase, name, replacement string) string {
	switch phase {
	case computev1alpha.InstanceTypePhaseDeprecated:
		return DeprecatedMessage(name, replacement)
	case computev1alpha.InstanceTypePhaseDisabled:
		return DisabledMessage(name, replacement)
	}
	return ""
}
