// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	"go.datum.net/compute/pkg/runtimeclass"
)

// These tests use invented class names rather than the names the platform
// ships. Resolution must run entirely off the catalog, and a test written
// against the shipped names could not distinguish a catalog lookup from a
// compiled-in one.
const (
	// testClassAzurite is the tier a test catalog marks as default.
	testClassAzurite = "azurite"

	// testClassBasalt is a published tier that is not the default.
	testClassBasalt = "basalt"

	// testUnpublishedClass is a class name that a test catalog publishes only
	// when the test says so. It represents a tier that does not exist.
	testUnpublishedClass = "citrine"
)

// makeRuntimeClass builds a catalog entry that serves every capability, so a
// test only has to state the part of the contract it exercises.
func makeRuntimeClass(name string, tweaks ...func(*computev1alpha.RuntimeClass)) computev1alpha.RuntimeClass {
	class := computev1alpha.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: computev1alpha.RuntimeClassSpec{
			ControllerName: "compute.datumapis.com/test-provider",
			Isolation:      computev1alpha.RuntimeClassIsolation{Boundary: "test"},
			Capabilities: computev1alpha.RuntimeClassCapabilities{
				Features: []computev1alpha.RuntimeClassFeature{
					computev1alpha.RuntimeClassFeatureSandboxRuntime,
					computev1alpha.RuntimeClassFeatureVirtualMachineRuntime,
					computev1alpha.RuntimeClassFeatureConfigMapVolumes,
					computev1alpha.RuntimeClassFeatureSecretVolumes,
					computev1alpha.RuntimeClassFeatureDiskVolumes,
					computev1alpha.RuntimeClassFeatureDeviceVolumeAttachments,
					computev1alpha.RuntimeClassFeatureEnvFrom,
					computev1alpha.RuntimeClassFeatureImagePullSecrets,
				},
			},
		},
	}
	for _, tweak := range tweaks {
		tweak(&class)
	}
	return class
}

func withDefault(class *computev1alpha.RuntimeClass) { class.Spec.Default = true }

func withAccepted(status metav1.ConditionStatus, reason, message string) func(*computev1alpha.RuntimeClass) {
	return func(class *computev1alpha.RuntimeClass) {
		class.Status.Conditions = []metav1.Condition{{
			Type:    computev1alpha.RuntimeClassConditionAccepted,
			Status:  status,
			Reason:  reason,
			Message: message,
		}}
	}
}

func withFeatures(featureList ...computev1alpha.RuntimeClassFeature) func(*computev1alpha.RuntimeClass) {
	return func(class *computev1alpha.RuntimeClass) {
		class.Spec.Capabilities.Features = featureList
	}
}

// defaultCatalog matches a bootstrapped control plane: one default tier and one
// other tier.
func defaultCatalog() runtimeclass.Catalog {
	return runtimeclass.Catalog{
		makeRuntimeClass(testClassAzurite, withDefault),
		makeRuntimeClass(testClassBasalt),
	}
}

// TestValidateRuntimeClassSelectionGateOff verifies that a control plane with
// the gate off rejects every tier selection and never reads the catalog, so
// publishing a catalog changes nothing until the gate is on.
func TestValidateRuntimeClassSelectionGateOff(t *testing.T) {
	root := field.NewPath("spec", "template", "spec")
	classPath := root.Child("runtime", "class")

	cases := map[string]struct {
		class          string
		catalog        runtimeclass.Catalog
		expectedErrors field.ErrorList
	}{
		"unset selects nothing": {},
		"unset with a catalog published": {
			catalog: defaultCatalog(),
		},
		"the class a catalog marks default is still refused": {
			class:          testClassAzurite,
			catalog:        defaultCatalog(),
			expectedErrors: field.ErrorList{field.Forbidden(classPath, "")},
		},
		"a published class is refused": {
			class:          testClassBasalt,
			catalog:        defaultCatalog(),
			expectedErrors: field.ErrorList{field.Forbidden(classPath, "")},
		},
		"an unpublished class is refused": {
			class:          testUnpublishedClass,
			expectedErrors: field.ErrorList{field.Forbidden(classPath, "")},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, false)

			spec := computev1alpha.InstanceSpec{
				Runtime: computev1alpha.InstanceRuntimeSpec{
					Class:   tc.class,
					Sandbox: &computev1alpha.SandboxRuntime{},
				},
			}
			opts := WorkloadValidationOptions{RuntimeClasses: tc.catalog}

			cmpErrs(t, tc.expectedErrors, validateRuntimeClassSelection(spec, root, opts))
		})
	}
}

