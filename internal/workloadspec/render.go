// SPDX-License-Identifier: AGPL-3.0-only

// Package workloadspec turns a small, flat description of a deployment into a
// complete compute Workload manifest.
//
// The package is pure: it performs no I/O, reads no configuration, and has no
// dependency on cobra, the datumctl plugin runtime, or anything under
// internal/cmd. Render is a total function of its Input, so the same Input
// always yields the same manifest. That makes it usable both from the CLI's
// flag path and from a tool-call surface that only ever renders and returns
// YAML.
//
// # Relationship to the admission webhook
//
// Render never emits a manifest that the Workload admission webhook
// (internal/validation) would reject for structural reasons: a runtime is
// always present and is exactly one of sandbox or virtualMachine, every
// declared volume is attached at least once, a VM always carries the
// compute.datumapis.com/ssh-keys template annotation and a bootable first
// volume attachment, exactly one network interface is emitted, and scale
// settings stay inside the accepted range. Inputs that cannot satisfy those
// rules are reported as a field.ErrorList rather than rendered.
//
// Render does not police values the platform's catalogs own — the instance
// type and the boot image are passed through and left to the server, which is
// authoritative and whose accepted set changes without this package changing.
// Today the server accepts only DefaultInstanceType and DefaultBootImage.
//
// # Create-time-only decisions
//
// Several fields of a network interface are immutable once the workload
// exists, so Render's choices for them cannot be corrected by a later render
// of a changed Input — the workload has to be recreated instead:
//
//   - name: left unset, so the API server defaults it to "eth0". The guest is
//     configured against this name and the interface's address claim is named
//     after it.
//   - ipFamilies: left unset (the API server defaults it to IPv6 only) unless
//     PublicIPv4 is requested, in which case [IPv4, IPv6] is emitted so the
//     interface also holds an IPv4 address inside its network. Every family
//     listed must be satisfiable by the network or the interface is never
//     published.
//   - addresses: emitted only for PublicIPv4, as a single public-ipv4 class
//     request.
//   - reclaimPolicy: left unset, so the API server defaults it to Delete and
//     addresses are returned to IPAM when the instance slot goes away. Callers
//     that publish an address in DNS want Retain, which means editing the
//     rendered manifest before the first apply.
package workloadspec

