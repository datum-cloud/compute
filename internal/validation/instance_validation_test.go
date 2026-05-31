// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// cmpErrs compares two field.ErrorLists, ignoring BadValue and Detail fields,
// and treating nil and empty slices as equal.
func cmpErrs(t *testing.T, want, got field.ErrorList) {
	t.Helper()
	delta := cmp.Diff(
		want, got,
		cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail"),
		cmpopts.EquateEmpty(),
	)
	if delta != "" {
		t.Errorf("errors mismatch (-want +got):\n%s", delta)
	}
}

// TestValidateSecretVolumeSource tests the new secret volume validation.
func TestValidateSecretVolumeSource(t *testing.T) {
	// Use a named root path so child field names are predictable.
	root := field.NewPath("secret")

	cases := map[string]struct {
		source         *corev1.SecretVolumeSource
		expectedErrors field.ErrorList
	}{
		"valid minimal": {
			source: &corev1.SecretVolumeSource{SecretName: "my-secret"},
		},
		"missing secretName": {
			source: &corev1.SecretVolumeSource{},
			expectedErrors: field.ErrorList{
				field.Required(root.Child("secretName"), ""),
			},
		},
		"valid defaultMode 0644": {
			source: &corev1.SecretVolumeSource{SecretName: "s", DefaultMode: ptr.To(int32(0644))},
		},
		"defaultMode too large": {
			// 512 decimal == 0o1000 > 0o777, so invalid
			source: &corev1.SecretVolumeSource{SecretName: "s", DefaultMode: ptr.To(int32(512))},
			expectedErrors: field.ErrorList{
				field.Invalid(root.Child("defaultMode"), int32(512), fileModeErrorMsg),
			},
		},
		"defaultMode negative": {
			source: &corev1.SecretVolumeSource{SecretName: "s", DefaultMode: ptr.To(int32(-1))},
			expectedErrors: field.ErrorList{
				field.Invalid(root.Child("defaultMode"), int32(-1), fileModeErrorMsg),
			},
		},
		"valid items": {
			source: &corev1.SecretVolumeSource{
				SecretName: "s",
				Items: []corev1.KeyToPath{
					{Key: "password", Path: "config/pass"},
				},
			},
		},
		"items missing key": {
			source: &corev1.SecretVolumeSource{
				SecretName: "s",
				Items: []corev1.KeyToPath{
					{Path: "config/pass"},
				},
			},
			expectedErrors: field.ErrorList{
				field.Required(root.Child("items").Index(0).Child("key"), ""),
			},
		},
		"items missing path": {
			source: &corev1.SecretVolumeSource{
				SecretName: "s",
				Items: []corev1.KeyToPath{
					{Key: "password"},
				},
			},
			expectedErrors: field.ErrorList{
				field.Required(root.Child("items").Index(0).Child("path"), ""),
			},
		},
		"items invalid item mode": {
			source: &corev1.SecretVolumeSource{
				SecretName: "s",
				Items: []corev1.KeyToPath{
					// 513 > 511 (0777), invalid
					{Key: "k", Path: "p", Mode: ptr.To(int32(513))},
				},
			},
			expectedErrors: field.ErrorList{
				field.Invalid(root.Child("items").Index(0).Child("mode"), int32(513), fileModeErrorMsg),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := validateSecretVolumeSource(tc.source, root)
			cmpErrs(t, tc.expectedErrors, errs)
		})
	}
}

