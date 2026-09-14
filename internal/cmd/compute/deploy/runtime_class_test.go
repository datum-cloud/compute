// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"reflect"
	"strings"
	"testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	classGeneralPurpose = "general-purpose"
	classUnikernel      = "unikernel"
)

// storedWorkload is a workload as the control plane returns it after
// admission, with the runtime class and the interface defaults recorded.
func storedWorkload(class string) *computev1alpha.Workload {
	w := workloadWithPorts()
	w.Spec.Template.Spec.Runtime.Resources = computev1alpha.InstanceRuntimeResources{InstanceType: "datumcloud/d1-standard-2"}
	w.Spec.Template.Spec.Runtime.Class = class

	created, _ := resolveNetworkInterfaces(workload(), true, "")
	iface := created[0]
	iface.Name = "eth0"
	iface.IPFamilies = []networkingv1alpha.IPFamily{networkingv1alpha.IPv6Protocol}
	iface.ReclaimPolicy = "Delete"
	w.Spec.Template.Spec.NetworkInterfaces = []computev1alpha.InstanceNetworkInterface{iface}
	return &w
}

// TestResolveRuntimeClass covers the rule that a workload keeps its tier for
// life. A redeploy that names no class must not let admission substitute the
// catalog default, and a redeploy that names a different class is refused
// before anything is written.
func TestResolveRuntimeClass(t *testing.T) {
	tests := []struct {
		name      string
		existing  *computev1alpha.Workload
		creating  bool
		requested string
		want      string
		wantErr   []string
	}{
		{
			name:      "create sets the requested class",
			existing:  workload(),
			creating:  true,
			requested: classGeneralPurpose,
			want:      classGeneralPurpose,
		},
		{
			name:     "create without a class leaves it to the platform default",
			existing: workload(),
			creating: true,
		},
		{
			name:     "update without a class keeps the existing one",
			existing: storedWorkload(classGeneralPurpose),
			want:     classGeneralPurpose,
		},
		{
			name:      "update with the same class proceeds",
			existing:  storedWorkload(classGeneralPurpose),
			requested: classGeneralPurpose,
			want:      classGeneralPurpose,
		},
		{
			name:      "update with a different class is refused",
			existing:  storedWorkload(classGeneralPurpose),
			requested: classUnikernel,
			wantErr: []string{
				`"` + classGeneralPurpose + `"`, `"` + classUnikernel + `"`,
				"cannot be changed", "datumctl compute destroy " + testWorkload, "new name",
			},
		},
		{
			name:      "update of a workload with no recorded class defers to admission",
			existing:  storedWorkload(""),
			requested: classUnikernel,
			want:      classUnikernel,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRuntimeClass(tc.existing, tc.creating, tc.requested)
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatalf("want an error, got class %q", got)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error does not mention %q: %v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("want no error, got %v", err)
			}
			if got != tc.want {
				t.Fatalf("runtime class = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveNetworkInterfacesKeepsImmutableFields covers the interface fields
// the control plane treats as immutable. A redeploy that reset them to their
// defaults would be rejected.
func TestResolveNetworkInterfacesKeepsImmutableFields(t *testing.T) {
	existing := storedWorkload(classGeneralPurpose)
	existing.Spec.Template.Spec.NetworkInterfaces[0].IPFamilies = []networkingv1alpha.IPFamily{networkingv1alpha.IPv4Protocol}

	got, err := resolveNetworkInterfaces(existing, false, "")
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if !reflect.DeepEqual(got, existing.Spec.Template.Spec.NetworkInterfaces) {
		t.Errorf("interfaces = %+v, want the stored ones %+v", got, existing.Spec.Template.Spec.NetworkInterfaces)
	}

	// A new workload gets the single default interface and leaves the
	// immutable fields for the control plane to default.
	created, err := resolveNetworkInterfaces(workload(), true, "")
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if len(created) != 1 || created[0].Name != "" || created[0].IPFamilies != nil || created[0].Addresses != nil {
		t.Errorf("interfaces on create = %+v, want one interface with only its network set", created)
	}
}

// TestRuntimeClassFlag checks that the flag is registered and follows the -f
// convention of the other spec-shaping flags, where a manifest declares its
// own class.
func TestRuntimeClassFlag(t *testing.T) {
	cmd, opts := command()
	if cmd.Flags().Lookup("runtime-class") == nil {
		t.Fatal("--runtime-class must be registered")
	}
	if err := cmd.Flags().Parse([]string{"--file=manifest.yaml", "--runtime-class=" + classUnikernel}); err != nil {
		t.Fatalf("parsing flags: %v", err)
	}
	err := validateFlags(cmd, opts)
	if err == nil || !strings.Contains(err.Error(), "--runtime-class cannot be combined with -f") {
		t.Fatalf("want the -f conflict error, got %v", err)
	}
}