import (
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	sigsyaml "sigs.k8s.io/yaml"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

const (
	// DefaultInstanceType is the instance type used when Input.InstanceType is
	// empty. It is currently the only type the admission webhook accepts.
	DefaultInstanceType = "datumcloud/d1-standard-2"

	// DefaultNetwork is the network attached when Input.Network is empty.
	DefaultNetwork = "default"

	// DefaultPlacementName is the name given to a placement that does not name
	// itself.
	DefaultPlacementName = "default"

	// DefaultMinReplicas is the replica floor used when a placement leaves
	// MinReplicas at zero. Scale-to-zero is not supported, so zero is read as
	// "unset" rather than as a request for no instances.
	DefaultMinReplicas int32 = 1

	// DefaultBootImage is the image a VM's boot disk is populated from when
	// VMInput.BootImage is empty. It is currently the only image the admission
	// webhook accepts.
	DefaultBootImage = "datumcloud/ubuntu-2204-lts"

	// Namespace is the namespace every rendered workload lives in. Project
	// control planes serve a single namespace.
	Namespace = "default"

	// ContainerName is the name given to the sandbox container. A rendered
	// sandbox always has exactly one container.
	ContainerName = "app"

	// BootVolumeName is the name of the disk volume a VM boots from. It is
	// always the VM's first volume attachment, which is what the webhook
	// requires of a bootable volume.
	BootVolumeName = "boot"

	// PublicIPv4Class is the IPAM class requested for a public IPv4 address.
	PublicIPv4Class = "public-ipv4"

	// diskTypePDStandard is the only disk type the platform currently offers.
	diskTypePDStandard = "pd-standard"

	// anyIPv4CIDR is the peer an exposed port is opened to. Exposing a port
	// without opening it would leave the port unreachable, so the two travel
	// together.
	anyIPv4CIDR = "0.0.0.0/0"
)

// Input is the flat description a manifest is rendered from. Every field
// except Name, Image, and Placements has a usable zero value.
type Input struct {
	// Name of the workload. Required.
	Name string

	// Image is the fully qualified container image the sandbox runs. Required
	// unless VM is set, in which case it must be empty — a VM boots from a
	// disk image, not a container image.
	Image string

	// InstanceType selects the shape of each instance. Defaults to
	// DefaultInstanceType.
	InstanceType string

	// RuntimeClass names the execution tier the instances run in. Passed
	// through verbatim and left empty when unset, so the server picks the
	// class its catalog marks as default. Nothing is defaulted here: the
	// catalog is served, this package is pure, and guessing a class would
	// settle a choice that cannot be changed after the workload exists.
	RuntimeClass string

	// Network is the name of the network the instance's single interface
	// attaches to. Defaults to DefaultNetwork.
	Network string

	// Placements says where instances run and how many. At least one is
	// required.
	Placements []Placement

	// Ports are the named ports the workload serves. Each also opens an
	// ingress network policy rule for that port from anyIPv4CIDR, because a
	// declared port that nothing is allowed to reach is not useful.
	Ports []Port

	// Env are environment variables set on the sandbox container. Ignored for
	// a VM, which has no container to set them on.
	Env []EnvVar

	// ConfigMounts project a ConfigMap or Secret into the instance's
	// filesystem. Each becomes a volume plus an attachment on the container
	// (sandbox) or on the VM.
	ConfigMounts []Mount

	// PublicIPv4 asks for a public IPv4 address in front of the interface's
	// private addressing. See the package doc: this also fixes ipFamilies at
	// [IPv4, IPv6] for the life of the workload.
	PublicIPv4 bool

	// Labels are applied both to the workload and to the instance template, so
	// they land on the instances the workload creates. Template labels take
	// part in the template hash, so changing them rolls the instances.
	Labels map[string]string

	// VM, when set, renders a virtual machine runtime instead of a sandbox.
	VM *VMInput
}

// Placement is one group of locations scaled together. Exactly one of
// Locations or LocationSelector must be set.
type Placement struct {
	// Name of the placement. Must be a DNS label. Defaults to
	// DefaultPlacementName.
	Name string

	// Locations the placement deploys to, by name, such as "us-south-dfw-1".
	// Each named location receives the placement's replicas. The set of valid
	// names is owned by the platform and is not checked here — only that the
	// names are well formed and distinct.
	Locations []string

	// LocationSelector places at every location whose topology matches, such
	// as every location in a city or a region. It is re-evaluated as locations
	// are added and removed, where Locations is a fixed list. An empty
	// selector is rejected rather than read as matching everything.
	LocationSelector *metav1.LabelSelector

	// MinReplicas is the number of instances per placement. Defaults to
	// DefaultMinReplicas; must not exceed 1000.
	MinReplicas int32
}

// Port is a named port the workload serves.
type Port struct {
	// Name of the port, referenced by other platform features. Must be a valid
	// IANA service name (a DNS label of at most 15 characters containing a
	// letter). Required.
	Name string

	// Port number, 1 to 65535. Required.
	Port int32

	// Protocol defaults to TCP.
	Protocol corev1.Protocol
}

// EnvVar is one environment variable. Exactly one of Value, ConfigMapKeyRef,
// or SecretKeyRef may be set; all three unset yields an empty value.
type EnvVar struct {
	Name            string
	Value           string
	ConfigMapKeyRef *KeyRef
	SecretKeyRef    *KeyRef
}

// KeyRef selects one key of a ConfigMap or Secret in the workload's namespace.
type KeyRef struct {
	Name string
	Key  string
}

// Mount projects a ConfigMap or Secret into the instance's filesystem.
// Exactly one of ConfigMap or Secret must be set.
type Mount struct {
	// Name of the generated volume. Must be a DNS label. Defaults to the name
	// of the referenced ConfigMap or Secret, so a ConfigMap and a Secret of
	// the same name need one of them named explicitly.
	Name string

	// ConfigMap is the name of the ConfigMap to project.
	ConfigMap string

	// Secret is the name of the Secret to project.
	Secret string

	// MountPath is the absolute path the volume appears at inside the guest.
	// Required, and unique across mounts.
	MountPath string
}

// VMInput describes a virtual machine runtime.
type VMInput struct {
	// SSHKeys are the keys authorized to log in, one per entry, each in
	// "username:ssh-public-key" form. At least one is required: a VM with no
	// key is unreachable and the webhook rejects it.
	SSHKeys []string

	// BootImage the boot disk is populated from. Defaults to
	// DefaultBootImage.
	BootImage string
}

// Defaults returns an Input pre-filled with the values the CLI advertises: the
// default instance type and network, and a single placement named "default"
// with one replica. The caller still has to supply Name, Image, and the
// placement's Locations or LocationSelector.
func Defaults() Input {
	return Input{
		InstanceType: DefaultInstanceType,
		Network:      DefaultNetwork,
		Placements: []Placement{
			{
				Name:        DefaultPlacementName,
				MinReplicas: DefaultMinReplicas,
			},
		},
	}
}

// Render builds a complete Workload from in. It returns the aggregate of a
// field.ErrorList when the input is missing something required or describes a
// manifest the admission webhook would structurally reject; the returned
// workload is nil in that case.
func Render(in Input) (*computev1alpha.Workload, error) {
	in = withDefaults(in)
	volumes := plannedVolumes(in)

	if errs := validate(in, volumes); len(errs) > 0 {
		return nil, errs.ToAggregate()
	}

	workload := &computev1alpha.Workload{
		TypeMeta: metav1.TypeMeta{
			APIVersion: computev1alpha.GroupVersion.String(),
			Kind:       "Workload",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      in.Name,
			Namespace: Namespace,
			Labels:    copyLabels(in.Labels),
		},
		Spec: computev1alpha.WorkloadSpec{
			Template:   buildTemplate(in, volumes),
			Placements: buildPlacements(in.Placements),
		},
	}

	return workload, nil
}

// plannedVolume pairs a rendered volume with the attachment that carries it
// into the runtime, so the two can never drift apart: every volume the spec
// declares must be attached at least once.
type plannedVolume struct {
	volume     computev1alpha.InstanceVolume
	attachment computev1alpha.VolumeAttachment
}

// plannedVolumes derives the instance's volumes from the input. The VM boot
// disk, when present, is always first: the webhook requires the first volume
// attachment of a VM to be a bootable one.
func plannedVolumes(in Input) []plannedVolume {
	planned := make([]plannedVolume, 0, len(in.ConfigMounts)+1)

	if in.VM != nil {
		planned = append(planned, plannedVolume{
			volume: computev1alpha.InstanceVolume{
				Name: BootVolumeName,
				VolumeSource: computev1alpha.VolumeSource{
					Disk: &computev1alpha.DiskTemplateVolumeSource{
						Template: &computev1alpha.DiskTemplateVolumeSourceTemplate{
							Spec: computev1alpha.DiskSpec{
								Type: diskTypePDStandard,
								Populator: &computev1alpha.DiskPopulator{
									Image: &computev1alpha.ImageDiskPopulator{
										Name: in.VM.BootImage,
									},
								},
							},
						},
					},
				},
			},
			// No mount path: the boot disk is attached as the boot device.
			attachment: computev1alpha.VolumeAttachment{Name: BootVolumeName},
		})
	}

	for _, m := range in.ConfigMounts {
		name := mountVolumeName(m)

		var source computev1alpha.VolumeSource
		switch {
		case m.ConfigMap != "":
			// A configMap volume names its source with `name`, while a secret
			// volume names it with `secretName`.
			source.ConfigMap = &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: m.ConfigMap},
			}
		case m.Secret != "":
			source.Secret = &corev1.SecretVolumeSource{SecretName: m.Secret}
		default:
			// Rejected by validate; skip so rendering stays total.
			continue
		}

		mountPath := m.MountPath
		planned = append(planned, plannedVolume{
			volume:     computev1alpha.InstanceVolume{Name: name, VolumeSource: source},
			attachment: computev1alpha.VolumeAttachment{Name: name, MountPath: &mountPath},
		})
	}

	return planned
}

