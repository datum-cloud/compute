// SPDX-License-Identifier: AGPL-3.0-only

package runtimeclass

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Fixture class names shared across these tests. They are constants so every
// case names the same fixture the same way.
const (
	testContainerName    = "app"
	testConfigVolumeName = "config"
	testDiskVolumeName   = "data"
	testConfigMapName    = "settings"
	testCapChown         = "CHOWN"

	testCapNetBindService = "NET_BIND_SERVICE"
)

// envFromRejection is the rejection a narrow class returns for envFrom. It is
// spelled out rather than built from the validator, so the test fails if the
// wording changes.
const envFromRejection = "environment variables sourced from a whole ConfigMap or Secret " +
	`are not supported by the "azurite" runtime class`

// fullCapabilities declares every feature, so a case that expects no rejection
// cannot pass because of an omitted declaration.
var fullCapabilities = Capabilities{
	Class: testClassBasalt,
	Features: []Feature{
		FeatureSandboxRuntime,
		FeatureVirtualMachineRuntime,
		FeatureConfigMapVolumes,
		FeatureSecretVolumes,
		FeatureDiskVolumes,
		FeatureDeviceVolumeAttachments,
		FeatureEnvFrom,
		FeatureImagePullSecrets,
		FeatureContainerCapabilities,
	},
	GrantableCapabilities: []Capability{testCapNetBindService, testCapChown},
}

// minimalCapabilities is the narrow set a fast-start class serves. It covers
// containers with injected data and excludes anything requiring a disk or a
// full guest.
var minimalCapabilities = Capabilities{
	Class: testClassAzurite,
	Features: []Feature{
		FeatureSandboxRuntime,
		FeatureConfigMapVolumes,
		FeatureSecretVolumes,
	},
}

func sandboxSpec(containers ...computev1alpha.SandboxContainer) computev1alpha.InstanceSpec {
	return computev1alpha.InstanceSpec{
		Runtime: computev1alpha.InstanceRuntimeSpec{
			Sandbox: &computev1alpha.SandboxRuntime{Containers: containers},
		},
	}
}

