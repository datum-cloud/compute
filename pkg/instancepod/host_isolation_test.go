// SPDX-License-Identifier: AGPL-3.0-only

package instancepod

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/pkg/runtimeclass"
)

// These tests guard an invariant that holds for every runtime class: the
// platform never produces configuration that reaches the host. A class may sit
// outside the cell's security profile when its guest kernel confines
// capabilities, root, privilege escalation, and syscall filtering. The guest
// does not confine host directories, the host network, host process or IPC
// namespaces, or host ports, so none of these may appear in a translated Pod.

// maximalInstance exercises every part of the sandbox API this package
// translates, so a new translation that reaches the host fails the guarantee.
func maximalInstance() *computev1alpha.Instance {
	protocol := corev1.ProtocolUDP
	instance := newInstance(
		computev1alpha.SandboxContainer{
			Name:    testContainerName,
			Image:   testNginxImage,
			Command: []string{"nginx"},
			Args:    []string{"-g", "daemon off;"},
			Env: []corev1.EnvVar{
				{Name: "MODE", Value: "production"},
				{Name: "API_KEY", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: testSecretName},
						Key:                  "key",
					},
				}},
			},
			EnvFrom: []computev1alpha.EnvFromSource{
				{Prefix: "NGINX_", ConfigMapRef: &computev1alpha.ConfigMapEnvSource{Name: testConfigMapName}},
				{SecretRef: &computev1alpha.SecretEnvSource{Name: testSecretName}},
			},
			Ports: []computev1alpha.NamedPort{
				{Name: "web", Port: 80},
				{Name: "resolver", Port: 53, Protocol: &protocol},
			},
			VolumeAttachments: []computev1alpha.VolumeAttachment{
				{Name: testConfigVolumeName, MountPath: ptr.To("/etc/nginx/conf.d")},
				{Name: "tls", MountPath: ptr.To("/run/secrets")},
				{Name: testDiskVolumeName, MountPath: ptr.To("/var/cache/nginx")},
			},
			SecurityContext: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Add:  []computev1alpha.Capability{testCapNetBindService, "NET_ADMIN", testCapSysAdmin},
					Drop: []computev1alpha.Capability{testCapAll},
				},
			},
		},
		computev1alpha.SandboxContainer{
			Name:              "sidecar",
			Image:             "ghcr.io/acme/sidecar:1.0",
			VolumeAttachments: []computev1alpha.VolumeAttachment{{Name: "scratch"}},
		},
	)
	instance.Spec.Runtime.Sandbox.ImagePullSecrets = []computev1alpha.LocalSecretReference{{Name: "pull-credentials"}}
	instance.Spec.Volumes = []computev1alpha.InstanceVolume{
		{
			Name: testConfigVolumeName,
			VolumeSource: computev1alpha.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: testConfigMapName},
			}},
		},
		{
			Name:         "tls",
			VolumeSource: computev1alpha.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: testSecretName}},
		},
		{
			Name:         testDiskVolumeName,
			VolumeSource: computev1alpha.VolumeSource{Disk: &computev1alpha.DiskTemplateVolumeSource{}},
		},
		{
			Name:         "scratch",
			VolumeSource: computev1alpha.VolumeSource{Disk: &computev1alpha.DiskTemplateVolumeSource{}},
		},
	}
	return instance
}

// maximalOptions serves every sandbox feature and backs disks the way a
// provider does, with a claim per volume.
func maximalOptions() Options {
	return Options{
		Capabilities: runtimeclass.Capabilities{
			Class: testClassBasalt,
			Features: []runtimeclass.Feature{
				runtimeclass.FeatureSandboxRuntime,
				runtimeclass.FeatureConfigMapVolumes,
				runtimeclass.FeatureSecretVolumes,
				runtimeclass.FeatureDiskVolumes,
				runtimeclass.FeatureDeviceVolumeAttachments,
				runtimeclass.FeatureEnvFrom,
				runtimeclass.FeatureImagePullSecrets,
				runtimeclass.FeatureContainerCapabilities,
			},
			GrantableCapabilities: []runtimeclass.Capability{testCapNetBindService, "NET_ADMIN", testCapSysAdmin},
		},
		NodeSelector:   map[string]string{"compute.datumapis.com/runtime": "basalt"},
		Tolerations:    []corev1.Toleration{{Key: "compute.datumapis.com/runtime", Operator: corev1.TolerationOpExists}},
		PodLabels:      map[string]string{testManagedByKey: testManagedByValue},
		PodAnnotations: map[string]string{"io.katacontainers.config.hypervisor.default_memory": "2048"},
		ResolveVolumeSource: func(volume computev1alpha.InstanceVolume) (corev1.VolumeSource, error) {
			return corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: "app-0-" + volume.Name,
			}}, nil
		},
	}
}