func mountVolumeName(m Mount) string {
	switch {
	case m.Name != "":
		return m.Name
	case m.ConfigMap != "":
		return m.ConfigMap
	default:
		return m.Secret
	}
}

func buildTemplate(in Input, volumes []plannedVolume) computev1alpha.InstanceTemplateSpec {
	template := computev1alpha.InstanceTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: copyLabels(in.Labels),
		},
		Spec: computev1alpha.InstanceSpec{
			Runtime: computev1alpha.InstanceRuntimeSpec{
				// `requests` is left unset: adjustments to an instance type's
				// resources are rejected as not implemented.
				Resources: computev1alpha.InstanceRuntimeResources{
					InstanceType: in.InstanceType,
				},
				Class: in.RuntimeClass,
			},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{
				buildNetworkInterface(in),
			},
		},
	}

	if in.VM != nil {
		template.Annotations = map[string]string{
			computev1alpha.SSHKeysAnnotation: strings.Join(in.VM.SSHKeys, "\n"),
		}
		template.Spec.Runtime.VirtualMachine = &computev1alpha.VirtualMachineRuntime{
			VolumeAttachments: attachments(volumes),
			Ports:             buildPorts(in.Ports),
		}
	} else {
		template.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
			Containers: []computev1alpha.SandboxContainer{buildContainer(in, volumes)},
		}
	}

	for _, v := range volumes {
		template.Spec.Volumes = append(template.Spec.Volumes, v.volume)
	}

	return template
}