func TestValidateInstanceSpec(t *testing.T) {
	tests := []struct {
		name         string
		spec         computev1alpha.InstanceSpec
		capabilities Capabilities
		want         field.ErrorList
	}{
		{
			name:         "a sandbox using only supported features is accepted",
			capabilities: minimalCapabilities,
			spec: func() computev1alpha.InstanceSpec {
				spec := sandboxSpec(computev1alpha.SandboxContainer{
					Name:  testContainerName,
					Image: "index.unikraft.io/datum/app:latest",
					VolumeAttachments: []computev1alpha.VolumeAttachment{
						{Name: testConfigVolumeName, MountPath: ptr.To("/etc/app")},
					},
				})
				spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name: testConfigVolumeName,
						VolumeSource: computev1alpha.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{},
						},
					},
				}
				return spec
			}(),
		},
		{
			name:         "a disk-backed volume is rejected by a class without disks",
			capabilities: minimalCapabilities,
			spec: func() computev1alpha.InstanceSpec {
				spec := sandboxSpec(computev1alpha.SandboxContainer{Name: testContainerName})
				spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name: testDiskVolumeName,
						VolumeSource: computev1alpha.VolumeSource{
							Disk: &computev1alpha.DiskTemplateVolumeSource{},
						},
					},
				}
				return spec
			}(),
			want: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "volumes").Index(0).Child("disk"),
					`disk-backed volumes are not supported by the "azurite" runtime class`),
			},
		},
		{
			name:         "a disk-backed volume is accepted by a class that serves disks",
			capabilities: fullCapabilities,
			spec: func() computev1alpha.InstanceSpec {
				spec := sandboxSpec(computev1alpha.SandboxContainer{Name: testContainerName})
				spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name: testDiskVolumeName,
						VolumeSource: computev1alpha.VolumeSource{
							Disk: &computev1alpha.DiskTemplateVolumeSource{},
						},
					},
				}
				return spec
			}(),
		},
		{
			name: "a ConfigMap volume is rejected by a class that cannot present one",
			capabilities: Capabilities{
				Class:    testClassAzurite,
				Features: []Feature{FeatureSandboxRuntime},
			},
			spec: func() computev1alpha.InstanceSpec {
				spec := sandboxSpec(computev1alpha.SandboxContainer{Name: testContainerName})
				spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name: testConfigVolumeName,
						VolumeSource: computev1alpha.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{},
						},
					},
				}
				return spec
			}(),
			want: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "volumes").Index(0).Child("configMap"),
					`ConfigMap-backed volumes are not supported by the "azurite" runtime class`),
			},
		},
		{
			name: "a Secret volume is rejected by a class that cannot present one",
			capabilities: Capabilities{
				Class:    testClassAzurite,
				Features: []Feature{FeatureSandboxRuntime},
			},
			spec: func() computev1alpha.InstanceSpec {
				spec := sandboxSpec(computev1alpha.SandboxContainer{Name: testContainerName})
				spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name: "credentials",
						VolumeSource: computev1alpha.VolumeSource{
							Secret: &corev1.SecretVolumeSource{},
						},
					},
				}
				return spec
			}(),
			want: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "volumes").Index(0).Child("secret"),
					`Secret-backed volumes are not supported by the "azurite" runtime class`),
			},
		},
		{
			name:         "envFrom is rejected by a class without it",
			capabilities: minimalCapabilities,
			spec: sandboxSpec(computev1alpha.SandboxContainer{
				Name: testContainerName,
				EnvFrom: []computev1alpha.EnvFromSource{
					{ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: testConfigMapName}},
				},
			}),
			want: field.ErrorList{
				field.Forbidden(
					field.NewPath("spec", "runtime", "sandbox", "containers").Index(0).Child("envFrom"),
					envFromRejection),
			},
		},
		{
			name:         "image pull secrets are rejected by a class that cannot authenticate to a registry",
			capabilities: minimalCapabilities,
			spec: func() computev1alpha.InstanceSpec {
				spec := sandboxSpec(computev1alpha.SandboxContainer{Name: testContainerName})
				spec.Runtime.Sandbox.ImagePullSecrets = []computev1alpha.LocalSecretReference{{Name: "registry"}}
				return spec
			}(),
			want: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "runtime", "sandbox", "imagePullSecrets"),
					`image pull secrets are not supported by the "azurite" runtime class`),
			},
		},
		{
			name:         "an attachment with no mount path is rejected as a raw device",
			capabilities: minimalCapabilities,
			spec: sandboxSpec(computev1alpha.SandboxContainer{
				Name:              testContainerName,
				VolumeAttachments: []computev1alpha.VolumeAttachment{{Name: testDiskVolumeName}},
			}),
			want: field.ErrorList{
				field.Forbidden(
					field.NewPath("spec", "runtime", "sandbox", "containers").Index(0).Child("volumeAttachments").Index(0),
					`volumes attached as raw devices are not supported by the "azurite" runtime class`),
			},
		},
		{
			name: "a sandbox is rejected by a class that only runs virtual machines",
			capabilities: Capabilities{
				Class:    testClassBasalt,
				Features: []Feature{FeatureVirtualMachineRuntime},
			},
			spec: sandboxSpec(computev1alpha.SandboxContainer{Name: testContainerName}),
			want: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "runtime", "sandbox"),
					`container sandbox instances are not supported by the "basalt" runtime class`),
			},
		},
		{
			name:         "a virtual machine is rejected by a sandbox-only class",
			capabilities: minimalCapabilities,
			spec: computev1alpha.InstanceSpec{
				Runtime: computev1alpha.InstanceRuntimeSpec{
					VirtualMachine: &computev1alpha.VirtualMachineRuntime{
						VolumeAttachments: []computev1alpha.VolumeAttachment{
							{Name: "boot", MountPath: ptr.To("/")},
						},
					},
				},
			},
			want: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "runtime", "virtualMachine"),
					`virtual machine instances are not supported by the "azurite" runtime class`),
			},
		},
		{
			name:         "every unsupported feature is reported, not just the first",
			capabilities: minimalCapabilities,
			spec: func() computev1alpha.InstanceSpec {
				spec := sandboxSpec(computev1alpha.SandboxContainer{
					Name: testContainerName,
					EnvFrom: []computev1alpha.EnvFromSource{
						{SecretRef: &computev1alpha.SecretEnvSource{Name: testConfigMapName}},
					},
					VolumeAttachments: []computev1alpha.VolumeAttachment{{Name: testDiskVolumeName}},
				})
				spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name:         testDiskVolumeName,
						VolumeSource: computev1alpha.VolumeSource{Disk: &computev1alpha.DiskTemplateVolumeSource{}},
					},
				}
				return spec
			}(),
			want: field.ErrorList{
				field.Forbidden(
					field.NewPath("spec", "runtime", "sandbox", "containers").Index(0).Child("envFrom"),
					envFromRejection),
				field.Forbidden(
					field.NewPath("spec", "runtime", "sandbox", "containers").Index(0).Child("volumeAttachments").Index(0),
					`volumes attached as raw devices are not supported by the "azurite" runtime class`),
				field.Forbidden(field.NewPath("spec", "volumes").Index(0).Child("disk"),
					`disk-backed volumes are not supported by the "azurite" runtime class`),
			},
		},
		{
			name:         "a class that was never resolved is described rather than named",
			capabilities: Capabilities{Features: []Feature{FeatureSandboxRuntime}},
			spec: sandboxSpec(computev1alpha.SandboxContainer{
				Name: testContainerName,
				EnvFrom: []computev1alpha.EnvFromSource{
					{ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: testConfigMapName}},
				},
			}),
			want: field.ErrorList{
				field.Forbidden(
					field.NewPath("spec", "runtime", "sandbox", "containers").Index(0).Child("envFrom"),
					"environment variables sourced from a whole ConfigMap or Secret "+
						"are not supported by the runtime class this instance runs in"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ValidateInstanceSpec(test.spec, test.capabilities, field.NewPath("spec"))
			if delta := cmp.Diff(test.want, got, cmpopts.EquateEmpty()); delta != "" {
				t.Errorf("unexpected rejections (-want +got):\n%s", delta)
			}
		})
	}
}

