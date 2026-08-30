// SPDX-License-Identifier: AGPL-3.0-only

package instancepod

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/pkg/instancetype"
	"go.datum.net/compute/pkg/runtimeclass"
)

// Fixture names shared across these tests. They are constants only so the
// same fixture is named the same way in every case.
const (
	testContainerName    = "app"
	testConfigVolumeName = "config"
	testDiskVolumeName   = "data"
	testConfigMapName    = "settings"
	testSecretName       = "api-key"
	testWorkloadName     = "web"
	testManagedByKey     = "managed-by"
	testManagedByValue   = "infra-provider-unikraft"
	testMemory2Gi        = "2Gi"
	testLabelValueTrue   = "true"

	// The class names below are invented, and deliberately not the ones the
	// platform ships: translating an instance must not depend on what its class
	// is called.
	testClassAzurite = "azurite"
	testClassBasalt  = "basalt"
)

// sandboxCapabilities serves the whole sandbox surface, so a translation test
// exercises translation rather than capability rejection.
var sandboxCapabilities = runtimeclass.Capabilities{
	Class: testClassAzurite,
	Features: []runtimeclass.Feature{
		runtimeclass.FeatureSandboxRuntime,
		runtimeclass.FeatureConfigMapVolumes,
		runtimeclass.FeatureSecretVolumes,
		runtimeclass.FeatureDeviceVolumeAttachments,
		runtimeclass.FeatureEnvFrom,
		runtimeclass.FeatureImagePullSecrets,
	},
}

func newInstance(containers ...computev1alpha.SandboxContainer) *computev1alpha.Instance {
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "app-0", Namespace: "project"},
		Spec: computev1alpha.InstanceSpec{
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: instancetype.D1Standard2},
				Sandbox:   &computev1alpha.SandboxRuntime{Containers: containers},
			},
		},
	}
}

