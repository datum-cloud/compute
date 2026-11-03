// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"testing"
)

func TestInstanceTypesListOffersOnlyWhatValidationAccepts(t *testing.T) {
	deps := fixtureDeps(fixtureReader())

	_, out, err := instanceTypesList(deps)(context.Background(), nil, InstanceTypesListInput{})
	if err != nil {
		t.Fatalf("compute_instance_types_list: %v", err)
	}
	if len(out.InstanceTypes) == 0 {
		t.Fatal("no instance types offered; a model with no catalog invents one")
	}

	var defaults int
	for _, it := range out.InstanceTypes {
		if it.Default {
			defaults++
		}
		// A type with no size is worse than useless: it invites a replica count
		// chosen against nothing.
		if it.VCPU <= 0 || it.MemoryMiB <= 0 {
			t.Errorf("%s = %g vCPU / %d MiB, want a real size", it.Name, it.VCPU, it.MemoryMiB)
		}
	}
	if defaults != 1 {
		t.Errorf("got %d default instance types, want exactly 1", defaults)
	}

	// The one supported type today, with the sizing quota is accounted against.
	first := out.InstanceTypes[0]
	if first.Name != "datumcloud-d1-standard-2" || first.VCPU != 1 || first.MemoryMiB != 2048 {
		t.Errorf("first type = %+v, want datumcloud-d1-standard-2 at 1 vCPU / 2048 MiB", first)
	}
}

// TestInstanceTypesListFailsWhenDepsAreUnavailable: the catalog alone could
// answer, but an unauthenticated caller is still turned away so the tool is not
// a probe.
func TestInstanceTypesListFailsWhenDepsAreUnavailable(t *testing.T) {
	wantErr := errors.New("no credentials on this request")
	denied := DepsFor(func(context.Context) (ToolDeps, error) { return ToolDeps{}, wantErr })

	if _, _, err := instanceTypesList(denied)(context.Background(), nil, InstanceTypesListInput{}); !errors.Is(err, wantErr) {
		t.Errorf("compute_instance_types_list error = %v, want the deps error to surface unchanged", err)
	}
}
