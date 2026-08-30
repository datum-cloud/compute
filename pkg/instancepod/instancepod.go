// SPDX-License-Identifier: AGPL-3.0-only

// Package instancepod translates a compute Instance into the Kubernetes Pod
// that runs it.
//
// This is the runtime-neutral part of realizing an instance: volumes,
// containers, environment, ports, mounts, and resource resolution mean the
// same thing regardless of which runtime class ends up executing them, so they
// are translated once here and shared by every class that realizes instances
// as Pods. A class is not obliged to — one may provision a virtual machine
// instead — which is why this translation is its own package rather than part
// of the class contract in the runtimeclass package.
//
// Runtime-specific policy is an input, never a branch. Node targeting, extra
// Pod metadata, disk realization, and the set of features the class serves all
// arrive through Options, so this package holds no knowledge of any particular
// class and a new class needs no edit to it.
package instancepod

import (
	"errors"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/pkg/instancetype"
	"go.datum.net/compute/pkg/runtimeclass"
)

// FallbackMemoryMiB sizes a container whose memory is fixed by neither an
// explicit limit nor the instance type catalog. It exists only so an instance
// with an unrecognized instance type still boots with a sane footprint.
const FallbackMemoryMiB int64 = 1024

// ErrNotSandbox reports an instance whose shape this package cannot translate.
// Only a sandbox of containers maps onto a Pod; a virtual machine instance is
// realized by its provider in whatever way that provider provisions VMs.
var ErrNotSandbox = errors.New("instance does not declare a sandbox runtime")

// VolumeSourceResolver realizes an instance volume the platform cannot
// translate on its own — today, a disk — into the Pod volume source that backs
// it. A class declaring runtimeclass.FeatureDiskVolumes supplies one, since
// how a disk becomes a volume is the provider's storage integration and not
// something the platform can name for it.
type VolumeSourceResolver func(volume computev1alpha.InstanceVolume) (corev1.VolumeSource, error)

// Options carries the policy a runtime class contributes to the Pod. Every
// field here is a decision the platform deliberately does not make, so that
// this package never has to ask which class it is building for.
type Options struct {
	// Capabilities is the class's declaration of what it serves. It is
	// enforced before anything is translated, so a feature the class cannot
	// serve fails the build rather than being dropped from the Pod.
	Capabilities runtimeclass.Capabilities

	// NodeSelector targets the nodes that can run this class.
	NodeSelector map[string]string

	// Tolerations admit the Pod to the taints those nodes carry.
	Tolerations []corev1.Toleration

	// PodLabels are added to the Pod beyond the platform identity labels, for
	// a provider's own ownership and selection needs. They win on conflict, so
	// a provider is never blocked by a platform label it needs to override.
	PodLabels map[string]string

	// PodAnnotations are added to the Pod, typically to carry runtime
	// configuration a class reads off the Pod. These are platform- or
	// provider-owned; tenant-supplied metadata must not be funnelled here,
	// since runtime configuration influenced by tenant input is how isolation
	// boundaries get escaped.
	PodAnnotations map[string]string

	// DefaultMemoryMiB overrides FallbackMemoryMiB for a class whose minimum
	// viable instance differs. Zero means FallbackMemoryMiB.
	DefaultMemoryMiB int64

	// ResolveVolumeSource realizes disk-backed volumes. Required only when the
	// class declares runtimeclass.FeatureDiskVolumes.
	ResolveVolumeSource VolumeSourceResolver
}

// identityLabelKeys are the compute-owned labels copied from an Instance onto
// its Pod. They let selectors reach the Pods of a WorkloadDeployment — an HPA
// /scale target, for one — without exposing arbitrary Instance labels, which
// are tenant-writable and must not become selectable platform identity.
var identityLabelKeys = []string{
	computev1alpha.WorkloadUIDLabel,
	computev1alpha.WorkloadDeploymentUIDLabel,
	computev1alpha.WorkloadDeploymentNameLabel,
	computev1alpha.WorkloadNameLabel,
	computev1alpha.PlacementNameLabel,
	computev1alpha.CityCodeLabel,
	computev1alpha.InstanceIndexLabel,
}

// IdentityLabels returns the platform identity labels an instance's Pod
// carries. Providers that build their Pod object themselves apply these so
// every class's Pods stay selectable in the same way.
func IdentityLabels(instance *computev1alpha.Instance) map[string]string {
	labels := make(map[string]string, len(identityLabelKeys))
	for _, key := range identityLabelKeys {
		if value, ok := instance.Labels[key]; ok {
			labels[key] = value
		}
	}
	return labels
}