func TestBuildPodSpecTranslation(t *testing.T) {
	tests := []struct {
		name     string
		instance *computev1alpha.Instance
		opts     Options
		want     func(spec corev1.PodSpec) error
	}{
		{
			name: "ConfigMap and Secret volumes pass through",
			instance: func() *computev1alpha.Instance {
				instance := newInstance(computev1alpha.SandboxContainer{Name: testContainerName})
				instance.Spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name: testConfigVolumeName,
						VolumeSource: computev1alpha.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: testConfigMapName},
							},
						},
					},
					{
						Name: "credentials",
						VolumeSource: computev1alpha.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: testSecretName},
						},
					},
				}
				return instance
			}(),
			opts: Options{Capabilities: sandboxCapabilities},
			want: func(spec corev1.PodSpec) error {
				want := []corev1.Volume{
					{
						Name: testConfigVolumeName,
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: testConfigMapName},
							},
						},
					},
					{
						Name: "credentials",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: testSecretName},
						},
					},
				}
				return diff(want, spec.Volumes)
			},
		},
		{
			name: "environment carries ValueFrom through",
			instance: newInstance(computev1alpha.SandboxContainer{
				Name: testContainerName,
				Env: []corev1.EnvVar{
					{Name: "LITERAL", Value: "value"},
					{
						Name: "FROM_SECRET",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: testSecretName},
								Key:                  "token",
							},
						},
					},
				},
			}),
			opts: Options{Capabilities: sandboxCapabilities},
			want: func(spec corev1.PodSpec) error {
				want := []corev1.EnvVar{
					{Name: "LITERAL", Value: "value"},
					{
						Name: "FROM_SECRET",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: testSecretName},
								Key:                  "token",
							},
						},
					},
				}
				return diff(want, spec.Containers[0].Env)
			},
		},
		{
			name: "envFrom translates field by field",
			instance: newInstance(computev1alpha.SandboxContainer{
				Name: testContainerName,
				EnvFrom: []computev1alpha.EnvFromSource{
					{
						Prefix:       "APP_",
						ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: testConfigMapName, Optional: ptr.To(true)},
					},
					{
						SecretRef: &computev1alpha.SecretEnvSource{Name: testSecretName},
					},
				},
			}),
			opts: Options{Capabilities: sandboxCapabilities},
			want: func(spec corev1.PodSpec) error {
				want := []corev1.EnvFromSource{
					{
						Prefix: "APP_",
						ConfigMapRef: &corev1.ConfigMapEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: testConfigMapName},
							Optional:             ptr.To(true),
						},
					},
					{
						SecretRef: &corev1.SecretEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: testSecretName},
						},
					},
				}
				return diff(want, spec.Containers[0].EnvFrom)
			},
		},
		{
			name: "ports carry name, number and protocol",
			instance: newInstance(computev1alpha.SandboxContainer{
				Name: testContainerName,
				Ports: []computev1alpha.NamedPort{
					{Name: "http", Port: 8080},
					{Name: "dns", Port: 53, Protocol: ptr.To(corev1.ProtocolUDP)},
				},
			}),
			opts: Options{Capabilities: sandboxCapabilities},
			want: func(spec corev1.PodSpec) error {
				want := []corev1.ContainerPort{
					{Name: "http", ContainerPort: 8080},
					{Name: "dns", ContainerPort: 53, Protocol: corev1.ProtocolUDP},
				}
				return diff(want, spec.Containers[0].Ports)
			},
		},
		{
			name: "attachments with a mount path become mounts, raw devices do not",
			instance: newInstance(computev1alpha.SandboxContainer{
				Name: testContainerName,
				VolumeAttachments: []computev1alpha.VolumeAttachment{
					{Name: testConfigVolumeName, MountPath: ptr.To("/etc/app")},
					{Name: "raw"},
				},
			}),
			opts: Options{Capabilities: sandboxCapabilities},
			want: func(spec corev1.PodSpec) error {
				want := []corev1.VolumeMount{{Name: testConfigVolumeName, MountPath: "/etc/app"}}
				return diff(want, spec.Containers[0].VolumeMounts)
			},
		},
		{
			name: "command and args pass through untouched",
			instance: newInstance(computev1alpha.SandboxContainer{
				Name:    testContainerName,
				Image:   "index.unikraft.io/datum/app:latest",
				Command: []string{"/app"},
				Args:    []string{"--serve"},
			}),
			opts: Options{Capabilities: sandboxCapabilities},
			want: func(spec corev1.PodSpec) error {
				container := spec.Containers[0]
				if err := diff([]string{"/app"}, container.Command); err != nil {
					return err
				}
				if err := diff([]string{"--serve"}, container.Args); err != nil {
					return err
				}
				return diff("index.unikraft.io/datum/app:latest", container.Image)
			},
		},
		{
			name: "image pull secrets reach the pod",
			instance: func() *computev1alpha.Instance {
				instance := newInstance(computev1alpha.SandboxContainer{Name: testContainerName})
				instance.Spec.Runtime.Sandbox.ImagePullSecrets = []computev1alpha.LocalSecretReference{{Name: "registry"}}
				return instance
			}(),
			opts: Options{Capabilities: sandboxCapabilities},
			want: func(spec corev1.PodSpec) error {
				return diff([]corev1.LocalObjectReference{{Name: "registry"}}, spec.ImagePullSecrets)
			},
		},
		{
			name:     "node targeting comes from the caller",
			instance: newInstance(computev1alpha.SandboxContainer{Name: testContainerName}),
			opts: Options{
				Capabilities: sandboxCapabilities,
				NodeSelector: map[string]string{"unikraft.com/virtual-kubelet": testLabelValueTrue},
				Tolerations: []corev1.Toleration{{
					Key:      "virtual-kubelet.io/provider",
					Operator: corev1.TolerationOpEqual,
					Value:    "ukc",
					Effect:   corev1.TaintEffectNoSchedule,
				}},
			},
			want: func(spec corev1.PodSpec) error {
				wantSelector := map[string]string{"unikraft.com/virtual-kubelet": testLabelValueTrue}
				if err := diff(wantSelector, spec.NodeSelector); err != nil {
					return err
				}
				return diff([]corev1.Toleration{{
					Key:      "virtual-kubelet.io/provider",
					Operator: corev1.TolerationOpEqual,
					Value:    "ukc",
					Effect:   corev1.TaintEffectNoSchedule,
				}}, spec.Tolerations)
			},
		},
		{
			name:     "an instance is denied Kubernetes API access and restarts on exit",
			instance: newInstance(computev1alpha.SandboxContainer{Name: testContainerName}),
			opts:     Options{Capabilities: sandboxCapabilities},
			want: func(spec corev1.PodSpec) error {
				if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
					return errors.New("service account token is mounted into the instance")
				}
				if spec.EnableServiceLinks == nil || *spec.EnableServiceLinks {
					return errors.New("service links are enabled for the instance")
				}
				return diff(corev1.RestartPolicyAlways, spec.RestartPolicy)
			},
		},
		{
			name: "a disk volume is realized by the class's resolver",
			instance: func() *computev1alpha.Instance {
				instance := newInstance(computev1alpha.SandboxContainer{Name: testContainerName})
				instance.Spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name:         testDiskVolumeName,
						VolumeSource: computev1alpha.VolumeSource{Disk: &computev1alpha.DiskTemplateVolumeSource{}},
					},
				}
				return instance
			}(),
			opts: Options{
				Capabilities: runtimeclass.Capabilities{
					Class: testClassBasalt,
					Features: []runtimeclass.Feature{
						runtimeclass.FeatureSandboxRuntime,
						runtimeclass.FeatureDiskVolumes,
					},
				},
				ResolveVolumeSource: func(volume computev1alpha.InstanceVolume) (corev1.VolumeSource, error) {
					return corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: volume.Name},
					}, nil
				},
			},
			want: func(spec corev1.PodSpec) error {
				want := []corev1.Volume{{
					Name: testDiskVolumeName,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: testDiskVolumeName},
					},
				}}
				return diff(want, spec.Volumes)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec, err := BuildPodSpec(test.instance, test.opts)
			if err != nil {
				t.Fatalf("BuildPodSpec() returned an unexpected error: %v", err)
			}
			if err := test.want(spec); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestBuildPodSpecErrors(t *testing.T) {
	tests := []struct {
		name         string
		instance     *computev1alpha.Instance
		opts         Options
		wantContains string
	}{
		{
			name: "a virtual machine instance is not translated to a pod",
			instance: &computev1alpha.Instance{
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						VirtualMachine: &computev1alpha.VirtualMachineRuntime{},
					},
				},
			},
			opts:         Options{Capabilities: sandboxCapabilities},
			wantContains: "does not declare a sandbox runtime",
		},
		{
			name: "an unsupported feature fails the build rather than being dropped",
			instance: func() *computev1alpha.Instance {
				instance := newInstance(computev1alpha.SandboxContainer{Name: testContainerName})
				instance.Spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name:         testDiskVolumeName,
						VolumeSource: computev1alpha.VolumeSource{Disk: &computev1alpha.DiskTemplateVolumeSource{}},
					},
				}
				return instance
			}(),
			opts:         Options{Capabilities: sandboxCapabilities},
			wantContains: `disk-backed volumes are not supported by the "azurite" runtime class`,
		},
		{
			name: "a class claiming disk support without a resolver is reported",
			instance: func() *computev1alpha.Instance {
				instance := newInstance(computev1alpha.SandboxContainer{Name: testContainerName})
				instance.Spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name:         testDiskVolumeName,
						VolumeSource: computev1alpha.VolumeSource{Disk: &computev1alpha.DiskTemplateVolumeSource{}},
					},
				}
				return instance
			}(),
			opts: Options{Capabilities: runtimeclass.Capabilities{
				Class: testClassBasalt,
				Features: []runtimeclass.Feature{
					runtimeclass.FeatureSandboxRuntime,
					runtimeclass.FeatureDiskVolumes,
				},
			}},
			wantContains: "supplied no volume source resolver",
		},
		{
			name: "a resolver failure is surfaced with the volume name",
			instance: func() *computev1alpha.Instance {
				instance := newInstance(computev1alpha.SandboxContainer{Name: testContainerName})
				instance.Spec.Volumes = []computev1alpha.InstanceVolume{
					{
						Name:         testDiskVolumeName,
						VolumeSource: computev1alpha.VolumeSource{Disk: &computev1alpha.DiskTemplateVolumeSource{}},
					},
				}
				return instance
			}(),
			opts: Options{
				Capabilities: runtimeclass.Capabilities{
					Class: testClassBasalt,
					Features: []runtimeclass.Feature{
						runtimeclass.FeatureSandboxRuntime,
						runtimeclass.FeatureDiskVolumes,
					},
				},
				ResolveVolumeSource: func(computev1alpha.InstanceVolume) (corev1.VolumeSource, error) {
					return corev1.VolumeSource{}, errors.New("no storage class available")
				},
			},
			wantContains: `failed to resolve volume "data"`,
		},
		{
			name: "a volume with no source is reported rather than skipped",
			instance: func() *computev1alpha.Instance {
				instance := newInstance(computev1alpha.SandboxContainer{Name: testContainerName})
				instance.Spec.Volumes = []computev1alpha.InstanceVolume{{Name: "mystery"}}
				return instance
			}(),
			opts:         Options{Capabilities: sandboxCapabilities},
			wantContains: `volume "mystery" declares no source`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildPodSpec(test.instance, test.opts)
			if err == nil {
				t.Fatal("BuildPodSpec() succeeded, want an error")
			}
			if !strings.Contains(err.Error(), test.wantContains) {
				t.Errorf("error %q does not contain %q", err, test.wantContains)
			}
		})
	}
}