// TestValidateRuntimeClassSelection verifies that validation accepts a class
// because the catalog publishes it, not because the name is compiled in.
func TestValidateRuntimeClassSelection(t *testing.T) {
	root := field.NewPath("spec", "template", "spec")
	classPath := root.Child("runtime", "class")

	cases := map[string]struct {
		class          string
		catalog        runtimeclass.Catalog
		expectedErrors field.ErrorList
	}{
		"a published class is accepted": {
			class:   testClassBasalt,
			catalog: defaultCatalog(),
		},
		"a class published only in the catalog is accepted": {
			class:   testUnpublishedClass,
			catalog: runtimeclass.Catalog{makeRuntimeClass(testUnpublishedClass)},
		},
		"an unpublished class names what is available": {
			class:          testUnpublishedClass,
			catalog:        defaultCatalog(),
			expectedErrors: field.ErrorList{field.NotSupported(classPath, testUnpublishedClass, []string{})},
		},
		"an empty catalog cannot run anything": {
			class:          testClassAzurite,
			expectedErrors: field.ErrorList{field.Invalid(classPath, "", "")},
		},
		"a class whose controller refused it is turned down": {
			class: testClassBasalt,
			catalog: runtimeclass.Catalog{
				makeRuntimeClass(testClassBasalt,
					withAccepted(metav1.ConditionFalse, computev1alpha.RuntimeClassReasonUnsupportedFeature, "no")),
			},
			expectedErrors: field.ErrorList{field.Forbidden(classPath, "")},
		},
		"a class no controller has reported on yet is admitted": {
			class: testClassBasalt,
			catalog: runtimeclass.Catalog{
				makeRuntimeClass(testClassBasalt,
					withAccepted(metav1.ConditionUnknown, computev1alpha.RuntimeClassReasonPending, "waiting")),
			},
		},
		"a class its controller accepted is admitted": {
			class: testClassBasalt,
			catalog: runtimeclass.Catalog{
				makeRuntimeClass(testClassBasalt,
					withAccepted(metav1.ConditionTrue, computev1alpha.RuntimeClassReasonAccepted, "")),
			},
		},
		"an unselected class with no default published is refused": {
			catalog:        runtimeclass.Catalog{makeRuntimeClass(testUnpublishedClass)},
			expectedErrors: field.ErrorList{field.Invalid(classPath, "", "")},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, true)

			spec := computev1alpha.InstanceSpec{
				Runtime: computev1alpha.InstanceRuntimeSpec{
					Class:   tc.class,
					Sandbox: &computev1alpha.SandboxRuntime{},
				},
			}
			opts := WorkloadValidationOptions{RuntimeClasses: tc.catalog}

			cmpErrs(t, tc.expectedErrors, validateRuntimeClassSelection(spec, root, opts))
		})
	}
}

// TestValidateRuntimeClassCapabilities verifies that the same instance is
// accepted or rejected based only on the capabilities the selected class
// publishes, and that the rejection names the selected class.
func TestValidateRuntimeClassCapabilities(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, true)

	root := field.NewPath("spec", "template", "spec")
	spec := computev1alpha.InstanceSpec{
		Runtime: computev1alpha.InstanceRuntimeSpec{
			Class:   "fast-path",
			Sandbox: &computev1alpha.SandboxRuntime{},
		},
		Volumes: []computev1alpha.InstanceVolume{{
			Name: "data",
			VolumeSource: computev1alpha.VolumeSource{
				Disk: &computev1alpha.DiskTemplateVolumeSource{},
			},
		}},
	}

	serving := runtimeclass.Catalog{makeRuntimeClass("fast-path")}
	if errs := validateRuntimeClassSelection(spec, root, WorkloadValidationOptions{RuntimeClasses: serving}); len(errs) > 0 {
		t.Fatalf("a class declaring disk-backed volumes should serve them, got: %v", errs)
	}

	declining := runtimeclass.Catalog{
		makeRuntimeClass("fast-path", withFeatures(computev1alpha.RuntimeClassFeatureSandboxRuntime)),
	}
	errs := validateRuntimeClassSelection(spec, root, WorkloadValidationOptions{RuntimeClasses: declining})
	if len(errs) != 1 {
		t.Fatalf("expected the disk volume to be refused once, got: %v", errs)
	}
	if got := errs[0].Error(); !strings.Contains(got, "disk-backed volumes") || !strings.Contains(got, `"fast-path"`) {
		t.Errorf("rejection should name the feature and the class, got: %s", got)
	}
}