// BuildPod returns the complete Pod for an instance, named after it so the
// backing Pod is always findable from the instance a customer sees.
//
// The owner reference is left to the caller: it needs the provider's scheme,
// and whether a Pod is owned or explicitly torn down is part of a provider's
// lifecycle rather than of this translation.
func BuildPod(instance *computev1alpha.Instance, opts Options) (*corev1.Pod, error) {
	spec, err := BuildPodSpec(instance, opts)
	if err != nil {
		return nil, err
	}

	labels := IdentityLabels(instance)
	maps.Copy(labels, opts.PodLabels)

	annotations := make(map[string]string, len(opts.PodAnnotations))
	maps.Copy(annotations, opts.PodAnnotations)

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        instance.Name,
			Namespace:   instance.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: spec,
	}, nil
}

// BuildPodSpec translates an instance's sandbox into a PodSpec.
//
// The instance is validated against the class's capabilities first, so an
// unsupported feature is reported to the customer rather than omitted from the
// result. Name resolution of the referenced ConfigMaps and Secrets is left to
// the kubelet realizing the Pod, under its own node identity; nothing is read
// or mirrored here.
func BuildPodSpec(instance *computev1alpha.Instance, opts Options) (corev1.PodSpec, error) {
	if instance.Spec.Runtime.Sandbox == nil {
		return corev1.PodSpec{}, ErrNotSandbox
	}

	if errs := runtimeclass.ValidateInstanceSpec(instance.Spec, opts.Capabilities, field.NewPath("spec")); len(errs) > 0 {
		return corev1.PodSpec{}, errs.ToAggregate()
	}

	volumes, err := buildVolumes(instance, opts)
	if err != nil {
		return corev1.PodSpec{}, err
	}

	containers := make([]corev1.Container, 0, len(instance.Spec.Runtime.Sandbox.Containers))
	for i := range instance.Spec.Runtime.Sandbox.Containers {
		container := &instance.Spec.Runtime.Sandbox.Containers[i]
		containers = append(containers, corev1.Container{
			Name:         container.Name,
			Image:        container.Image,
			Command:      container.Command,
			Args:         container.Args,
			Env:          buildEnv(container),
			EnvFrom:      buildEnvFrom(container),
			Ports:        buildPorts(container),
			Resources:    ContainerResources(instance, container, opts.DefaultMemoryMiB),
			VolumeMounts: buildVolumeMounts(container),
		})
	}

	imagePullSecrets := make([]corev1.LocalObjectReference, 0, len(instance.Spec.Runtime.Sandbox.ImagePullSecrets))
	for _, secret := range instance.Spec.Runtime.Sandbox.ImagePullSecrets {
		imagePullSecrets = append(imagePullSecrets, corev1.LocalObjectReference{Name: secret.Name})
	}

	return corev1.PodSpec{
		Containers:       containers,
		Volumes:          volumes,
		ImagePullSecrets: imagePullSecrets,
		// An instance runs customer code and must not reach the Kubernetes API
		// of the cluster hosting it. Without a projected token the identity the
		// apiserver assigns cannot authenticate, and without service links the
		// addresses of neighbouring services never enter its environment.
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		RestartPolicy:                corev1.RestartPolicyAlways,
		NodeSelector:                 opts.NodeSelector,
		Tolerations:                  opts.Tolerations,
	}, nil
}

func buildVolumes(instance *computev1alpha.Instance, opts Options) ([]corev1.Volume, error) {
	volumes := make([]corev1.Volume, 0, len(instance.Spec.Volumes))
	for _, volume := range instance.Spec.Volumes {
		switch {
		case volume.ConfigMap != nil:
			volumes = append(volumes, corev1.Volume{
				Name:         volume.Name,
				VolumeSource: corev1.VolumeSource{ConfigMap: volume.ConfigMap},
			})
		case volume.Secret != nil:
			volumes = append(volumes, corev1.Volume{
				Name:         volume.Name,
				VolumeSource: corev1.VolumeSource{Secret: volume.Secret},
			})
		case volume.Disk != nil:
			// Reachable only for a class declaring FeatureDiskVolumes, so a
			// missing resolver is that class misconfiguring itself rather than
			// anything the customer can act on.
			if opts.ResolveVolumeSource == nil {
				return nil, fmt.Errorf(
					"runtime class %q declares disk-backed volume support but supplied no volume source resolver",
					opts.Capabilities.Class)
			}
			source, err := opts.ResolveVolumeSource(volume)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve volume %q: %w", volume.Name, err)
			}
			volumes = append(volumes, corev1.Volume{Name: volume.Name, VolumeSource: source})
		default:
			return nil, fmt.Errorf("volume %q declares no source", volume.Name)
		}
	}
	return volumes, nil
}

// buildEnv carries ValueFrom through untouched so a customer's ConfigMap and
// Secret key references resolve inside the instance instead of arriving empty.
func buildEnv(container *computev1alpha.SandboxContainer) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, len(container.Env))
	for _, variable := range container.Env {
		env = append(env, corev1.EnvVar{
			Name:      variable.Name,
			Value:     variable.Value,
			ValueFrom: variable.ValueFrom,
		})
	}
	return env
}

