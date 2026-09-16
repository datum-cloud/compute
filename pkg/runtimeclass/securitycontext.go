// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"slices"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// DefaultSecurityContext returns the security context to store on a sandbox
// container, given what the customer stated and what the class publishes as its
// default.
//
// The platform chooses a security configuration for every container, and a
// container that fails to start because of that choice shows nothing but its own
// logs. Writing the choice onto the stored container is what makes it readable:
// a customer sees the capabilities, privilege escalation, and seccomp profile
// their container runs with, and a provider runs only what the container states.
//
// A customer statement is the whole answer for the capabilities it covers.
// Merging the class default into a stated capability set would put back the
// invisible injection this replaces, so a container that states capabilities
// keeps exactly those. Privilege escalation and the seccomp profile default
// independently, because each is a separate statement.
//
// A capability set holding neither an add nor a drop states nothing, so the two
// ways of writing that, an absent field and an empty object, grant the same
// capabilities. Writing only a drop does state something, and a container that
// drops ALL receives no default grant, which is the only way to ask for no
// capabilities at all.
//
// The returned value is a copy, and calling DefaultSecurityContext on its own
// result returns an equal value, so repeated admission of an unchanged workload
// stores an unchanged object.
func DefaultSecurityContext(
	class *computev1alpha.RuntimeClass,
	stated *computev1alpha.SandboxSecurityContext,
) *computev1alpha.SandboxSecurityContext {
	if class == nil {
		return stated.DeepCopy()
	}

	defaults := class.Spec.DefaultSecurityContext

	defaulted := stated.DeepCopy()
	if defaulted == nil {
		defaulted = &computev1alpha.SandboxSecurityContext{}
	}

	switch {
	case statesNoCapabilities(defaulted.Capabilities):
		defaulted.Capabilities = &computev1alpha.SandboxCapabilities{
			Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
			Add:  defaultCapabilityAdds(defaults),
		}

	// Every sandbox container drops ALL regardless of what it states, so the
	// floor is recorded even on a container that lists its own capabilities.
	// Leaving it off exactly there would hide it from the customer most likely
	// to be reading it.
	case !slices.Contains(defaulted.Capabilities.Drop, computev1alpha.CapabilityAll):
		defaulted.Capabilities.Drop = append(defaulted.Capabilities.Drop, computev1alpha.CapabilityAll)
	}

	if defaults != nil {
		if defaulted.AllowPrivilegeEscalation == nil && defaults.AllowPrivilegeEscalation != nil {
			allowed := *defaults.AllowPrivilegeEscalation
			defaulted.AllowPrivilegeEscalation = &allowed
		}

		if defaulted.SeccompProfile == nil && defaults.SeccompProfile != nil {
			defaulted.SeccompProfile = defaults.SeccompProfile.DeepCopy()
		}
	}

	return defaulted
}

// statesNoCapabilities reports whether a capability set asks for nothing. An
// absent set and an empty one are the same statement, so they must not produce
// different capabilities.
func statesNoCapabilities(capabilities *computev1alpha.SandboxCapabilities) bool {
	return capabilities == nil || (len(capabilities.Add) == 0 && len(capabilities.Drop) == 0)
}

// defaultCapabilityAdds returns the class's granted-by-default capabilities,
// sorted and deduplicated so an unchanged class stores an unchanged list.
func defaultCapabilityAdds(defaults *computev1alpha.RuntimeClassSecurityContext) []computev1alpha.Capability {
	if defaults == nil || defaults.Capabilities == nil || len(defaults.Capabilities.Add) == 0 {
		return nil
	}

	add := slices.Clone(defaults.Capabilities.Add)
	slices.Sort(add)
	return slices.Compact(add)
}