// capabilityContainer is a container shaped like the nginx sandbox a customer
// ports from a Kubernetes manifest.
func capabilityContainer(add, drop []Capability) computev1alpha.SandboxContainer {
	return computev1alpha.SandboxContainer{
		Name:  testContainerName,
		Image: "docker.io/library/nginx:1.27",
		SecurityContext: &computev1alpha.SandboxSecurityContext{
			Capabilities: &computev1alpha.SandboxCapabilities{Add: add, Drop: drop},
		},
	}
}

// TestValidateContainerCapabilities covers capability requests against the
// class's published grantable set.
func TestValidateContainerCapabilities(t *testing.T) {
	addPath := field.NewPath("spec", "runtime", "sandbox", "containers").Index(0).
		Child("securityContext", "capabilities", "add")

	tests := []struct {
		name         string
		spec         computev1alpha.InstanceSpec
		capabilities Capabilities
		want         field.ErrorList
	}{
		{
			name:         "a container with no security context needs no feature",
			capabilities: minimalCapabilities,
			spec:         sandboxSpec(computev1alpha.SandboxContainer{Name: testContainerName}),
		},
		{
			name:         "adding a capability is rejected by a class without the feature",
			capabilities: minimalCapabilities,
			spec: sandboxSpec(capabilityContainer(
				[]Capability{testCapChown}, []Capability{computev1alpha.CapabilityAll})),
			want: field.ErrorList{
				field.Forbidden(addPath,
					`container capability requests are not supported by the "azurite" runtime class`),
			},
		},
		{
			name: "listing grantable capabilities without declaring the feature grants nothing",
			capabilities: Capabilities{
				Class:                 testClassAzurite,
				Features:              []Feature{FeatureSandboxRuntime},
				GrantableCapabilities: []Capability{testCapChown},
			},
			spec: sandboxSpec(capabilityContainer([]Capability{testCapChown}, nil)),
			want: field.ErrorList{
				field.Forbidden(addPath,
					`container capability requests are not supported by the "azurite" runtime class`),
			},
		},
		{
			name:         "a drop-only request is accepted by a class without the feature",
			capabilities: minimalCapabilities,
			spec:         sandboxSpec(capabilityContainer(nil, []Capability{computev1alpha.CapabilityAll})),
		},
		{
			name:         "capabilities the class grants are accepted",
			capabilities: fullCapabilities,
			spec: sandboxSpec(capabilityContainer(
				[]Capability{testCapChown, testCapNetBindService}, []Capability{computev1alpha.CapabilityAll})),
		},
		{
			name:         "each capability the class does not grant is rejected on its own entry",
			capabilities: fullCapabilities,
			spec: sandboxSpec(capabilityContainer(
				[]Capability{"SYS_ADMIN", testCapChown, "NET_RAW"}, []Capability{computev1alpha.CapabilityAll})),
			want: field.ErrorList{
				field.Forbidden(addPath.Index(0),
					`capability SYS_ADMIN is not granted by the "basalt" runtime class, which grants CHOWN, NET_BIND_SERVICE`),
				field.Forbidden(addPath.Index(2),
					`capability NET_RAW is not granted by the "basalt" runtime class, which grants CHOWN, NET_BIND_SERVICE`),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ValidateInstanceSpec(test.spec, test.capabilities, field.NewPath("spec"))
			if delta := cmp.Diff(test.want, got, cmpopts.EquateEmpty()); delta != "" {
				t.Errorf("unexpected rejections (-want +got):\n%s", delta)
			}
		})
	}
}