// buildEnvFrom translates field by field: compute's EnvFromSource is not the
// Kubernetes type, because the compute API cannot expose Kubernetes-specific
// sources it has no way to serve.
func buildEnvFrom(container *computev1alpha.SandboxContainer) []corev1.EnvFromSource {
	envFrom := make([]corev1.EnvFromSource, 0, len(container.EnvFrom))
	for _, source := range container.EnvFrom {
		translated := corev1.EnvFromSource{Prefix: source.Prefix}
		if source.ConfigMapRef != nil {
			translated.ConfigMapRef = &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: source.ConfigMapRef.Name},
				Optional:             source.ConfigMapRef.Optional,
			}
		}
		if source.SecretRef != nil {
			translated.SecretRef = &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: source.SecretRef.Name},
				Optional:             source.SecretRef.Optional,
			}
		}
		envFrom = append(envFrom, translated)
	}
	return envFrom
}

func buildPorts(container *computev1alpha.SandboxContainer) []corev1.ContainerPort {
	ports := make([]corev1.ContainerPort, 0, len(container.Ports))
	for _, port := range container.Ports {
		translated := corev1.ContainerPort{
			Name:          port.Name,
			ContainerPort: port.Port,
		}
		if port.Protocol != nil {
			translated.Protocol = *port.Protocol
		}
		ports = append(ports, translated)
	}
	return ports
}

// buildVolumeMounts maps only the attachments that name a mount path. An
// attachment without one is a raw device the guest is expected to handle
// itself, which has no Pod equivalent; validation has already confirmed the
// class serves those, so realizing them is the provider's to do.
func buildVolumeMounts(container *computev1alpha.SandboxContainer) []corev1.VolumeMount {
	mounts := make([]corev1.VolumeMount, 0, len(container.VolumeAttachments))
	for _, attachment := range container.VolumeAttachments {
		if attachment.MountPath == nil {
			continue
		}
		mounts = append(mounts, corev1.VolumeMount{
			Name:      attachment.Name,
			MountPath: *attachment.MountPath,
		})
	}
	return mounts
}

// ContainerResources resolves what a container is actually given.
//
// Requests equal limits so the Pod lands in the guaranteed QoS class and its
// footprint is exactly what quota claimed for the instance — a request smaller
// than the limit would let an instance consume more than it was billed for.
//
// Each dimension resolves independently, so a container that sets only a
// memory limit keeps that memory while the catalog still sizes its CPU:
//
//  1. An explicit container limit for the dimension always wins.
//  2. The instance type catalog, which is the common case and the sizing quota
//     accounted for.
//  3. A default: defaultMemoryMiB for memory, and nothing for CPU, since an
//     invented CPU limit would throttle an instance at a number no one chose.
func ContainerResources(
	instance *computev1alpha.Instance,
	container *computev1alpha.SandboxContainer,
	defaultMemoryMiB int64,
) corev1.ResourceRequirements {
	if defaultMemoryMiB <= 0 {
		defaultMemoryMiB = FallbackMemoryMiB
	}

	var explicitCPUMillicores, explicitMemoryMiB int64
	if container != nil && container.Resources != nil && container.Resources.Limits != nil {
		if cpu := container.Resources.Limits.Cpu(); cpu != nil && !cpu.IsZero() {
			explicitCPUMillicores = cpu.MilliValue()
		}
		if memory := container.Resources.Limits.Memory(); memory != nil && !memory.IsZero() {
			explicitMemoryMiB = memory.Value() / (1024 * 1024)
		}
	}

	var catalogCPUMillicores, catalogMemoryMiB int64
	if instance != nil {
		if sizing, ok := instancetype.Lookup(instance.Spec.Runtime.Resources.InstanceType); ok {
			catalogCPUMillicores = sizing.CPUMillicores
			catalogMemoryMiB = sizing.MemoryMiB
		}
	}

	cpuMillicores := explicitCPUMillicores
	if cpuMillicores == 0 {
		cpuMillicores = catalogCPUMillicores
	}

	memoryMiB := explicitMemoryMiB
	if memoryMiB == 0 {
		memoryMiB = catalogMemoryMiB
	}
	if memoryMiB == 0 {
		memoryMiB = defaultMemoryMiB
	}

	resources := corev1.ResourceList{
		corev1.ResourceMemory: *resource.NewQuantity(memoryMiB*1024*1024, resource.BinarySI),
	}
	if cpuMillicores > 0 {
		resources[corev1.ResourceCPU] = *resource.NewMilliQuantity(cpuMillicores, resource.DecimalSI)
	}

	return corev1.ResourceRequirements{
		Requests: resources.DeepCopy(),
		Limits:   resources,
	}
}
