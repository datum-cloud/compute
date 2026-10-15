// SPDX-License-Identifier: AGPL-3.0-only

package instancetypecatalog

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/pkg/instancetype"
)

func namedType(name string) computev1alpha.InstanceType {
	return computev1alpha.InstanceType{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// TestCatalogFind covers resolving a published type by name, the lookup the
// admission webhook and controller drive.
func TestCatalogFind(t *testing.T) {
	catalog := Catalog{namedType("azurite"), namedType("basalt")}

	if got := catalog.Find("basalt"); got == nil || got.Name != "basalt" {
		t.Errorf("Find(basalt) = %v, want the basalt entry", got)
	}
	if got := catalog.Find("not-published"); got != nil {
		t.Errorf("Find(not-published) = %v, want nil", got)
	}
}

// TestCatalogFindRetiredName checks that a name retired by a rename finds the
// type published under the name that replaced it. An object name cannot hold
// the retired name's '/', so without this a workload stored before the rename
// could never match its type.
func TestCatalogFindRetiredName(t *testing.T) {
	catalog := Catalog{namedType(instancetype.D1Standard2)}

	if got := catalog.Find(instancetype.LegacyD1Standard2); got == nil || got.Name != instancetype.D1Standard2 {
		t.Errorf("Find(%q) = %v, want the %s entry", instancetype.LegacyD1Standard2, got, instancetype.D1Standard2)
	}
}

// TestCatalogNames verifies the published names come back sorted, so rejection
// messages read the same way on every plane.
func TestCatalogNames(t *testing.T) {
	catalog := Catalog{namedType("citrine"), namedType("azurite"), namedType("basalt")}

	got := catalog.Names()
	want := []string{"azurite", "basalt", "citrine"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}

	if len((Catalog{}).Names()) != 0 {
		t.Error("Names() of an empty catalog = non-empty, want empty")
	}
}
