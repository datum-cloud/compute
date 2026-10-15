// SPDX-License-Identifier: AGPL-3.0-only

package referenceddata

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	testContainerImage = "img"
	testEnvConfigMap   = "app-config"
	testSharedCfg      = "shared-cfg"
	testCfgRef         = "cfg"
	testPullSecret     = "registry-creds"
	testPrivateImage   = "registry.example.com/private/app:v1"
	testAppContainer   = "app"
	testZRegistry      = "z-registry"
)

func TestCollectFromTemplate(t *testing.T) {
	ns := "my-project"

	cases := map[string]struct {
		template computev1alpha.InstanceTemplateSpec
		want     ReferencedSet
	}{
		"empty template": {
			template: computev1alpha.InstanceTemplateSpec{},
			want:     nil,
		},
		"sandbox with no references": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{
						{Name: "c1", Image: testContainerImage, Env: []corev1.EnvVar{{Name: "FOO", Value: "bar"}}},
					},
				}
			}),
			want: nil,
		},
		"env.valueFrom.configMapKeyRef": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{
						{
							Name:  "c1",
							Image: testContainerImage,
							Env: []corev1.EnvVar{
								{
									Name: "KEY",
									ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: testEnvConfigMap},
											Key:                  "key",
										},
									},
								},
							},
						},
					},
				}
			}),
			want: ReferencedSet{
				{Kind: testKindConfigMap, Name: testEnvConfigMap, Namespace: ns},
			},
		},
		"env.valueFrom.secretKeyRef": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{
						{
							Name:  "c1",
							Image: testContainerImage,
							Env: []corev1.EnvVar{
								{
									Name: "PASS",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: testNameDBCreds},
											Key:                  "password",
										},
									},
								},
							},
						},
					},
				}
			}),
			want: ReferencedSet{
				{Kind: testKindSecret, Name: testNameDBCreds, Namespace: ns},
			},
		},
		"envFrom.configMapRef": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{
						{
							Name:    "c1",
							Image:   testContainerImage,
							EnvFrom: []computev1alpha.EnvFromSource{{ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: "env-config"}}},
						},
					},
				}
			}),
			want: ReferencedSet{
				{Kind: testKindConfigMap, Name: "env-config", Namespace: ns},
			},
		},
		"envFrom.secretRef": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{
						{
							Name:    "c1",
							Image:   testContainerImage,
							EnvFrom: []computev1alpha.EnvFromSource{{SecretRef: &computev1alpha.SecretEnvSource{Name: "env-secret"}}},
						},
					},
				}
			}),
			want: ReferencedSet{
				{Kind: testKindSecret, Name: "env-secret", Namespace: ns},
			},
		},
		"volume configMap": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name: "cfg-vol",
						VolumeSource: computev1alpha.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "vol-config"},
							},
						},
					},
				}
			}),
			want: ReferencedSet{
				{Kind: testKindConfigMap, Name: "vol-config", Namespace: ns},
			},
		},
		"volume secret": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name: "sec-vol",
						VolumeSource: computev1alpha.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: "vol-secret"},
						},
					},
				}
			}),
			want: ReferencedSet{
				{Kind: testKindSecret, Name: "vol-secret", Namespace: ns},
			},
		},
		"deduplication across containers": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{
						{
							Name:    "c1",
							Image:   testContainerImage,
							EnvFrom: []computev1alpha.EnvFromSource{{ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: testSharedCfg}}},
						},
						{
							Name:    "c2",
							Image:   testContainerImage,
							EnvFrom: []computev1alpha.EnvFromSource{{ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: testSharedCfg}}},
						},
					},
				}
			}),
			want: ReferencedSet{
				{Kind: testKindConfigMap, Name: testSharedCfg, Namespace: ns},
			},
		},
		// When both configMapRef and secretRef are set on the same envFrom
		// entry, validateEnvFrom rejects it, so the collector must skip it
		// rather than collecting (and later SAR-ing) both refs.
		"both refs set on envFrom entry — skipped": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{
						{
							Name:  "c1",
							Image: testContainerImage,
							EnvFrom: []computev1alpha.EnvFromSource{
								{
									ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: testCfgRef},
									SecretRef:    &computev1alpha.SecretEnvSource{Name: "sec"},
								},
							},
						},
					},
				}
			}),
			// No refs collected — the invalid entry is skipped entirely.
			want: nil,
		},
		// Shape mirrors test/e2e/referenced-data-mounts/workload-deployment.yaml
		// with a pull credential added: a real template references a ConfigMap by
		// volume, a Secret by env, and a registry credential by imagePullSecrets.
		"imagePullSecrets alongside env and volume sources": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					ImagePullSecrets: []computev1alpha.LocalSecretReference{{Name: testPullSecret}},
					Containers: []computev1alpha.SandboxContainer{
						{
							Name:  testAppContainer,
							Image: testPrivateImage,
							Env: []corev1.EnvVar{
								{
									Name: "DB_PASSWORD",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: "app-secret"},
											Key:                  "db.password",
										},
									},
								},
							},
							VolumeAttachments: []computev1alpha.VolumeAttachment{
								{Name: "config-vol", MountPath: ptr.To("/etc/config")},
							},
						},
					},
				}
				t.Spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name: "config-vol",
						VolumeSource: computev1alpha.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: testEnvConfigMap},
							},
						},
					},
				}
			}),
			want: ReferencedSet{
				{Kind: testKindConfigMap, Name: testEnvConfigMap, Namespace: ns},
				{Kind: testKindSecret, Name: "app-secret", Namespace: ns},
				{Kind: testKindSecret, Name: "registry-creds", Namespace: ns},
			},
		},
		"multiple imagePullSecrets deduplicated and sorted": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					ImagePullSecrets: []computev1alpha.LocalSecretReference{
						{Name: testZRegistry},
						{Name: "a-registry"},
						{Name: testZRegistry},
					},
					Containers: []computev1alpha.SandboxContainer{
						{Name: testAppContainer, Image: testPrivateImage},
					},
				}
			}),
			want: ReferencedSet{
				{Kind: testKindSecret, Name: "a-registry", Namespace: ns},
				{Kind: testKindSecret, Name: testZRegistry, Namespace: ns},
			},
		},
		// The same Secret used both as a pull credential and as an env source
		// must federate exactly once.
		"imagePullSecret shared with env secret collapses to one ref": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					ImagePullSecrets: []computev1alpha.LocalSecretReference{{Name: testNameDBCreds}},
					Containers: []computev1alpha.SandboxContainer{
						{
							Name:    "app",
							Image:   testPrivateImage,
							EnvFrom: []computev1alpha.EnvFromSource{{SecretRef: &computev1alpha.SecretEnvSource{Name: testNameDBCreds}}},
						},
					},
				}
			}),
			want: ReferencedSet{
				{Kind: testKindSecret, Name: testNameDBCreds, Namespace: ns},
			},
		},
		"imagePullSecret with empty name is skipped": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					ImagePullSecrets: []computev1alpha.LocalSecretReference{{Name: ""}},
					Containers: []computev1alpha.SandboxContainer{
						{Name: testAppContainer, Image: testContainerImage},
					},
				}
			}),
			want: nil,
		},
		"mixed sources sorted configmap-first then secret": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{
						{
							Name:  "c1",
							Image: testContainerImage,
							EnvFrom: []computev1alpha.EnvFromSource{
								{SecretRef: &computev1alpha.SecretEnvSource{Name: "z-secret"}},
								{ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: "a-config"}},
							},
						},
					},
				}
			}),
			// Sorted: ConfigMap < Secret lexicographically, then name ascending
			want: ReferencedSet{
				{Kind: testKindConfigMap, Name: "a-config", Namespace: ns},
				{Kind: testKindSecret, Name: "z-secret", Namespace: ns},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := CollectFromTemplate(ns, tc.template)
			if len(got) != len(tc.want) {
				t.Fatalf("CollectFromTemplate: len=%d want=%d; got=%v want=%v", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("index %d: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestTemplateReferencesData(t *testing.T) {
	cases := map[string]struct {
		template computev1alpha.InstanceTemplateSpec
		want     bool
	}{
		"empty": {
			template: computev1alpha.InstanceTemplateSpec{},
			want:     false,
		},
		"plain env only": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{
						{Name: "c", Image: testContainerImage, Env: []corev1.EnvVar{{Name: "X", Value: "y"}}},
					},
				}
			}),
			want: false,
		},
		"has configmap ref": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Volumes = []computev1alpha.InstanceVolume{
					{Name: "v", VolumeSource: computev1alpha.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: testCfgRef},
						},
					}},
				}
			}),
			want: true,
		},
		// A template whose only referenced data is a pull credential must still
		// stamp the ReferencedData scheduling gate — otherwise the Instance is
		// admitted to the cell before the credential lands there.
		"has only an image pull secret": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					ImagePullSecrets: []computev1alpha.LocalSecretReference{{Name: testPullSecret}},
					Containers: []computev1alpha.SandboxContainer{
						{Name: testAppContainer, Image: testPrivateImage},
					},
				}
			}),
			want: true,
		},
		"has secret ref": {
			template: makeTemplate(func(t *computev1alpha.InstanceTemplateSpec) {
				t.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{
						{
							Name:  "c",
							Image: testContainerImage,
							EnvFrom: []computev1alpha.EnvFromSource{
								{SecretRef: &computev1alpha.SecretEnvSource{Name: "s", Optional: ptr.To(true)}},
							},
						},
					},
				}
			}),
			want: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := TemplateReferencesData(tc.template)
			if got != tc.want {
				t.Errorf("TemplateReferencesData = %v, want %v", got, tc.want)
			}
		})
	}
}

// makeTemplate is a helper that creates a minimal InstanceTemplateSpec and
// applies the given mutations.
func makeTemplate(fn func(*computev1alpha.InstanceTemplateSpec)) computev1alpha.InstanceTemplateSpec {
	t := computev1alpha.InstanceTemplateSpec{}
	fn(&t)
	return t
}