// TestValidateConfigMapItems tests that ConfigMap volume items are now validated
// (previously they were forbidden).
func TestValidateConfigMapItems(t *testing.T) {
	root := field.NewPath("configMap")

	cases := map[string]struct {
		source         *corev1.ConfigMapVolumeSource
		expectedErrors field.ErrorList
	}{
		"valid with items": {
			source: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"},
				Items: []corev1.KeyToPath{
					{Key: "app.conf", Path: "etc/app.conf"},
				},
			},
		},
		"items absolute path": {
			source: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"},
				Items: []corev1.KeyToPath{
					{Key: "k", Path: "/absolute/path"},
				},
			},
			expectedErrors: field.ErrorList{
				field.Invalid(root.Child("items").Index(0).Child("path"), "/absolute/path", "must be a relative path"),
			},
		},
		"items dotdot path escape": {
			source: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"},
				Items: []corev1.KeyToPath{
					{Key: "k", Path: "../escape"},
				},
			},
			expectedErrors: field.ErrorList{
				field.Invalid(root.Child("items").Index(0).Child("path"), "../escape", "must not contain '..' path elements"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := validateConfigMapVolumeSource(tc.source, root)
			cmpErrs(t, tc.expectedErrors, errs)
		})
	}
}

// TestValidateKeyToPath tests the key→path projection validator directly.
func TestValidateKeyToPath(t *testing.T) {
	root := field.NewPath("kp")

	cases := map[string]struct {
		kp             corev1.KeyToPath
		expectedErrors field.ErrorList
	}{
		"valid": {
			kp: corev1.KeyToPath{Key: "app.conf", Path: "config/app.conf"},
		},
		"valid with mode": {
			kp: corev1.KeyToPath{Key: "k", Path: "p", Mode: ptr.To(int32(0400))},
		},
		"missing key": {
			kp: corev1.KeyToPath{Path: "p"},
			expectedErrors: field.ErrorList{
				field.Required(root.Child("key"), ""),
			},
		},
		"missing path": {
			kp: corev1.KeyToPath{Key: "k"},
			expectedErrors: field.ErrorList{
				field.Required(root.Child("path"), ""),
			},
		},
		"absolute path": {
			kp: corev1.KeyToPath{Key: "k", Path: "/etc/hosts"},
			expectedErrors: field.ErrorList{
				field.Invalid(root.Child("path"), "/etc/hosts", "must be a relative path"),
			},
		},
		"dotdot escape": {
			kp: corev1.KeyToPath{Key: "k", Path: "../../etc/passwd"},
			expectedErrors: field.ErrorList{
				field.Invalid(root.Child("path"), "../../etc/passwd", "must not contain '..' path elements"),
			},
		},
		"invalid mode": {
			// 512 > 511 (0777)
			kp: corev1.KeyToPath{Key: "k", Path: "p", Mode: ptr.To(int32(512))},
			expectedErrors: field.ErrorList{
				field.Invalid(root.Child("mode"), int32(512), fileModeErrorMsg),
			},
		},
		"mode zero ok": {
			kp: corev1.KeyToPath{Key: "k", Path: "p", Mode: ptr.To(int32(0))},
		},
		"mode 0777 ok": {
			kp: corev1.KeyToPath{Key: "k", Path: "p", Mode: ptr.To(int32(0777))},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := validateKeyToPath(&tc.kp, root)
			cmpErrs(t, tc.expectedErrors, errs)
		})
	}
}