func TestBuildPodSpecNotSandboxIsIdentifiable(t *testing.T) {
	instance := &computev1alpha.Instance{}
	if _, err := BuildPodSpec(instance, Options{Capabilities: sandboxCapabilities}); !errors.Is(err, ErrNotSandbox) {
		t.Errorf("error = %v, want ErrNotSandbox", err)
	}
}

func TestContainerResources(t *testing.T) {
	tests := []struct {
		name             string
		instanceType     string
		limits           corev1.ResourceList
		defaultMemoryMiB int64
		wantCPU          string
		wantMemory       string
	}{
		{
			name:         "instance type sizes the container",
			instanceType: instancetype.D1Standard2,
			wantCPU:      "1",
			wantMemory:   testMemory2Gi,
		},
		{
			name:         "explicit limits win over the catalog",
			instanceType: instancetype.D1Standard2,
			limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
			wantCPU:    "500m",
			wantMemory: "512Mi",
		},
		{
			name:         "an explicit memory limit still leaves CPU to the catalog",
			instanceType: instancetype.D1Standard2,
			limits:       corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
			wantCPU:      "1",
			wantMemory:   "512Mi",
		},
		{
			name:         "an explicit CPU limit still leaves memory to the catalog",
			instanceType: instancetype.D1Standard2,
			limits:       corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
			wantCPU:      "250m",
			wantMemory:   testMemory2Gi,
		},
		{
			name:         "an unknown instance type falls back to memory only",
			instanceType: "datumcloud/d1-standard-64",
			wantMemory:   "1Gi",
		},
		{
			name:             "the class may raise the memory fallback",
			instanceType:     "datumcloud/d1-standard-64",
			defaultMemoryMiB: 2048,
			wantMemory:       testMemory2Gi,
		},
		{
			name:       "no instance type at all still yields a viable footprint",
			wantMemory: "1Gi",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance := newInstance()
			instance.Spec.Runtime.Resources.InstanceType = test.instanceType
			container := &computev1alpha.SandboxContainer{Name: testContainerName}
			if test.limits != nil {
				container.Resources = &computev1alpha.ContainerResourceRequirements{Limits: test.limits}
			}

			got := ContainerResources(instance, container, test.defaultMemoryMiB)

			memory := got.Limits[corev1.ResourceMemory]
			if want := resource.MustParse(test.wantMemory); memory.Cmp(want) != 0 {
				t.Errorf("memory limit = %s, want %s", memory.String(), want.String())
			}

			cpu, hasCPU := got.Limits[corev1.ResourceCPU]
			if test.wantCPU == "" {
				if hasCPU {
					t.Errorf("cpu limit = %s, want no cpu limit", cpu.String())
				}
			} else {
				if !hasCPU {
					t.Fatalf("cpu limit missing, want %s", test.wantCPU)
				}
				if want := resource.MustParse(test.wantCPU); cpu.Cmp(want) != 0 {
					t.Errorf("cpu limit = %s, want %s", cpu.String(), want.String())
				}
			}

			// Guaranteed QoS, and a footprint equal to what quota claimed.
			if err := diff(got.Limits, got.Requests); err != nil {
				t.Errorf("requests differ from limits: %v", err)
			}
		})
	}
}