func buildContainer(in Input, volumes []plannedVolume) computev1alpha.SandboxContainer {
	return computev1alpha.SandboxContainer{
		Name:  ContainerName,
		Image: in.Image,
		// `resources` is left unset: per-container resource requirements are
		// rejected as not implemented, and the instance type carries the shape.
		Env:               buildEnv(in.Env),
		Ports:             buildPorts(in.Ports),
		VolumeAttachments: attachments(volumes),
	}
}

func attachments(volumes []plannedVolume) []computev1alpha.VolumeAttachment {
	if len(volumes) == 0 {
		return nil
	}
	out := make([]computev1alpha.VolumeAttachment, 0, len(volumes))
	for _, v := range volumes {
		out = append(out, v.attachment)
	}
	return out
}

func buildNetworkInterface(in Input) computev1alpha.InstanceNetworkInterface {
	iface := computev1alpha.InstanceNetworkInterface{
		Network: networkingv1alpha.NetworkRef{Name: in.Network},
	}

	if in.PublicIPv4 {
		iface.IPFamilies = []networkingv1alpha.IPFamily{
			networkingv1alpha.IPv4Protocol,
			networkingv1alpha.IPv6Protocol,
		}
		iface.Addresses = []computev1alpha.InstanceNetworkInterfaceAddressRequest{
			{Class: PublicIPv4Class},
		}
	}

	if ingress := buildIngressRules(in.Ports); len(ingress) > 0 {
		iface.NetworkPolicy = &computev1alpha.InstanceNetworkInterfaceNetworkPolicy{
			Ingress: ingress,
		}
	}

	return iface
}