// TestValidateEnvFrom tests the EnvFrom field validation.
func TestValidateEnvFrom(t *testing.T) {
	root := field.NewPath("envFrom")

	cases := map[string]struct {
		envFrom        []computev1alpha.EnvFromSource
		expectedErrors field.ErrorList
	}{
		"empty list ok": {
			envFrom: nil,
		},
		"valid configMapRef": {
			envFrom: []computev1alpha.EnvFromSource{
				{ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: "my-cfg"}},
			},
		},
		"valid secretRef": {
			envFrom: []computev1alpha.EnvFromSource{
				{SecretRef: &computev1alpha.SecretEnvSource{Name: "my-secret"}},
			},
		},
		"valid with prefix": {
			envFrom: []computev1alpha.EnvFromSource{
				{Prefix: "APP_", ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: "cfg"}},
			},
		},
		"invalid prefix not C_IDENTIFIER": {
			envFrom: []computev1alpha.EnvFromSource{
				{Prefix: "123BAD", ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: "cfg"}},
			},
			expectedErrors: field.ErrorList{
				field.Invalid(root.Index(0).Child("prefix"), "123BAD", ""),
			},
		},
		"no source specified": {
			envFrom: []computev1alpha.EnvFromSource{
				{Prefix: "OK_"},
			},
			expectedErrors: field.ErrorList{
				field.Required(root.Index(0), ""),
			},
		},
		"both sources specified": {
			envFrom: []computev1alpha.EnvFromSource{
				{
					ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: "cfg"},
					SecretRef:    &computev1alpha.SecretEnvSource{Name: "sec"},
				},
			},
			expectedErrors: field.ErrorList{
				field.Forbidden(root.Index(0).Child("secretRef"), ""),
			},
		},
		"configMapRef missing name": {
			envFrom: []computev1alpha.EnvFromSource{
				{ConfigMapRef: &computev1alpha.ConfigMapEnvSource{}},
			},
			expectedErrors: field.ErrorList{
				field.Required(root.Index(0).Child("configMapRef").Child("name"), ""),
			},
		},
		"secretRef missing name": {
			envFrom: []computev1alpha.EnvFromSource{
				{SecretRef: &computev1alpha.SecretEnvSource{}},
			},
			expectedErrors: field.ErrorList{
				field.Required(root.Index(0).Child("secretRef").Child("name"), ""),
			},
		},
		"invalid dns label in configMapRef name": {
			envFrom: []computev1alpha.EnvFromSource{
				{ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: "INVALID_NAME"}},
			},
			expectedErrors: field.ErrorList{
				field.Invalid(root.Index(0).Child("configMapRef").Child("name"), "INVALID_NAME", ""),
			},
		},
		"optional secret": {
			envFrom: []computev1alpha.EnvFromSource{
				{SecretRef: &computev1alpha.SecretEnvSource{Name: "opt-secret", Optional: ptr.To(true)}},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := validateEnvFrom(tc.envFrom, root)
			cmpErrs(t, tc.expectedErrors, errs)
		})
	}
}

// sarGenerateName is used as a GenerateName prefix on synthetic SAR objects so
// the fake client accepts them. Extracted as a constant to satisfy goconst.
const sarGenerateName = "sar-"

// TestReferencedDataSAR tests that the admission SAR check fires for referenced
// ConfigMaps and Secrets, and produces the expected errors on deny.
func TestReferencedDataSAR(t *testing.T) {
	scheme := k8sruntime.NewScheme()
	utilruntime.Must(computev1alpha.AddToScheme(scheme))
	utilruntime.Must(networkingv1alpha.AddToScheme(scheme))
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	// allowAll is an interceptor that marks every SAR as allowed.
	allowAll := interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if sar, ok := obj.(*authorizationv1.SubjectAccessReview); ok {
				sar.GenerateName = sarGenerateName
				sar.Status.Allowed = true
			}
			return c.Create(ctx, obj, opts...)
		},
	}

	// denyAll is an interceptor that marks every SAR as denied.
	denyAll := interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if sar, ok := obj.(*authorizationv1.SubjectAccessReview); ok {
				sar.GenerateName = sarGenerateName
				sar.Status.Allowed = false
			}
			return c.Create(ctx, obj, opts...)
		},
	}

	baseClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	specPath := field.NewPath("spec").Child("template").Child("spec")

	workloadWithConfigMap := MakeSandboxWorkload("test", func(w *computev1alpha.Workload) {
		w.Spec.Template.Spec.Volumes = []computev1alpha.InstanceVolume{
			{
				Name: "cfg-vol",
				VolumeSource: computev1alpha.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"},
					},
				},
			},
		}
	})

	workloadWithSecret := MakeSandboxWorkload("test", func(w *computev1alpha.Workload) {
		w.Spec.Template.Spec.Runtime.Sandbox.Containers[0].EnvFrom = []computev1alpha.EnvFromSource{
			{SecretRef: &computev1alpha.SecretEnvSource{Name: "db-creds"}},
		}
	})

	cases := map[string]struct {
		workload       *computev1alpha.Workload
		interceptor    interceptor.Funcs
		expectedErrors field.ErrorList
	}{
		"configmap allowed": {
			workload:    workloadWithConfigMap,
			interceptor: allowAll,
		},
		"configmap denied": {
			workload:    workloadWithConfigMap,
			interceptor: denyAll,
			expectedErrors: field.ErrorList{
				field.Forbidden(specPath.Child("configmaps").Key("app-config"), ""),
			},
		},
		"secret denied": {
			workload:    workloadWithSecret,
			interceptor: denyAll,
			expectedErrors: field.ErrorList{
				field.Forbidden(specPath.Child("secrets").Key("db-creds"), ""),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cl := interceptor.NewClient(baseClient, tc.interceptor)
			opts := WorkloadValidationOptions{
				Client:           cl,
				Context:          context.Background(),
				Workload:         tc.workload,
				AdmissionRequest: admission.Request{},
				ValidCityCodes:   []string{"DFW"},
			}

			spec := tc.workload.Spec.Template.Spec
			errs := validateReferencedDataAccess(spec, specPath, opts)
			cmpErrs(t, tc.expectedErrors, errs)
		})
	}
}

