// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"context"
	"strings"
	"testing"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	authorizationv1 "k8s.io/api/authorization/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/utils/ptr"

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

	// testCapChown and testCapNetBindService are capabilities every test class
	// grants.
	testCapChown          = "CHOWN"
	testCapNetBindService = "NET_BIND_SERVICE"

	// testCapPrefixedChown is testCapChown as tools that keep the CAP_ prefix
	// spell it.
	testCapPrefixedChown = "CAP_CHOWN"
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
					computev1alpha.RuntimeClassFeatureContainerCapabilities,
				},
				GrantableCapabilities: []computev1alpha.Capability{testCapChown, testCapNetBindService},
			},
		},
	}
	for _, tweak := range tweaks {
		tweak(&class)
	}
	return class
}

func withDefault(class *computev1alpha.RuntimeClass) { class.Spec.Default = true }

func withAvailable(status metav1.ConditionStatus, reason, message string) func(*computev1alpha.RuntimeClass) {
	return func(class *computev1alpha.RuntimeClass) {
		class.Status.Conditions = []metav1.Condition{{
			Type:    computev1alpha.RuntimeClassConditionAvailable,
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

// capabilitySpec is a sandbox whose single container adds and drops
// capabilities the way a Kubernetes nginx manifest does.
func capabilitySpec(class string, add, drop []computev1alpha.Capability) computev1alpha.InstanceSpec {
	return computev1alpha.InstanceSpec{
		Runtime: computev1alpha.InstanceRuntimeSpec{
			Class: class,
			Sandbox: &computev1alpha.SandboxRuntime{
				Containers: []computev1alpha.SandboxContainer{{
					Name:  "nginx",
					Image: "docker.io/library/nginx:1.27",
					SecurityContext: &computev1alpha.SandboxSecurityContext{
						Capabilities: &computev1alpha.SandboxCapabilities{Add: add, Drop: drop},
					},
				}},
			},
		},
	}
}

// TestValidateContainerCapabilitiesSelection verifies capability requests
// against the class the instance selects, with the gate both off and on.
func TestValidateContainerCapabilitiesSelection(t *testing.T) {
	root := field.NewPath("spec", "template", "spec")
	addPath := root.Child("runtime", "sandbox", "containers").Index(0).
		Child("securityContext", "capabilities", "add")

	cases := map[string]struct {
		gate           bool
		spec           computev1alpha.InstanceSpec
		catalog        runtimeclass.Catalog
		expectedErrors field.ErrorList
	}{
		"gate off: adding a capability is refused": {
			spec: capabilitySpec("", []computev1alpha.Capability{testCapChown}, []computev1alpha.Capability{computev1alpha.CapabilityAll}),
			expectedErrors: field.ErrorList{
				field.Forbidden(addPath, ""),
			},
		},
		"gate off: dropping capabilities is accepted": {
			spec: capabilitySpec("", nil, []computev1alpha.Capability{computev1alpha.CapabilityAll}),
		},
		"gate on: a class that grants the capability accepts it": {
			gate:    true,
			spec:    capabilitySpec(testClassBasalt, []computev1alpha.Capability{testCapChown}, nil),
			catalog: defaultCatalog(),
		},
		"gate on: a class without the feature refuses the request": {
			gate: true,
			spec: capabilitySpec(testClassBasalt, []computev1alpha.Capability{testCapChown}, nil),
			catalog: runtimeclass.Catalog{makeRuntimeClass(testClassBasalt,
				withFeatures(computev1alpha.RuntimeClassFeatureSandboxRuntime))},
			expectedErrors: field.ErrorList{field.Forbidden(addPath, "")},
		},
		"gate on: a capability outside the grantable set is refused": {
			gate:           true,
			spec:           capabilitySpec(testClassBasalt, []computev1alpha.Capability{"SYS_ADMIN"}, nil),
			catalog:        defaultCatalog(),
			expectedErrors: field.ErrorList{field.Forbidden(addPath.Index(0), "")},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, tc.gate)

			opts := WorkloadValidationOptions{RuntimeClasses: tc.catalog}
			cmpErrs(t, tc.expectedErrors, validateRuntimeClassSelection(tc.spec, root, opts))
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
					withAvailable(metav1.ConditionFalse, computev1alpha.RuntimeClassReasonUnsupportedFeature, "no")),
			},
			expectedErrors: field.ErrorList{field.Forbidden(classPath, "")},
		},
		"a class no controller has reported on yet is admitted": {
			class: testClassBasalt,
			catalog: runtimeclass.Catalog{
				makeRuntimeClass(testClassBasalt,
					withAvailable(metav1.ConditionUnknown, computev1alpha.RuntimeClassReasonPending, "waiting")),
			},
		},
		"a class its controller serves is admitted": {
			class: testClassBasalt,
			catalog: runtimeclass.Catalog{
				makeRuntimeClass(testClassBasalt,
					withAvailable(metav1.ConditionTrue, computev1alpha.RuntimeClassReasonServed, "")),
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

// confinementSpec is a sandbox whose single container states the security
// options that loosen or tighten its confinement.
func confinementSpec(class string, allowEscalation *bool, profile *computev1alpha.SandboxSeccompProfile) computev1alpha.InstanceSpec {
	return computev1alpha.InstanceSpec{
		Runtime: computev1alpha.InstanceRuntimeSpec{
			Class: class,
			Sandbox: &computev1alpha.SandboxRuntime{
				Containers: []computev1alpha.SandboxContainer{{
					Name:  "nginx",
					Image: "docker.io/library/nginx:1.27",
					SecurityContext: &computev1alpha.SandboxSecurityContext{
						AllowPrivilegeEscalation: allowEscalation,
						SeccompProfile:           profile,
					},
				}},
			},
		},
	}
}

// TestValidateConfinementSelection verifies the security options that loosen a
// container's confinement. A class states the confinement a tier offers, so with
// no catalog there is nothing that could state it and a loosening request is
// refused. Tightening needs no class and is always accepted.
func TestValidateConfinementSelection(t *testing.T) {
	root := field.NewPath("spec", "template", "spec")
	securityPath := root.Child("runtime", "sandbox", "containers").Index(0).Child("securityContext")

	allow, deny := true, false
	runtimeDefault := &computev1alpha.SandboxSeccompProfile{Type: computev1alpha.SeccompProfileTypeRuntimeDefault}
	unconfined := &computev1alpha.SandboxSeccompProfile{Type: computev1alpha.SeccompProfileTypeUnconfined}

	cases := map[string]struct {
		gate           bool
		spec           computev1alpha.InstanceSpec
		catalog        runtimeclass.Catalog
		expectedErrors field.ErrorList
	}{
		"gate off: allowing privilege escalation is refused": {
			spec: confinementSpec("", &allow, nil),
			expectedErrors: field.ErrorList{
				field.Forbidden(securityPath.Child("allowPrivilegeEscalation"), ""),
			},
		},
		"gate off: disabling seccomp is refused": {
			spec: confinementSpec("", nil, unconfined),
			expectedErrors: field.ErrorList{
				field.Forbidden(securityPath.Child("seccompProfile", "type"), ""),
			},
		},
		"gate off: tightening confinement is accepted": {
			spec: confinementSpec("", &deny, runtimeDefault),
		},
		"gate on: a class can serve either confinement": {
			gate:    true,
			spec:    confinementSpec(testClassBasalt, &allow, unconfined),
			catalog: defaultCatalog(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, tc.gate)

			opts := WorkloadValidationOptions{RuntimeClasses: tc.catalog}
			cmpErrs(t, tc.expectedErrors, validateRuntimeClassSelection(tc.spec, root, opts))
		})
	}
}

// stampedWorkload is a workload holding the security context admission writes
// when a class publishes a default: a capability grant, allowed privilege
// escalation, and an unconfined seccomp profile. Every value here was written by
// the platform, not typed by the customer.
func stampedWorkload() *computev1alpha.Workload {
	workload := &computev1alpha.Workload{}
	workload.Spec.Template.Spec = confinementSpec(testClassBasalt, nil, nil)
	workload.Spec.Template.Spec.Runtime.Sandbox.Containers[0].SecurityContext =
		&computev1alpha.SandboxSecurityContext{
			Capabilities: &computev1alpha.SandboxCapabilities{
				Add:  []computev1alpha.Capability{testCapChown},
				Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
			},
			AllowPrivilegeEscalation: ptr.To(true),
			SeccompProfile: &computev1alpha.SandboxSeccompProfile{
				Type: computev1alpha.SeccompProfileTypeUnconfined,
			},
		}
	return workload
}

// TestGateOffLeavesStampedWorkloadsUpdatable covers the rollback path. Turning
// the gate off is how an operator retreats from runtime classes, and workloads
// already stored hold a security context the platform wrote for them. Rejecting
// those values on update would make every such workload permanently unupdatable,
// including the controller's finalizer patch, and so undeletable.
func TestGateOffLeavesStampedWorkloadsUpdatable(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, false)
	root := field.NewPath("spec", "template", "spec")

	stored := stampedWorkload()

	t.Run("re-submitting the stored workload unchanged is accepted", func(t *testing.T) {
		unchanged := stored.DeepCopy()
		opts := WorkloadValidationOptions{OldWorkload: stored}
		if errs := validateRuntimeClassSelection(unchanged.Spec.Template.Spec, root, opts); len(errs) > 0 {
			t.Errorf("a stored workload must stay updatable after the gate goes off, got %v", errs)
		}
	})

	t.Run("an unrelated edit to the same workload is accepted", func(t *testing.T) {
		edited := stored.DeepCopy()
		edited.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image = "docker.io/library/nginx:1.28"
		opts := WorkloadValidationOptions{OldWorkload: stored}
		if errs := validateRuntimeClassSelection(edited.Spec.Template.Spec, root, opts); len(errs) > 0 {
			t.Errorf("an unrelated edit must not be blocked by a stamped value, got %v", errs)
		}
	})

	t.Run("newly widening confinement is still refused", func(t *testing.T) {
		widened := stored.DeepCopy()
		widened.Spec.Template.Spec.Runtime.Sandbox.Containers[0].
			SecurityContext.Capabilities.Add = []computev1alpha.Capability{testCapChown, testCapNetBindService}
		opts := WorkloadValidationOptions{OldWorkload: stored}
		if errs := validateRuntimeClassSelection(widened.Spec.Template.Spec, root, opts); len(errs) == 0 {
			t.Error("a customer adding a capability with the gate off must still be refused")
		}
	})

	t.Run("creating the same workload fresh is refused", func(t *testing.T) {
		created := stored.DeepCopy()
		opts := WorkloadValidationOptions{}
		if errs := validateRuntimeClassSelection(created.Spec.Template.Spec, root, opts); len(errs) == 0 {
			t.Error("a create carries no stored value to ratchet against and must be refused")
		}
	})
}

// TestValidateWorkloadUpdateAfterGateOff exercises the rollback path through
// the real entry point, mirroring workload_controller.go's finalizer-only
// Update: the same spec, platform-stamped security context included, written
// back verbatim. Rejecting it would leave the workload undeletable.
func TestValidateWorkloadUpdateAfterGateOff(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, false)

	scheme := k8sruntime.NewScheme()
	utilruntime.Must(computev1alpha.AddToScheme(scheme))
	utilruntime.Must(networkingv1alpha.AddToScheme(scheme))
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if sar, ok := obj.(*authorizationv1.SubjectAccessReview); ok {
					sar.GenerateName = "sar-"
					sar.Status.Allowed = true
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		WithObjects(&networkingv1alpha.Network{
			ObjectMeta: metav1.ObjectMeta{Namespace: testDefaultNamespace, Name: testDefaultNamespace},
		}).
		Build()

	// A workload stored while the gate was on: admission stamped both the class
	// and the security context, and the customer typed neither.
	stored := MakeSandboxWorkload("stamped", func(w *computev1alpha.Workload) {
		w.Spec.Template.Spec.Runtime.Class = testClassBasalt
		w.Spec.Template.Spec.Runtime.Sandbox.Containers[0].SecurityContext =
			&computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Add:  []computev1alpha.Capability{testCapChown},
					Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
				},
				AllowPrivilegeEscalation: ptr.To(true),
				SeccompProfile: &computev1alpha.SandboxSeccompProfile{
					Type: computev1alpha.SeccompProfileTypeUnconfined,
				},
			}
	})

	opts := WorkloadValidationOptions{
		Client:         fakeClient,
		Context:        context.Background(),
		ValidLocations: []string{testCityCodeDFW},
	}

	t.Run("writing the stored workload back verbatim is accepted", func(t *testing.T) {
		updated := stored.DeepCopy()
		o := opts
		o.Workload = updated
		if errs := ValidateWorkloadUpdate(updated, stored, o); len(errs) != 0 {
			t.Errorf("a stamped workload must stay updatable after the gate goes off, got: %v", errs)
		}
	})

	t.Run("creating the same workload fresh is refused", func(t *testing.T) {
		created := stored.DeepCopy()
		o := opts
		o.Workload = created
		if errs := ValidateWorkloadCreate(created, o); len(errs) == 0 {
			t.Error("a create carries no stored value to ratchet against and must be refused")
		}
	})
}