func buildIngressRules(ports []Port) []networkingv1alpha.NetworkPolicyIngressRule {
	if len(ports) == 0 {
		return nil
	}

	rules := make([]networkingv1alpha.NetworkPolicyIngressRule, 0, len(ports))
	for _, p := range ports {
		protocol := protocolOrDefault(p.Protocol)
		port := intstr.FromInt32(p.Port)
		rules = append(rules, networkingv1alpha.NetworkPolicyIngressRule{
			Ports: []networkingv1alpha.NetworkPolicyPort{
				{Protocol: &protocol, Port: &port},
			},
			From: []networkingv1alpha.NetworkPolicyPeer{
				{IPBlock: &networkingv1alpha.IPBlock{CIDR: anyIPv4CIDR}},
			},
		})
	}
	return rules
}

func buildPorts(ports []Port) []computev1alpha.NamedPort {
	if len(ports) == 0 {
		return nil
	}

	out := make([]computev1alpha.NamedPort, 0, len(ports))
	for _, p := range ports {
		protocol := protocolOrDefault(p.Protocol)
		out = append(out, computev1alpha.NamedPort{
			Name:     p.Name,
			Port:     p.Port,
			Protocol: &protocol,
		})
	}
	return out
}

func buildEnv(env []EnvVar) []corev1.EnvVar {
	if len(env) == 0 {
		return nil
	}

	out := make([]corev1.EnvVar, 0, len(env))
	for _, e := range env {
		v := corev1.EnvVar{Name: e.Name}
		switch {
		case e.ConfigMapKeyRef != nil:
			v.ValueFrom = &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: e.ConfigMapKeyRef.Name},
					Key:                  e.ConfigMapKeyRef.Key,
				},
			}
		case e.SecretKeyRef != nil:
			v.ValueFrom = &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: e.SecretKeyRef.Name},
					Key:                  e.SecretKeyRef.Key,
				},
			}
		default:
			v.Value = e.Value
		}
		out = append(out, v)
	}
	return out
}

func buildPlacements(placements []Placement) []computev1alpha.WorkloadPlacement {
	out := make([]computev1alpha.WorkloadPlacement, 0, len(placements))
	for _, p := range placements {
		// Exactly one of the two is emitted: the API rejects a placement
		// carrying both, and validate has already refused an input with both.
		var refs []locationsv1alpha1.LocationReference
		selector := p.LocationSelector
		if len(p.Locations) > 0 {
			refs = make([]locationsv1alpha1.LocationReference, 0, len(p.Locations))
			for _, name := range p.Locations {
				refs = append(refs, locationsv1alpha1.LocationReference{Name: name})
			}
			selector = nil
		}

		out = append(out, computev1alpha.WorkloadPlacement{
			Name:             p.Name,
			Locations:        refs,
			LocationSelector: selector.DeepCopy(),
			ScaleSettings: computev1alpha.HorizontalScaleSettings{
				MinReplicas: p.MinReplicas,
				// maxReplicas is left unset: it requires scaling metrics, which
				// this input does not describe.
				InstanceManagementPolicy: computev1alpha.OrderedReadyInstanceManagementPolicyType,
			},
		})
	}
	return out
}

func protocolOrDefault(p corev1.Protocol) corev1.Protocol {
	if p == "" {
		return corev1.ProtocolTCP
	}
	return p
}

func copyLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

// withDefaults returns a copy of in with every defaultable field filled in. It
// is idempotent, so calling it twice is harmless.
func withDefaults(in Input) Input {
	if in.InstanceType == "" {
		in.InstanceType = DefaultInstanceType
	}
	if in.Network == "" {
		in.Network = DefaultNetwork
	}

	placements := make([]Placement, len(in.Placements))
	copy(placements, in.Placements)
	for i := range placements {
		if placements[i].Name == "" {
			placements[i].Name = DefaultPlacementName
		}
		if placements[i].MinReplicas == 0 {
			placements[i].MinReplicas = DefaultMinReplicas
		}
	}
	in.Placements = placements

	if in.VM != nil {
		vm := *in.VM
		if vm.BootImage == "" {
			vm.BootImage = DefaultBootImage
		}
		in.VM = &vm
	}

	return in
}