func TestBuildPodNeverReachesTheHost(t *testing.T) {
	pod, err := BuildPod(maximalInstance(), maximalOptions())
	if err != nil {
		t.Fatalf("BuildPod() returned an unexpected error: %v", err)
	}
	spec := pod.Spec

	if spec.HostNetwork {
		t.Error("the Pod shares the host network")
	}
	if spec.HostPID {
		t.Error("the Pod shares the host process namespace")
	}
	if spec.HostIPC {
		t.Error("the Pod shares the host IPC namespace")
	}
	if spec.HostUsers != nil && *spec.HostUsers {
		t.Error("the Pod explicitly requests the host user namespace")
	}

	containers := make([]corev1.Container, 0, len(spec.Containers)+len(spec.InitContainers))
	containers = append(containers, spec.Containers...)
	containers = append(containers, spec.InitContainers...)
	for _, container := range containers {
		for _, port := range container.Ports {
			if port.HostPort != 0 {
				t.Errorf("container %q port %q binds host port %d", container.Name, port.Name, port.HostPort)
			}
			if port.HostIP != "" {
				t.Errorf("container %q port %q binds host address %q", container.Name, port.Name, port.HostIP)
			}
		}
	}
	for _, container := range spec.EphemeralContainers {
		for _, port := range container.Ports {
			if port.HostPort != 0 {
				t.Errorf("ephemeral container %q binds host port %d", container.Name, port.HostPort)
			}
		}
	}

	for _, volume := range spec.Volumes {
		if volume.HostPath != nil {
			t.Errorf("volume %q is backed by host path %q", volume.Name, volume.HostPath.Path)
		}
	}

	// A guarantee that passes because nothing was translated would prove
	// nothing, so confirm the maximal instance reached the Pod.
	if len(spec.Containers) != 2 || len(spec.Volumes) != 4 || len(spec.Containers[0].Ports) != 2 {
		t.Fatalf("the maximal instance was not fully translated: %d containers, %d volumes",
			len(spec.Containers), len(spec.Volumes))
	}
}

// TestBuildPodDropsAllCapabilitiesInEveryContainer confirms no container keeps a
// container runtime default capability, including a container that requests
// none, so a provider cannot leave one in place by omission.
func TestBuildPodDropsAllCapabilitiesInEveryContainer(t *testing.T) {
	pod, err := BuildPod(maximalInstance(), maximalOptions())
	if err != nil {
		t.Fatalf("BuildPod() returned an unexpected error: %v", err)
	}

	containers := make([]corev1.Container, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	containers = append(containers, pod.Spec.Containers...)
	containers = append(containers, pod.Spec.InitContainers...)
	for _, container := range containers {
		securityContext := container.SecurityContext
		if securityContext == nil || securityContext.Capabilities == nil ||
			!slices.Contains(securityContext.Capabilities.Drop, corev1.Capability(computev1alpha.CapabilityAll)) {
			t.Errorf("container %q does not drop ALL capabilities: %+v", container.Name, securityContext)
		}
	}

	// The sidecar requests no capabilities, so it proves the default rather
	// than a request.
	if len(pod.Spec.Containers) != 2 || pod.Spec.Containers[1].SecurityContext == nil ||
		pod.Spec.Containers[1].SecurityContext.Capabilities == nil ||
		len(pod.Spec.Containers[1].SecurityContext.Capabilities.Add) != 0 {
		t.Errorf("the container without a request was not translated to drop ALL and add nothing")
	}
}

func TestBuildPodSpecRejectsHostPathFromResolver(t *testing.T) {
	opts := maximalOptions()
	opts.ResolveVolumeSource = func(computev1alpha.InstanceVolume) (corev1.VolumeSource, error) {
		return corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib"}}, nil
	}

	_, err := BuildPodSpec(maximalInstance(), opts)
	if !errors.Is(err, ErrHostPathVolume) {
		t.Fatalf("BuildPodSpec() error = %v, want %v", err, ErrHostPathVolume)
	}
}

// hostSettingFields are the JSON field names Kubernetes uses for configuration
// that reaches the host.
var hostSettingFields = []string{"hostnetwork", "hostpid", "hostipc", "hostusers", "hostpath", "hostport", "hostip"}

// TestInstanceAPICannotExpressHostSettings walks every field a customer can set
// on an Instance or a Workload, including embedded Kubernetes types, so a field
// that could carry a host setting into translation fails here first.
func TestInstanceAPICannotExpressHostSettings(t *testing.T) {
	for _, root := range []any{computev1alpha.Instance{}, computev1alpha.Workload{}} {
		rootType := reflect.TypeOf(root)
		for _, path := range hostSettingPaths(rootType, rootType.Name(), map[reflect.Type]bool{}) {
			t.Errorf("the API can express a host setting at %s", path)
		}
	}
}

func hostSettingPaths(typ reflect.Type, path string, visited map[reflect.Type]bool) []string {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice ||
		typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || visited[typ] {
		return nil
	}
	visited[typ] = true

	var found []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		fieldPath := path
		if name != "" {
			fieldPath = path + "." + name
			for _, host := range hostSettingFields {
				if strings.EqualFold(name, host) {
					found = append(found, fieldPath)
				}
			}
		}
		found = append(found, hostSettingPaths(f.Type, fieldPath, visited)...)
	}
	return found
}