// TestValidateVolumeProjectionPath tests the path safety validator.
func TestValidateVolumeProjectionPath(t *testing.T) {
	root := field.NewPath("path")

	cases := map[string]struct {
		p              string
		expectedErrors field.ErrorList
	}{
		"relative path ok":    {p: "subdir/file.conf"},
		"single component ok": {p: "file.conf"},
		"absolute path rejected": {
			p:              "/etc/hosts",
			expectedErrors: field.ErrorList{field.Invalid(root, "/etc/hosts", "")},
		},
		"dotdot at root rejected": {
			p:              "../escape",
			expectedErrors: field.ErrorList{field.Invalid(root, "../escape", "")},
		},
		"dotdot in middle rejected": {
			p:              "a/../../../etc/passwd",
			expectedErrors: field.ErrorList{field.Invalid(root, "a/../../../etc/passwd", "")},
		},
		// "a/b/../c" cleans to "a/c" which does not start with ".."
		"dotdot in subdir safe": {p: "a/b/../c"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := validateVolumeProjectionPath(tc.p, root)
			cmpErrs(t, tc.expectedErrors, errs)
		})
	}
}

// TestWorkloadWithReferencedDataE2E exercises full workload validation including
// the new SAR check, to confirm no regression in happy-path behaviour.
func TestWorkloadWithReferencedDataE2E(t *testing.T) {
	scheme := k8sruntime.NewScheme()
	utilruntime.Must(computev1alpha.AddToScheme(scheme))
	utilruntime.Must(networkingv1alpha.AddToScheme(scheme))
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if sar, ok := obj.(*authorizationv1.SubjectAccessReview); ok {
					sar.GenerateName = sarGenerateName
					sar.Status.Allowed = true
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		WithObjects(&networkingv1alpha.Network{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "default"},
		}).
		Build()

	workload := MakeSandboxWorkload("test", func(w *computev1alpha.Workload) {
		w.Spec.Template.Spec.Volumes = []computev1alpha.InstanceVolume{
			{
				Name: "cfg-vol",
				VolumeSource: computev1alpha.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"},
					},
				},
			},
		}
		// Wire volume attachment to satisfy validation.
		w.Spec.Template.Spec.Runtime.Sandbox.Containers[0].VolumeAttachments = []computev1alpha.VolumeAttachment{
			{Name: "cfg-vol"},
		}
	})

	opts := WorkloadValidationOptions{
		Client:         fakeClient,
		Context:        context.Background(),
		Workload:       workload,
		ValidCityCodes: []string{"DFW"},
	}

	errs := ValidateWorkloadCreate(workload, opts)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}