// MarshalYAML renders a workload as the YAML a user would commit. Status and
// the null creationTimestamp the object meta always carries are dropped, since
// neither is input to an apply.
func MarshalYAML(w *computev1alpha.Workload) ([]byte, error) {
	if w == nil {
		return nil, fmt.Errorf("workload is nil")
	}

	raw, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("marshalling workload: %w", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("normalizing workload: %w", err)
	}
	delete(doc, "status")
	pruneNulls(doc)

	data, err := sigsyaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshalling workload: %w", err)
	}
	return data, nil
}

// pruneNulls removes explicit nulls, which the Kubernetes object meta emits for
// creationTimestamp at every level of a manifest.
func pruneNulls(node any) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if v == nil {
				delete(n, k)
				continue
			}
			pruneNulls(v)
		}
	case []any:
		for _, v := range n {
			pruneNulls(v)
		}
	}
}

func validate(in Input, volumes []plannedVolume) field.ErrorList {
	allErrs := validateName(in.Name)

	allErrs = append(allErrs, validateRuntime(in)...)
	allErrs = append(allErrs, validateNetwork(in.Network)...)
	allErrs = append(allErrs, validatePlacements(in.Placements)...)
	allErrs = append(allErrs, validatePorts(in.Ports)...)
	allErrs = append(allErrs, validateEnv(in.Env)...)
	allErrs = append(allErrs, validateMounts(in.ConfigMounts, volumes)...)

	return allErrs
}

func validateName(name string) field.ErrorList {
	allErrs := field.ErrorList{}
	namePath := field.NewPath("name")

	if name == "" {
		return append(allErrs, field.Required(namePath, "a workload name is required"))
	}
	for _, msg := range apimachineryvalidation.NameIsDNSSubdomain(name, false) {
		allErrs = append(allErrs, field.Invalid(namePath, name, msg))
	}
	return allErrs
}

// validateRuntime enforces the "exactly one of sandbox or virtualMachine"
// rule at the input level, where the caller can still act on it.
func validateRuntime(in Input) field.ErrorList {
	allErrs := field.ErrorList{}

	if in.VM == nil {
		if in.Image == "" {
			allErrs = append(allErrs, field.Required(field.NewPath("image"),
				"a container image is required for a sandbox workload; set vm to render a virtual machine instead"))
		}
		return allErrs
	}

	if in.Image != "" {
		allErrs = append(allErrs, field.Forbidden(field.NewPath("image"),
			"a virtual machine boots from vm.bootImage, not from a container image"))
	}
	if len(in.Env) > 0 {
		allErrs = append(allErrs, field.Forbidden(field.NewPath("env"),
			"a virtual machine has no container to set environment variables on"))
	}

	return append(allErrs, validateSSHKeys(in.VM.SSHKeys)...)
}

// validateSSHKeys mirrors the webhook's parsing of the ssh-keys annotation:
// one "username:key" pair per line, with a parseable public key.
func validateSSHKeys(keys []string) field.ErrorList {
	allErrs := field.ErrorList{}
	keysPath := field.NewPath("vm", "sshKeys")

	if len(keys) == 0 {
		return append(allErrs, field.Required(keysPath,
			"a virtual machine requires at least one 'username:ssh-public-key' entry"))
	}

	for i, k := range keys {
		keyPath := keysPath.Index(i)

		user, key, found := strings.Cut(k, ":")
		if !found {
			allErrs = append(allErrs, field.Invalid(keyPath, k, "must be in the format 'username:key'"))
			continue
		}
		if user == "" {
			allErrs = append(allErrs, field.Required(keyPath, "must provide a username"))
		}
		if strings.Contains(k, "\n") {
			allErrs = append(allErrs, field.Invalid(keyPath, k, "must not contain a newline; provide one entry per key"))
			continue
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key)); err != nil {
			allErrs = append(allErrs, field.Invalid(keyPath, key, "must be a valid SSH public key"))
		}
	}

	return allErrs
}

