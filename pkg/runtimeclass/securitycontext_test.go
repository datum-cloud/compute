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
			Add: []computev1alpha.Capability{"SETGID", "CHOWN", "CHOWN"},
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
		"a published default is written onto a container that states nothing": {
			class: classWithDefaults(full),
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
					Add:  []computev1alpha.Capability{"CHOWN", "SETGID"},
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
			Add: []computev1alpha.Capability{"SETGID", "CHOWN"},
		},
		SeccompProfile: &computev1alpha.SandboxSeccompProfile{
			Type: computev1alpha.SeccompProfileTypeRuntimeDefault,
		},
	})
	before := class.DeepCopy()

	stated := &computev1alpha.SandboxSecurityContext{
		Capabilities: &computev1alpha.SandboxCapabilities{
			Add: []computev1alpha.Capability{"NET_BIND_SERVICE"},
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
