// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	"go.datum.net/compute/pkg/instancetypecatalog"
)

const (
	testSelectionTypeAzurite = "azurite"
	testSelectionTypeBasalt  = "basalt"
	testSelectionTypeCitrine = "citrine"
)

// selectionCatalog builds the catalog the gate-on cases validate against. The
// names are invented, so a test cannot pass by matching the hardcoded fallback
// that gate-off validation consults.
func selectionCatalog() instancetypecatalog.Catalog {
	return instancetypecatalog.Catalog{
		selectionType(testSelectionTypeAzurite, computev1alpha.InstanceTypePhaseActive, ""),
		selectionType(testSelectionTypeBasalt, computev1alpha.InstanceTypePhaseDeprecated, testSelectionTypeAzurite),
		selectionType(testSelectionTypeCitrine, computev1alpha.InstanceTypePhaseDisabled, testSelectionTypeAzurite),
	}
}

func selectionType(name string, phase computev1alpha.InstanceTypeLifecyclePhase, replacement string) computev1alpha.InstanceType {
	return computev1alpha.InstanceType{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: computev1alpha.InstanceTypeSpec{
			Resources: computev1alpha.InstanceTypeResources{
				CPU:    resource.MustParse("1000m"),
				Memory: resource.MustParse("1Gi"),
			},
			Lifecycle: computev1alpha.InstanceTypeLifecycle{
				Phase:                   phase,
				ReplacementInstanceType: replacement,
			},
		},
	}
}

// TestValidateInstanceTypeSelectionGateOff verifies how selection is resolved on
// a control plane that has never enabled instance types. The hardcoded catalog
// pkg/instancetype publishes is the only place an instance can run, so a type it
// serves is allowed (the renderer stamps its name) and anything else is not.
func TestValidateInstanceTypeSelectionGateOff(t *testing.T) {
	for name, tc := range map[string]struct {
		selected       string
		old            *computev1alpha.Workload
		expectedErrors field.ErrorList
	}{
		"an empty selection is the legacy fallback": {
			selected: "",
		},
		"the hardcoded fallback type is allowed": {
			selected: "datumcloud-d1-standard-2",
		},
		"a type the hardcoded catalog does not serve is refused": {
			selected:       testSelectionTypeAzurite,
			expectedErrors: field.ErrorList{field.NotSupported(selectionTypePath(), testSelectionTypeAzurite, []string{"datumcloud-d1-standard-2"})},
		},
		"a stored type the hardcoded catalog does not serve stays updatable": {
			selected: testSelectionTypeAzurite,
			old:      storedWorkload(testSelectionTypeAzurite),
		},
	} {
		t.Run(name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.InstanceTypes, false)

			spec := computev1alpha.InstanceSpec{
				Runtime: computev1alpha.InstanceRuntimeSpec{
					Resources: computev1alpha.InstanceRuntimeResources{InstanceType: tc.selected},
				},
			}
			opts := WorkloadValidationOptions{OldWorkload: tc.old}
			root := field.NewPath("spec", "template", "spec")

			cmpErrs(t, tc.expectedErrors, validateInstanceTypeSelection(spec, root, opts))
		})
	}
}

// TestValidateInstanceTypeSelectionGateOn verifies selection against the live
// catalog once instance types are enabled.
func TestValidateInstanceTypeSelectionGateOn(t *testing.T) {
	for name, tc := range map[string]struct {
		selected       string
		catalog        instancetypecatalog.Catalog
		expectedErrors field.ErrorList
	}{
		"an empty selection keeps the hardcoded fallback": {
			selected: "",
			catalog:  selectionCatalog(),
		},
		"an active type is allowed": {
			selected: testSelectionTypeAzurite,
			catalog:  selectionCatalog(),
		},
		"a deprecated type is allowed": {
			selected: testSelectionTypeBasalt,
			catalog:  selectionCatalog(),
		},
		"a disabled type with a replacement is refused with the migration target": {
			selected:       testSelectionTypeCitrine,
			catalog:        selectionCatalog(),
			expectedErrors: field.ErrorList{field.Forbidden(selectionTypePath(), "")},
		},
		"an unpublished type is refused with the published names": {
			selected:       "not-published",
			catalog:        selectionCatalog(),
			expectedErrors: field.ErrorList{field.NotSupported(selectionTypePath(), "not-published", []string{"azurite", "basalt", "citrine"})},
		},
		"a type named on a plane that offers none is refused": {
			selected:       testSelectionTypeAzurite,
			catalog:        instancetypecatalog.Catalog{},
			expectedErrors: field.ErrorList{field.Invalid(selectionTypePath(), testSelectionTypeAzurite, "")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.InstanceTypes, true)

			spec := computev1alpha.InstanceSpec{
				Runtime: computev1alpha.InstanceRuntimeSpec{
					Resources: computev1alpha.InstanceRuntimeResources{InstanceType: tc.selected},
				},
			}
			opts := WorkloadValidationOptions{InstanceTypes: tc.catalog}
			root := field.NewPath("spec", "template", "spec")

			errs := validateInstanceTypeSelection(spec, root, opts)
			cmpErrs(t, tc.expectedErrors, errs)

			if len(tc.expectedErrors) > 0 {
				return
			}
			for _, err := range errs {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestValidateInstanceTypeSelectionDisabledMessage pins the exact rejection text
// the platform announces for a disabled type, because e2e matches on it.
func TestValidateInstanceTypeSelectionDisabledMessage(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.InstanceTypes, true)

	spec := computev1alpha.InstanceSpec{
		Runtime: computev1alpha.InstanceRuntimeSpec{
			Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testSelectionTypeCitrine},
		},
	}
	opts := WorkloadValidationOptions{InstanceTypes: selectionCatalog()}
	root := field.NewPath("spec", "template", "spec")

	errs := validateInstanceTypeSelection(spec, root, opts)
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly one", errs)
	}
	if got := errs[0].Detail; !strings.Contains(got, "InstanceType 'citrine' is disabled; please update your workload to 'azurite'.") {
		t.Errorf("detail = %q, want the disabled migration message", got)
	}

	// A disabled type without a replacement names no target.
	noReplacement := instancetypecatalog.Catalog{
		selectionType(testSelectionTypeCitrine, computev1alpha.InstanceTypePhaseDisabled, ""),
	}
	errs = validateInstanceTypeSelection(spec, root, WorkloadValidationOptions{InstanceTypes: noReplacement})
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly one", errs)
	}
	if got := errs[0].Detail; !strings.Contains(got, "is disabled and lists no replacement") {
		t.Errorf("detail = %q, want the no-replacement disabled message", got)
	}
}

// storedWorkload is a stored object carrying the given instance type, so a test
// can exercise the update tolerance produced by OldWorkload.
func storedWorkload(instanceType string) *computev1alpha.Workload {
	w := &computev1alpha.Workload{}
	w.Spec.Template.Spec.Runtime.Resources.InstanceType = instanceType
	return w
}

// selectionTypePath is the field path a selection rejection is reported under,
// matching the root the validation chain uses.
func selectionTypePath() *field.Path {
	return field.NewPath("spec", "template", "spec", "runtime", "resources", "instanceType")
}