func validateNetwork(network string) field.ErrorList {
	networkPath := field.NewPath("network")

	msgs := apimachineryvalidation.NameIsDNSLabel(network, false)
	allErrs := make(field.ErrorList, 0, len(msgs))
	for _, msg := range msgs {
		allErrs = append(allErrs, field.Invalid(networkPath, network, msg))
	}
	return allErrs
}

func validatePlacements(placements []Placement) field.ErrorList {
	allErrs := field.ErrorList{}
	placementsPath := field.NewPath("placements")

	if len(placements) == 0 {
		return append(allErrs, field.Required(placementsPath, "at least one placement is required"))
	}

	names := sets.Set[string]{}
	for i, p := range placements {
		path := placementsPath.Index(i)

		namePath := path.Child("name")
		for _, msg := range apimachineryvalidation.NameIsDNSLabel(p.Name, false) {
			allErrs = append(allErrs, field.Invalid(namePath, p.Name, msg))
		}
		if names.Has(p.Name) {
			allErrs = append(allErrs, field.Duplicate(namePath, p.Name))
		} else {
			names.Insert(p.Name)
		}

		allErrs = append(allErrs, validatePlacementLocations(p, path)...)

		minPath := path.Child("minReplicas")
		if p.MinReplicas < 0 {
			allErrs = append(allErrs, field.Invalid(minPath, p.MinReplicas, "must be greater than 0"))
		} else if p.MinReplicas > 1000 {
			allErrs = append(allErrs, field.Invalid(minPath, p.MinReplicas, "must be less than or equal to 1000"))
		}
	}

	return allErrs
}

// validatePlacementLocations enforces the API's "exactly one of locations or
// locationSelector" rule at the input level, where the caller can still act on
// it, and refuses an empty selector for the same reason the API does: it is
// not read as matching every location.
func validatePlacementLocations(p Placement, path *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	locationsPath := path.Child("locations")
	selectorPath := path.Child("locationSelector")

	switch {
	case len(p.Locations) == 0 && p.LocationSelector == nil:
		return append(allErrs, field.Required(locationsPath,
			"name at least one location, or set locationSelector to place at every location matching a topology"))
	case len(p.Locations) > 0 && p.LocationSelector != nil:
		return append(allErrs, field.Forbidden(selectorPath,
			"may not be set together with locations; name locations or select them, not both"))
	case p.LocationSelector != nil:
		if len(p.LocationSelector.MatchLabels) == 0 && len(p.LocationSelector.MatchExpressions) == 0 {
			return append(allErrs, field.Required(selectorPath,
				"an empty selector is not read as matching every location; select at least one topology key, such as "+
					locationsv1alpha1.TopologyCityCodeKey))
		}
		if _, err := metav1.LabelSelectorAsSelector(p.LocationSelector); err != nil {
			allErrs = append(allErrs, field.Invalid(selectorPath, p.LocationSelector, err.Error()))
		}
		return allErrs
	}

	seen := sets.Set[string]{}
	for i, name := range p.Locations {
		namePath := locationsPath.Index(i)
		if name == "" {
			allErrs = append(allErrs, field.Required(namePath, "a location name is required"))
			continue
		}
		for _, msg := range apimachineryvalidation.NameIsDNSSubdomain(name, false) {
			allErrs = append(allErrs, field.Invalid(namePath, name, msg))
		}
		if seen.Has(name) {
			allErrs = append(allErrs, field.Duplicate(namePath, name))
		}
		seen.Insert(name)
	}
	return allErrs
}

