// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// The class names these tests are built on are deliberately invented, and
// deliberately not the ones the platform ships. Nothing in this package may
// behave differently because of what a class is called, and a test written
// against the shipped names could not tell the difference.
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

// TestCatalogDefault covers which tier an instance that selects none runs in.
// The marker on the class is the only source of that answer: a catalog that
// does not say, or says twice, has not stated a default, and is not guessed at
// from any class name.
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

// TestCatalogClaimedBy is how a provider finds the classes it is responsible
// for: by the controller name published on them, never by matching a class name
// it would have to be rebuilt to change.
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

// TestAcceptanceOf separates "no provider has looked at this class yet" from
// "a provider looked and said no". Only the second is grounds for refusing a
// workload at admission; the first is what a provider rollout looks like.
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

// TestCapabilitiesFrom keeps the check an instance is held to sourced from the
// class's own declaration, so the published contract and the enforcement cannot
// drift apart.
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
