// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// These tests use invented class names rather than the names the platform
// ships. No code in this package may behave differently based on a class name,
// and tests written against the shipped names could not detect such a
// dependency.
const (
	testClassAzurite = "azurite"
	testClassBasalt  = "basalt"
	testClassCitrine = "citrine"
)

func class(name string, isDefault bool) computev1alpha.RuntimeClass {
	return computev1alpha.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       computev1alpha.RuntimeClassSpec{Default: isDefault},
	}
}

func TestCatalogFind(t *testing.T) {
	catalog := Catalog{class(testClassAzurite, true), class(testClassBasalt, false)}

	if got := catalog.Find(testClassBasalt); got == nil || got.Name != testClassBasalt {
		t.Errorf("Find(basalt) = %v, want the published class", got)
	}
	if got := catalog.Find(testClassCitrine); got != nil {
		t.Errorf("Find(citrine) = %v, want nil for a class the catalog does not offer", got)
	}
}

// TestCatalogDefault covers which class an instance runs in when it selects
// none. The default marker on a class is the only source of the answer. A
// catalog with zero or several marked classes has not stated a default, and
// Default does not infer one from a class name.
func TestCatalogDefault(t *testing.T) {
	cases := map[string]struct {
		catalog Catalog
		want    string
	}{
		"the class marking itself default": {
			catalog: Catalog{class(testClassAzurite, false), class(testClassBasalt, true)},
			want:    testClassBasalt,
		},
		"no class marks itself": {
			catalog: Catalog{class(testClassAzurite, false), class(testClassCitrine, false)},
			want:    "",
		},
		"several classes mark themselves": {
			catalog: Catalog{class(testClassAzurite, true), class(testClassCitrine, true)},
			want:    "",
		},
		"the only class marks itself": {
			catalog: Catalog{class(testClassCitrine, true)},
			want:    testClassCitrine,
		},
		"the only class does not": {
			catalog: Catalog{class(testClassCitrine, false)},
			want:    "",
		},
		"an empty catalog": {
			want: "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ""
			if resolved := tc.catalog.Default(); resolved != nil {
				got = resolved.Name
			}
			if got != tc.want {
				t.Errorf("Default() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCatalogNames(t *testing.T) {
	catalog := Catalog{class(testClassAzurite, true), class(testClassBasalt, false)}

	names := catalog.Names()
	if len(names) != 2 || names[0] != testClassAzurite || names[1] != testClassBasalt {
		t.Errorf("Names() = %v, want the published classes in sorted order", names)
	}
}

// TestCatalogClaimedBy covers how a provider finds the classes it is
// responsible for. Selection uses the controller name published on each class,
// never a match against a class name.
func TestCatalogClaimedBy(t *testing.T) {
	fastPath := class(testClassAzurite, true)
	fastPath.Spec.ControllerName = "compute.datumapis.com/unikraft-provider"
	general := class(testClassBasalt, false)
	general.Spec.ControllerName = "compute.datumapis.com/kata-provider"

	claimed := Catalog{fastPath, general}.ClaimedBy("compute.datumapis.com/kata-provider")
	if len(claimed) != 1 || claimed[0].Name != testClassBasalt {
		t.Errorf("ClaimedBy() = %v, want only the classes naming that controller", claimed.Names())
	}

	if claimed := (Catalog{fastPath}).ClaimedBy("compute.datumapis.com/kata-provider"); len(claimed) != 0 {
		t.Errorf("ClaimedBy() = %v, want nothing claimed", claimed.Names())
	}
}

// TestAcceptanceOf separates a class no provider has reported on yet from a
// class a provider reported it cannot serve. Only the second justifies refusing
// a workload at admission. The first also occurs during a provider rollout.
func TestAcceptanceOf(t *testing.T) {
	accepted := func(status metav1.ConditionStatus, message string) *computev1alpha.RuntimeClass {
		c := class(testClassCitrine, false)
		c.Status.Conditions = []metav1.Condition{{
			Type:    computev1alpha.RuntimeClassConditionAccepted,
			Status:  status,
			Reason:  computev1alpha.RuntimeClassReasonAccepted,
			Message: message,
		}}
		return &c
	}
	unreported := class(testClassCitrine, false)

	cases := map[string]struct {
		class       *computev1alpha.RuntimeClass
		want        Acceptance
		wantMessage string
	}{
		"no condition yet": {class: &unreported, want: AcceptancePending},
		"no class at all":  {want: AcceptancePending},
		"claimed and honored": {
			class: accepted(metav1.ConditionTrue, "serving"), want: AcceptanceAccepted, wantMessage: "serving",
		},
		"claimed and refused": {
			class: accepted(metav1.ConditionFalse, "no disks"), want: AcceptanceRejected, wantMessage: "no disks",
		},
		"still deciding": {
			class: accepted(metav1.ConditionUnknown, "waiting"), want: AcceptancePending, wantMessage: "waiting",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, message := AcceptanceOf(tc.class)
			if got != tc.want {
				t.Errorf("AcceptanceOf() = %v, want %v", got, tc.want)
			}
			if message != tc.wantMessage {
				t.Errorf("message = %q, want %q", message, tc.wantMessage)
			}
		})
	}
}

// TestCapabilitiesFrom checks that validation reads a class's own declaration,
// so the published contract and the enforced check cannot drift apart.
func TestCapabilitiesFrom(t *testing.T) {
	published := class(testClassCitrine, false)
	published.Spec.Capabilities.Features = []computev1alpha.RuntimeClassFeature{FeatureSandboxRuntime}

	capabilities := CapabilitiesFrom(&published)
	if capabilities.Class != testClassCitrine {
		t.Errorf("Class = %q, want the class the instance selected", capabilities.Class)
	}
	if !capabilities.Supports(FeatureSandboxRuntime) {
		t.Error("a declared feature should be served")
	}
	if capabilities.Supports(FeatureDiskVolumes) {
		t.Error("a feature the class did not declare must not be served")
	}

	if got := CapabilitiesFrom(nil); len(got.Features) != 0 {
		t.Errorf("CapabilitiesFrom(nil) = %v, want nothing served", got)
	}
}