func validatePorts(ports []Port) field.ErrorList {
	allErrs := field.ErrorList{}
	portsPath := field.NewPath("ports")

	names := sets.Set[string]{}
	for i, p := range ports {
		path := portsPath.Index(i)

		namePath := path.Child("name")
		if p.Name == "" {
			allErrs = append(allErrs, field.Required(namePath, ""))
		} else {
			for _, msg := range utilvalidation.IsValidPortName(p.Name) {
				allErrs = append(allErrs, field.Invalid(namePath, p.Name, msg))
			}
			if names.Has(p.Name) {
				allErrs = append(allErrs, field.Duplicate(namePath, p.Name))
			} else {
				names.Insert(p.Name)
			}
		}

		for _, msg := range utilvalidation.IsValidPortNum(int(p.Port)) {
			allErrs = append(allErrs, field.Invalid(path.Child("port"), p.Port, msg))
		}

		switch p.Protocol {
		case "", corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP:
		default:
			allErrs = append(allErrs, field.NotSupported(path.Child("protocol"), p.Protocol,
				[]string{string(corev1.ProtocolTCP), string(corev1.ProtocolUDP), string(corev1.ProtocolSCTP)}))
		}
	}

	return allErrs
}

func validateEnv(env []EnvVar) field.ErrorList {
	allErrs := field.ErrorList{}
	envPath := field.NewPath("env")

	names := sets.Set[string]{}
	for i, e := range env {
		path := envPath.Index(i)

		namePath := path.Child("name")
		if e.Name == "" {
			allErrs = append(allErrs, field.Required(namePath, ""))
		} else {
			for _, msg := range utilvalidation.IsCIdentifier(e.Name) {
				allErrs = append(allErrs, field.Invalid(namePath, e.Name, msg))
			}
			if names.Has(e.Name) {
				allErrs = append(allErrs, field.Duplicate(namePath, e.Name))
			} else {
				names.Insert(e.Name)
			}
		}

		sources := 0
		if e.Value != "" {
			sources++
		}
		if e.ConfigMapKeyRef != nil {
			sources++
			allErrs = append(allErrs, validateKeyRef(*e.ConfigMapKeyRef, path.Child("configMapKeyRef"))...)
		}
		if e.SecretKeyRef != nil {
			sources++
			allErrs = append(allErrs, validateKeyRef(*e.SecretKeyRef, path.Child("secretKeyRef"))...)
		}
		if sources > 1 {
			allErrs = append(allErrs, field.Forbidden(path,
				"may not specify more than one of value, configMapKeyRef, or secretKeyRef"))
		}
	}

	return allErrs
}

func validateKeyRef(ref KeyRef, path *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	namePath := path.Child("name")
	if ref.Name == "" {
		allErrs = append(allErrs, field.Required(namePath, ""))
	} else {
		for _, msg := range apimachineryvalidation.NameIsDNSSubdomain(ref.Name, false) {
			allErrs = append(allErrs, field.Invalid(namePath, ref.Name, msg))
		}
	}

	if ref.Key == "" {
		allErrs = append(allErrs, field.Required(path.Child("key"), ""))
	}

	return allErrs
}

func validateMounts(mounts []Mount, volumes []plannedVolume) field.ErrorList {
	allErrs := field.ErrorList{}
	mountsPath := field.NewPath("configMounts")

	names := sets.Set[string]{}
	// The boot volume claims its name before any mount can.
	for _, v := range volumes {
		if v.volume.Disk != nil {
			names.Insert(v.volume.Name)
		}
	}

	paths := sets.Set[string]{}
	for i, m := range mounts {
		path := mountsPath.Index(i)

		if (m.ConfigMap == "") == (m.Secret == "") {
			allErrs = append(allErrs, field.Required(path, "must specify exactly one of configMap or secret"))
		}

		namePath := path.Child("name")
		name := mountVolumeName(m)
		if name != "" {
			for _, msg := range apimachineryvalidation.NameIsDNSLabel(name, false) {
				allErrs = append(allErrs, field.Invalid(namePath, name, msg))
			}
			if names.Has(name) {
				allErrs = append(allErrs, field.Duplicate(namePath, name))
			} else {
				names.Insert(name)
			}
		}

		mountPath := path.Child("mountPath")
		if m.MountPath == "" {
			allErrs = append(allErrs, field.Required(mountPath, ""))
		} else if paths.Has(m.MountPath) {
			allErrs = append(allErrs, field.Duplicate(mountPath, m.MountPath))
		} else {
			paths.Insert(m.MountPath)
		}
	}

	return allErrs
}
