// SPDX-License-Identifier: AGPL-3.0-only

package instancetype

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestCatalogSizing pins the published sizing. Compute claims quota against
// these exact amounts, so changing one re-prices every instance of that type.
// Update this test only as part of a deliberate product decision.
func TestCatalogSizing(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
		want         Resources
		wantFound    bool
	}{
		{
			name:         "d1-standard-2 is 1 vCPU and 2 GiB",
			instanceType: D1Standard2,
			want:         Resources{CPUMillicores: 1000, MemoryMiB: 2048},
			wantFound:    true,
		},
		{
			name:         "unknown instance type yields no sizing",
			instanceType: "datumcloud-d1-standard-64",
			wantFound:    false,
		},
		{
			name:         "empty instance type yields no sizing",
			instanceType: "",
			wantFound:    false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, found := Lookup(test.instanceType)
			if found != test.wantFound {
				t.Fatalf("Lookup(%q) found = %v, want %v", test.instanceType, found, test.wantFound)
			}
			if delta := cmp.Diff(test.want, got); delta != "" {
				t.Errorf("unexpected sizing (-want +got):\n%s", delta)
			}
		})
	}
}

func TestCatalogName(t *testing.T) {
	if D1Standard2 != "datumcloud-d1-standard-2" {
		t.Errorf("instance type name changed to %q; the name is a customer-facing API value", D1Standard2)
	}
}

func TestNames(t *testing.T) {
	want := []string{D1Standard2}
	if delta := cmp.Diff(want, Names()); delta != "" {
		t.Errorf("unexpected catalog contents (-want +got):\n%s", delta)
	}
}

// TestRetiredName checks that the name the baseline type carried before the
// rename still resolves to the same sizing. Every workload, deployment, and
// instance stored before the rename carries it; if it stopped resolving, their
// quota claims would drop vCPU and memory and their pods would lose sizing.
func TestRetiredName(t *testing.T) {
	if LegacyD1Standard2 != "datumcloud/d1-standard-2" {
		t.Errorf("retired name changed to %q; it must match what stored objects carry", LegacyD1Standard2)
	}
	if got := Canonical(LegacyD1Standard2); got != D1Standard2 {
		t.Errorf("Canonical(%q) = %q, want %q", LegacyD1Standard2, got, D1Standard2)
	}
	if got := Canonical("datumcloud-unknown"); got != "datumcloud-unknown" {
		t.Errorf("Canonical must return an unknown name unchanged, got %q", got)
	}

	legacy, ok := Lookup(LegacyD1Standard2)
	if !ok {
		t.Fatalf("Lookup(%q) must resolve", LegacyD1Standard2)
	}
	current, _ := Lookup(D1Standard2)
	if legacy != current {
		t.Errorf("retired name sized %+v, want the same as %q: %+v", legacy, D1Standard2, current)
	}
}

// TestLookupDoesNotExposeMutableCatalog checks that Lookup returns a copy. A
// consumer that edited the returned value in place would change platform sizing
// for every later caller in the process.
func TestLookupDoesNotExposeMutableCatalog(t *testing.T) {
	sizing, _ := Lookup(D1Standard2)
	sizing.MemoryMiB = 1

	again, _ := Lookup(D1Standard2)
	if again.MemoryMiB != 2048 {
		t.Errorf("catalog sizing was mutated through Lookup: memory is now %d MiB", again.MemoryMiB)
	}
}