// TestCapabilitiesFromReadsGrantableCapabilities confirms a class's published
// grantable set reaches the check applied to its instances.
func TestCapabilitiesFromReadsGrantableCapabilities(t *testing.T) {
	class := &computev1alpha.RuntimeClass{
		Spec: computev1alpha.RuntimeClassSpec{
			Capabilities: computev1alpha.RuntimeClassCapabilities{
				Features:              []Feature{FeatureSandboxRuntime, FeatureContainerCapabilities},
				GrantableCapabilities: []Capability{testCapNetBindService},
			},
		},
	}
	class.Name = testClassBasalt

	capabilities := CapabilitiesFrom(class)
	if !capabilities.Grants(testCapNetBindService) {
		t.Error("Grants(NET_BIND_SERVICE) = false, want true for a capability the class publishes")
	}
	if capabilities.Grants("SYS_ADMIN") {
		t.Error("Grants(SYS_ADMIN) = true, want false for a capability the class does not publish")
	}
}

// TestValidateInstanceTemplateSpec confirms validation rejects a workload's
// template at apply time and reports paths into the template the customer
// wrote.
func TestValidateInstanceTemplateSpec(t *testing.T) {
	template := computev1alpha.InstanceTemplateSpec{
		Spec: func() computev1alpha.InstanceSpec {
			spec := sandboxSpec(computev1alpha.SandboxContainer{Name: testContainerName})
			spec.Volumes = []computev1alpha.InstanceVolume{
				{
					Name:         testDiskVolumeName,
					VolumeSource: computev1alpha.VolumeSource{Disk: &computev1alpha.DiskTemplateVolumeSource{}},
				},
			}
			return spec
		}(),
	}

	want := field.ErrorList{
		field.Forbidden(field.NewPath("spec", "template", "spec", "volumes").Index(0).Child("disk"),
			`disk-backed volumes are not supported by the "azurite" runtime class`),
	}

	got := ValidateInstanceTemplateSpec(template, minimalCapabilities, field.NewPath("spec", "template"))
	if delta := cmp.Diff(want, got, cmpopts.EquateEmpty()); delta != "" {
		t.Errorf("unexpected rejections (-want +got):\n%s", delta)
	}
}
