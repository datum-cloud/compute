// SPDX-License-Identifier: AGPL-3.0-only

// Package instancepod translates a compute Instance into the Kubernetes Pod
// that runs it.
//
// Volumes, containers, environment, ports, mounts, and resource resolution mean
// the same thing for every runtime class. This package translates them once for
// every class that realizes instances as Pods. A class may instead provision a
// virtual machine, so this translation lives in its own package rather than in
// the runtimeclass contract.
//
// Runtime-specific policy arrives through Options: node targeting, extra Pod
// metadata, disk realization, and the features a class serves. This package
// therefore knows nothing about any particular class, and adding a class needs
// no change here.
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

// FallbackMemoryMiB is the memory, in mebibytes (MiB), given to a container
// with neither an explicit limit nor an instance type catalog entry. An
// instance with an unrecognized instance type still boots with usable memory.
const FallbackMemoryMiB int64 = 1024

// ErrNotSandbox reports an instance this package cannot translate. Only a
// sandbox of containers maps onto a Pod. The provider realizes a virtual
// machine instance itself.
var ErrNotSandbox = errors.New("instance does not declare a sandbox runtime")

// VolumeSourceResolver converts an instance volume the platform cannot
// translate on its own, today a disk, into the Pod volume source backing it. A
// class that declares runtimeclass.FeatureDiskVolumes supplies one, because
// backing a disk depends on the provider's storage integration.
type VolumeSourceResolver func(volume computev1alpha.InstanceVolume) (corev1.VolumeSource, error)

// Options carries the policy a runtime class contributes to the Pod. Each field
// is a decision the platform leaves to the class, so this package never
// branches on which class it builds for.
type Options struct {
	// Capabilities declares what the class serves. BuildPodSpec enforces it
	// before translating, so a feature the class cannot serve fails the build
	// instead of being dropped from the Pod.
	Capabilities runtimeclass.Capabilities

	// NodeSelector targets the nodes that can run this class.
	NodeSelector map[string]string

	// Tolerations admit the Pod to the taints those nodes carry.
	Tolerations []corev1.Toleration

	// PodLabels are added to the Pod alongside the platform identity labels,
	// for a provider's own ownership and selection needs. PodLabels take
	// precedence on a key conflict, so a provider can override a platform
	// label.
	PodLabels map[string]string

	// PodAnnotations are added to the Pod, usually to carry runtime
	// configuration a class reads from it. The platform or the provider owns
	// these values. Do not pass tenant-supplied metadata through them, because
	// tenant-influenced runtime configuration can break the isolation
	// boundary.
	PodAnnotations map[string]string

	// DefaultMemoryMiB overrides FallbackMemoryMiB for a class whose minimum
	// viable instance differs. Zero means FallbackMemoryMiB.
	DefaultMemoryMiB int64

	// ResolveVolumeSource realizes disk-backed volumes. Required only when the
	// class declares runtimeclass.FeatureDiskVolumes.
	ResolveVolumeSource VolumeSourceResolver
}

// identityLabelKeys are the compute-owned labels copied from an Instance onto
// its Pod. Selectors can then reach the Pods of a WorkloadDeployment, for
// example a horizontal pod autoscaler (HPA) scale target. Arbitrary Instance
// labels are tenant-writable and must not become selectable platform identity.
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
// carries. A provider that builds its own Pod object applies these so every
// class's Pods stay selectable the same way.
func IdentityLabels(instance *computev1alpha.Instance) map[string]string {
	labels := make(map[string]string, len(identityLabelKeys))
	for _, key := range identityLabelKeys {
		if value, ok := instance.Labels[key]; ok {
			labels[key] = value
		}
	}
	return labels
}

// BuildPod returns the complete Pod for an instance. The Pod takes the
// instance's name, so it is findable from the instance a customer sees.
//
// The caller sets the owner reference, which needs the provider's scheme.
// Whether the provider owns the Pod or tears it down explicitly belongs to the
// provider's lifecycle.
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
// BuildPodSpec validates the instance against the class's capabilities first,
// so an unsupported feature is reported to the customer rather than omitted
// from the result. The kubelet realizing the Pod resolves the referenced
// ConfigMaps and Secrets under its own node identity. This package reads and
// mirrors nothing.
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
		// of the cluster hosting it. Without a projected token it cannot
		// authenticate to the apiserver. Without service links, the addresses
		// of neighboring services never enter its environment.
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
			// Only a class declaring FeatureDiskVolumes reaches this branch, so
			// a missing resolver is a misconfigured class rather than something
			// the customer can act on.
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

// buildEnvFrom translates field by field because compute's EnvFromSource is a
// distinct type from the Kubernetes one. The compute API does not expose
// Kubernetes-specific sources it cannot serve.
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
// attachment without a mount path is a raw device with no Pod equivalent.
// Validation has already confirmed the class serves raw devices, and the
// provider realizes them.
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

// ContainerResources resolves the resources a container receives.
//
// Requests equal limits, so the Pod lands in the guaranteed quality of service
// (QoS) class and uses exactly what quota claimed for the instance. A request
// smaller than the limit would let an instance consume more than it was billed
// for.
//
// CPU and memory resolve independently, so a container that sets only a memory
// limit keeps that memory while the catalog sizes its CPU. Each takes the first
// of:
//
//  1. An explicit container limit.
//  2. The instance type catalog entry, which is the sizing quota accounted for.
//  3. A default, which is defaultMemoryMiB for memory and nothing for CPU. An
//     invented CPU limit would throttle an instance at a value no one chose.
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