// TestContainerResourcesRequestsAreIndependent guards against the requests and
// limits sharing one map, where a caller adjusting one would silently move the
// other and drop the instance out of guaranteed QoS.
func TestContainerResourcesRequestsAreIndependent(t *testing.T) {
	instance := newInstance()
	resources := ContainerResources(instance, &computev1alpha.SandboxContainer{Name: testContainerName}, 0)

	resources.Requests[corev1.ResourceMemory] = resource.MustParse("1Mi")

	memory := resources.Limits[corev1.ResourceMemory]
	if want := resource.MustParse(testMemory2Gi); memory.Cmp(want) != 0 {
		t.Errorf("limit changed with the request: memory limit = %s, want %s", memory.String(), want.String())
	}
}

func TestBuildPod(t *testing.T) {
	instance := newInstance(computev1alpha.SandboxContainer{Name: testContainerName})
	instance.Labels = map[string]string{
		computev1alpha.WorkloadNameLabel:  testWorkloadName,
		computev1alpha.InstanceIndexLabel: "0",
		"customer-team":                   "payments",
	}

	pod, err := BuildPod(instance, Options{
		Capabilities:   sandboxCapabilities,
		PodLabels:      map[string]string{testManagedByKey: testManagedByValue},
		PodAnnotations: map[string]string{"cloud.unikraft.v1.instances/cni-enabled": testLabelValueTrue},
	})
	if err != nil {
		t.Fatalf("BuildPod() returned an unexpected error: %v", err)
	}

	if pod.Name != "app-0" || pod.Namespace != "project" {
		t.Errorf("pod is %s/%s, want project/app-0", pod.Namespace, pod.Name)
	}

	wantLabels := map[string]string{
		computev1alpha.WorkloadNameLabel:  testWorkloadName,
		computev1alpha.InstanceIndexLabel: "0",
		testManagedByKey:                  testManagedByValue,
	}
	if err := diff(wantLabels, pod.Labels); err != nil {
		t.Errorf("unexpected pod labels: %v", err)
	}

	wantAnnotations := map[string]string{"cloud.unikraft.v1.instances/cni-enabled": testLabelValueTrue}
	if err := diff(wantAnnotations, pod.Annotations); err != nil {
		t.Errorf("unexpected pod annotations: %v", err)
	}

	if len(pod.Spec.Containers) != 1 {
		t.Errorf("pod has %d containers, want 1", len(pod.Spec.Containers))
	}
}

