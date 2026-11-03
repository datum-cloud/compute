// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

func classWithDefaults(defaults *computev1alpha.RuntimeClassSecurityContext) *computev1alpha.RuntimeClass {
	return &computev1alpha.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "basalt"},
		Spec:       computev1alpha.RuntimeClassSpec{DefaultSecurityContext: defaults},
	}
}

func boolRef(value bool) *bool { return &value }

// TestDefaultSecurityContext covers the shared answer compute and every provider
// read, so a container is defaulted the same way wherever the question is asked.
func TestDefaultSecurityContext(t *testing.T) {
	runtimeDefault := &computev1alpha.SandboxSeccompProfile{
		Type: computev1alpha.SeccompProfileTypeRuntimeDefault,
	}

	full := &computev1alpha.RuntimeClassSecurityContext{
		Capabilities: &computev1alpha.RuntimeClassDefaultCapabilities{
			Add: []computev1alpha.Capability{testCapSetgid, testCapChown, testCapChown},
		},
		AllowPrivilegeEscalation: boolRef(true),
		SeccompProfile:           runtimeDefault,
	}

	tests := map[string]struct {
		class  *computev1alpha.RuntimeClass
		stated *computev1alpha.SandboxSecurityContext
		want   *computev1alpha.SandboxSecurityContext
	}{
		"no class leaves the container as stated": {
			class:  nil,
			stated: nil,
			want:   nil,
		},
		"no class copies rather than aliases what was stated": {
			class: nil,
			stated: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Add: []computev1alpha.Capability{testCapChown},
				},
			},
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Add: []computev1alpha.Capability{testCapChown},
				},
			},
		},
		"a published default is written onto a container that states nothing": {
			class: classWithDefaults(full),
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
					Add:  []computev1alpha.Capability{testCapChown, testCapSetgid},
				},
				AllowPrivilegeEscalation: boolRef(true),
				SeccompProfile:           runtimeDefault,
			},
		},
		"an empty security context still records the capability floor": {
			class:  classWithDefaults(nil),
			stated: &computev1alpha.SandboxSecurityContext{},
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
				},
			},
		},
		"a container stating adds still records the capability floor": {
			class: classWithDefaults(full),
			stated: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Add: []computev1alpha.Capability{testCapNetBindService},
				},
			},
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Add:  []computev1alpha.Capability{testCapNetBindService},
					Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
				},
				AllowPrivilegeEscalation: boolRef(true),
				SeccompProfile:           runtimeDefault,
			},
		},
		"an empty capability set states nothing and takes the default": {
			class: classWithDefaults(full),
			stated: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{},
			},
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
					Add:  []computev1alpha.Capability{testCapChown, testCapSetgid},
				},
				AllowPrivilegeEscalation: boolRef(true),
				SeccompProfile:           runtimeDefault,
			},
		},
		"a container dropping one capability keeps it and gains the floor": {
			class: classWithDefaults(full),
			stated: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Drop: []computev1alpha.Capability{testCapNetRaw},
				},
			},
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Drop: []computev1alpha.Capability{testCapNetRaw, computev1alpha.CapabilityAll},
				},
				AllowPrivilegeEscalation: boolRef(true),
				SeccompProfile:           runtimeDefault,
			},
		},
		"a stated capability set is the whole answer": {
			class: classWithDefaults(full),
			stated: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
				},
			},
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
				},
				AllowPrivilegeEscalation: boolRef(true),
				SeccompProfile:           runtimeDefault,
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := DefaultSecurityContext(test.class, test.stated)
			if delta := cmp.Diff(test.want, got); delta != "" {
				t.Errorf("DefaultSecurityContext() mismatch (-want +got):\n%s", delta)
			}

			// Admission runs on every update, so a second pass over its own
			// result must not move the stored object.
			if delta := cmp.Diff(got, DefaultSecurityContext(test.class, got)); delta != "" {
				t.Errorf("DefaultSecurityContext() is not idempotent (-first +second):\n%s", delta)
			}
		})
	}
}

// TestDefaultSecurityContextDoesNotMutateInput checks that defaulting leaves the
// caller's value alone, so a class or a stored container is never altered in
// place by asking what it would be defaulted to.
func TestDefaultSecurityContextDoesNotMutateInput(t *testing.T) {
	class := classWithDefaults(&computev1alpha.RuntimeClassSecurityContext{
		Capabilities: &computev1alpha.RuntimeClassDefaultCapabilities{
			Add: []computev1alpha.Capability{testCapSetgid, testCapChown},
		},
		SeccompProfile: &computev1alpha.SandboxSeccompProfile{
			Type: computev1alpha.SeccompProfileTypeRuntimeDefault,
		},
	})
	before := class.DeepCopy()

	stated := &computev1alpha.SandboxSecurityContext{
		Capabilities: &computev1alpha.SandboxCapabilities{
			Add: []computev1alpha.Capability{testCapNetBindService},
		},
	}
	statedBefore := stated.DeepCopy()

	DefaultSecurityContext(class, stated)

	if delta := cmp.Diff(before, class); delta != "" {
		t.Errorf("the class was modified (-before +after):\n%s", delta)
	}
	if delta := cmp.Diff(statedBefore, stated); delta != "" {
		t.Errorf("the stated security context was modified (-before +after):\n%s", delta)
	}
}

// TestDefaultSecurityContextReturnsACopy checks that the result never aliases
// what the caller passed in, including when no class resolves. A caller that
// stored the result would otherwise be holding the same memory as the object it
// read it from.
func TestDefaultSecurityContextReturnsACopy(t *testing.T) {
	stated := &computev1alpha.SandboxSecurityContext{
		Capabilities: &computev1alpha.SandboxCapabilities{
			Add: []computev1alpha.Capability{testCapChown},
		},
	}

	for name, class := range map[string]*computev1alpha.RuntimeClass{
		"with no class": nil,
		"with a class":  classWithDefaults(nil),
	} {
		t.Run(name, func(t *testing.T) {
			got := DefaultSecurityContext(class, stated)
			if got == stated {
				t.Fatal("the result aliases the stated security context")
			}
			if got.Capabilities == stated.Capabilities {
				t.Fatal("the result aliases the stated capabilities")
			}
		})
	}
}