// TestBuildPodDoesNotMutateOptions guards a caller reusing one Options value
// across every instance it builds.
func TestBuildPodDoesNotMutateOptions(t *testing.T) {
	labels := map[string]string{testManagedByKey: testManagedByValue}
	opts := Options{Capabilities: sandboxCapabilities, PodLabels: labels}

	instance := newInstance(computev1alpha.SandboxContainer{Name: testContainerName})
	instance.Labels = map[string]string{computev1alpha.WorkloadNameLabel: testWorkloadName}

	if _, err := BuildPod(instance, opts); err != nil {
		t.Fatalf("BuildPod() returned an unexpected error: %v", err)
	}

	if err := diff(map[string]string{testManagedByKey: testManagedByValue}, labels); err != nil {
		t.Errorf("caller's labels were mutated: %v", err)
	}
}

func TestIdentityLabels(t *testing.T) {
	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				computev1alpha.WorkloadUIDLabel:            "workload-uid",
				computev1alpha.WorkloadDeploymentUIDLabel:  "deployment-uid",
				computev1alpha.WorkloadDeploymentNameLabel: "web-dfw",
				computev1alpha.WorkloadNameLabel:           testWorkloadName,
				computev1alpha.PlacementNameLabel:          "dfw",
				computev1alpha.CityCodeLabel:               "DFW",
				computev1alpha.InstanceIndexLabel:          "0",
				computev1alpha.RuntimeClassLabel:           testClassAzurite,
				"customer-team":                            "payments",
			},
		},
	}

	// Tenant-writable labels stay off the Pod: identity that platform
	// selectors rely on must not be something a customer can set.
	want := map[string]string{
		computev1alpha.WorkloadUIDLabel:            "workload-uid",
		computev1alpha.WorkloadDeploymentUIDLabel:  "deployment-uid",
		computev1alpha.WorkloadDeploymentNameLabel: "web-dfw",
		computev1alpha.WorkloadNameLabel:           testWorkloadName,
		computev1alpha.PlacementNameLabel:          "dfw",
		computev1alpha.CityCodeLabel:               "DFW",
		computev1alpha.InstanceIndexLabel:          "0",
	}
	if err := diff(want, IdentityLabels(instance)); err != nil {
		t.Error(err)
	}

	if err := diff(map[string]string{}, IdentityLabels(&computev1alpha.Instance{})); err != nil {
		t.Errorf("an unlabelled instance should yield no identity labels: %v", err)
	}
}

func diff(want, got any) error {
	if delta := cmp.Diff(want, got, cmpopts.EquateEmpty()); delta != "" {
		return errors.New("(-want +got):\n" + delta)
	}
	return nil
}
