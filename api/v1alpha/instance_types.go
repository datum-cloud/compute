package v1alpha

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// InstanceSpec defines the desired state of Instance
type InstanceSpec struct {
	// The runtime type of the instance, such as a container sandbox or a VM.
	//
	// +kubebuilder:validation:Required
	Runtime InstanceRuntimeSpec `json:"runtime,omitempty"`

	// Network interface configuration.
	//
	// Keyed by interface name so an interface keeps its identity, and therefore
	// its addresses, across updates to the rest of the list.
	//
	// Limited to a single interface until the data plane can attach more than
	// one to an instance.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	// +listType=map
	// +listMapKey=name
	NetworkInterfaces []InstanceNetworkInterface `json:"networkInterfaces,omitempty"`

	// Volumes that must be available to attach to an instance's containers or
	// Virtual Machine.
	//
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=name
	Volumes []InstanceVolume `json:"volumes,omitempty"`

	// The location which the instance has been scheduled to
	//
	// +kubebuilder:validation:Optional
	Location *networkingv1alpha.LocationReference `json:"location,omitempty"`

	// Controller contains settings driven by the controller managing the instance.
	//
	// +kubebuilder:validation:Optional
	Controller *InstanceController `json:"controller,omitempty"`
}

type InstanceController struct {
	// TemplateHash is the hash of the instance template applied for this instance.
	//
	// +kubebuilder:validation:Required
	TemplateHash string `json:"templateHash"`

	// SchedulingGates is a list of gates that must be satisfied before the
	// instance can be scheduled.
	//
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=name
	SchedulingGates []SchedulingGate `json:"schedulingGates,omitempty"`
}

type SchedulingGate struct {
	// The name of the gate.
	Name string `json:"name"`
}

type InstanceRuntimeSpec struct {
	// Resources each instance must be allocated.
	//
	// A sandbox runtime's containers may specify resource requests and
	// limits. When limits are defined on all containers, they MUST consume
	// the entire amount of resources defined here. Some resources, such
	// as a GPU, MUST have at least one container request them so that the
	// device can be presented appropriately.
	//
	// A virtual machine runtime will be provided all requested resources.
	//
	// +kubebuilder:validation:Required
	Resources InstanceRuntimeResources `json:"resources,omitempty"`

	// A sandbox is a managed isolated environment capable of running containers.
	Sandbox *SandboxRuntime `json:"sandbox,omitempty"`

	// A virtual machine is a classical VM environment, booting a full OS provided by the user via an image.
	VirtualMachine *VirtualMachineRuntime `json:"virtualMachine,omitempty"`

	// The execution tier the instance runs in. The value names a RuntimeClass
	// in the platform catalog, which Datum publishes and customers do not
	// define. Publishing a new tier adds a class instead of changing this API.
	//
	// The class is independent of the runtime shape above. Either a sandbox or
	// a virtual machine can run in any class the platform offers.
	//
	// An empty value selects the class the catalog marks as default. Admission
	// records that choice on the workload and never resolves it again, so an
	// existing workload keeps the tier, cost, and startup characteristics it
	// was created with.
	//
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Optional
	Class string `json:"class,omitempty"`
}

type SandboxRuntime struct {
	// A list of containers to run within the sandbox.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Containers []SandboxContainer `json:"containers,omitempty"`

	// An optional list of secrets in the same namespace to use for pulling images
	// used by the instance.
	//
	// +kubebuilder:validation:Optional
	ImagePullSecrets []LocalSecretReference `json:"imagePullSecrets,omitempty"`
}

type SandboxContainer struct {
	// The name of the container.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// The fully qualified container image name.
	//
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Entrypoint array to run in the container image, overriding the image's
	// ENTRYPOINT. Each element is a separate token, not a shell command — to run a
	// shell command use: ["sh", "-c", "my command"].
	//
	// If not provided, the container image's own ENTRYPOINT is used.
	//
	// +kubebuilder:validation:Optional
	Command []string `json:"command,omitempty"`

	// Arguments to the entrypoint, overriding the image's CMD. Combined with
	// Command: when Command is also set the resulting invocation is
	// append(Command, Args...).  When only Args is set it overrides CMD while
	// preserving the image's ENTRYPOINT.
	//
	// If neither Command nor Args is set, the image's own ENTRYPOINT and CMD
	// are used unchanged.
	//
	// +kubebuilder:validation:Optional
	Args []string `json:"args,omitempty"`

	// List of environment variables to set in the container.
	//
	// +kubebuilder:validation:Optional
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	// TODO(jreese) can't use corev1.EnvVar due to EnvVarSource being k8s specific,
	// so replicate the structure here too.
	Env []corev1.EnvVar `json:"env,omitempty"`

	// List of sources to populate environment variables in the container.
	// The keys defined within a source must be a C_IDENTIFIER. All invalid
	// keys will be reported as an event when the container is starting. When a
	// key exists in multiple sources, the value associated with the last source
	// will take precedence. Values defined by an Env with a duplicate key will
	// take precedence.
	//
	// +kubebuilder:validation:Optional
	EnvFrom []EnvFromSource `json:"envFrom,omitempty"`

	// The resource requirements for the container, such as CPU, memory, and GPUs.
	//
	// +kubebuilder:validation:Optional
	Resources *ContainerResourceRequirements `json:"resources,omitempty"`

	// A list of volumes to attach to the container.
	//
	// +kubebuilder:validation:Optional
	VolumeAttachments []VolumeAttachment `json:"volumeAttachments,omitempty"`

	// A list of named ports for the container.
	//
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=name
	Ports []NamedPort `json:"ports,omitempty"`
}

// EnvFromSource represents a source for a set of ConfigMaps or Secrets to be
// used as environment variables in a container.
type EnvFromSource struct {
	// An optional identifier to prepend to each key in the referenced
	// ConfigMap or Secret. Must be a valid C_IDENTIFIER.
	//
	// +kubebuilder:validation:Optional
	Prefix string `json:"prefix,omitempty"`

	// The ConfigMap to select from.
	//
	// +kubebuilder:validation:Optional
	ConfigMapRef *ConfigMapEnvSource `json:"configMapRef,omitempty"`

	// The Secret to select from.
	//
	// +kubebuilder:validation:Optional
	SecretRef *SecretEnvSource `json:"secretRef,omitempty"`
}

// ConfigMapEnvSource selects a ConfigMap to populate the environment variables
// of a container.
type ConfigMapEnvSource struct {
	// Name of the ConfigMap in the same namespace as the Workload.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Specify whether the ConfigMap must be defined.
	//
	// +kubebuilder:validation:Optional
	Optional *bool `json:"optional,omitempty"`
}

// SecretEnvSource selects a Secret to populate the environment variables
// of a container.
type SecretEnvSource struct {
	// Name of the Secret in the same namespace as the Workload.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Specify whether the Secret must be defined.
	//
	// +kubebuilder:validation:Optional
	Optional *bool `json:"optional,omitempty"`
}

type ContainerResourceRequirements struct {
	// Limits describes the maximum amount of compute resources allowed.
	//
	// +kubebuilder:validation:Optional
	Limits corev1.ResourceList `json:"limits,omitempty"`

	// Requests describes the minimum amount of compute resources required.
	//
	// +kubebuilder:validation:Optional
	Requests corev1.ResourceList `json:"requests,omitempty"`
}

type NamedPort struct {
	// The name of the port that can be referenced by other platform features.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// The port number, which can be a value between 1 and 65535.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// protocol represents the protocol (TCP, UDP, or SCTP) which traffic must match.
	// If not specified, this field defaults to TCP.
	//
	// +kubebuilder:validation:Optional
	Protocol *corev1.Protocol `json:"protocol,omitempty"`
}

type VirtualMachineRuntime struct {
	// A list of volumes to attach to the VM.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	VolumeAttachments []VolumeAttachment `json:"volumeAttachments,omitempty"`

	// A list of named ports for the virtual machine.
	//
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=name
	Ports []NamedPort `json:"ports,omitempty"`
}

type VolumeAttachment struct {
	// The name of the volume to attach as defined in InstanceSpec.Volumes.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// The path to mount the volume inside the guest OS.
	//
	// The referenced volume must be populated with a filesystem to use this
	// feature.
	//
	// For VM based instances, this functionality requires certain capabilities
	// to be annotated on the boot image, such as cloud-init.
	MountPath *string `json:"mountPath,omitempty"`
}

type InstanceRuntimeResources struct {
	// Full or partial URL of the instance type resource to use for this instance.
	//
	// For example: `datumcloud/d1-standard-2`
	//
	// May be combined with `resources` to allow for custom instance types for
	// instance families that support customization. Instance types which support
	// customization will appear in the form `<project>/<instanceFamily>-custom`.
	//
	// +kubebuilder:validation:Required
	InstanceType string `json:"instanceType"`

	// Describes adjustments to the resources defined by the instance type.
	//
	// +kubebuilder:validation:Optional
	Requests corev1.ResourceList `json:"requests,omitempty"`
}

// InstanceNetworkInterface describes one interface an instance needs. The
// fields beyond `network` and `networkPolicy` are copied verbatim onto the
// NetworkInterfaceClaim created for each instance slot, so they carry the same
// meaning, defaults, and immutability the claim API defines.
//
// The location an interface is claimed in is implicit: the claim is created in
// the control plane serving the instance, which is already location scoped.
//
// +kubebuilder:validation:XValidation:message="addresses is immutable and cannot be set, changed, or cleared after creation",rule="has(self.addresses) == has(oldSelf.addresses) && (!has(self.addresses) || self.addresses == oldSelf.addresses)"
type InstanceNetworkInterface struct {
	// The network to attach the network interface to.
	//
	// +kubebuilder:validation:Required
	Network networkingv1alpha.NetworkRef `json:"network"`

	// The name of the interface, such as eth0 or eth1. It is both the device
	// name the guest operating system sees and the suffix of the interface
	// claim's name, which is what keeps an interface's addresses with the
	// instance slot across replacement.
	//
	// Immutable, because the guest is configured against it and the claim is
	// named after it.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=15
	// +kubebuilder:default="eth0"
	// +kubebuilder:validation:XValidation:message="name is immutable and cannot be changed after creation",rule="self == oldSelf"
	Name string `json:"name,omitempty"`

	// The address families the interface must carry, in priority order. List
	// [IPv6, IPv4] for a dual-stack interface. The first family listed holds the
	// interface's primary address, which is the one reported as the instance's
	// network IP.
	//
	// Every family listed must be satisfiable or the interface is never
	// published, so asking for a family the network does not carry fails rather
	// than yielding a partially addressed interface.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:default={IPv6}
	// +kubebuilder:validation:XValidation:message="Each address family may be requested at most once",rule="self.all(f, self.exists_one(g, g == f))"
	// +kubebuilder:validation:XValidation:message="ipFamilies is immutable and cannot be changed after creation",rule="self == oldSelf"
	IPFamilies []networkingv1alpha.IPFamily `json:"ipFamilies,omitempty"`

	// Requests for addresses beyond the ones the interface holds inside its
	// network, such as a public IPv4 address in front of a private one. Each is
	// reported in the interface's `externalAddresses` status.
	//
	// Omit this field for ordinary private addressing, which is the common case.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:XValidation:message="Each address class may be requested at most once",rule="self.all(a, self.exists_one(b, b.class == a.class))"
	Addresses []InstanceNetworkInterfaceAddressRequest `json:"addresses,omitempty"`

	// What becomes of the interface, and its addresses, when the instance slot
	// it serves goes away.
	//
	// Delete returns the addresses to IPAM, so an instance recreated later comes
	// back on different addresses. Retain keeps them reserved, and billable, so a
	// later instance filling the same slot returns to the same addresses. Choose
	// Retain when an address is published in DNS, allowed through a firewall, or
	// otherwise depended on from outside.
	//
	// Both policies keep the addresses for as long as the slot exists, including
	// across instance replacement. They differ only on scale-down and deletion.
	//
	// Immutable. An address keeps the policy it was allocated under.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="Delete"
	// +kubebuilder:validation:XValidation:message="reclaimPolicy is immutable and cannot be changed after creation",rule="self == oldSelf"
	ReclaimPolicy networkingv1alpha.NetworkInterfaceReclaimPolicy `json:"reclaimPolicy,omitempty"`

	// Interface specific network policy.
	//
	// If provided, this will result in a platform managed network policy being
	// created that targets the specfiic instance interface. This network policy
	// will be of the lowest priority, and can effectively be prohibited from
	// influencing network connectivity.
	//
	// +kubebuilder:validation:Optional
	NetworkPolicy *InstanceNetworkInterfaceNetworkPolicy `json:"networkPolicy,omitempty"`
}

// InstanceNetworkInterfaceAddressRequest asks for one address beyond the ones
// the interface holds inside its network.
type InstanceNetworkInterfaceAddressRequest struct {
	// The IPAM class to allocate from, such as public-ipv4.
	//
	// A class names a kind of address, and the platform decides which pool and
	// prefix length serve it. A class never names a pool, a prefix length, or a
	// CIDR, so a class cannot be used to ask for a particular address.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Class string `json:"class"`
}

type InstanceNetworkInterfaceStatus struct {
	// The name of the interface this entry reports on, matching the name in the
	// instance's spec.
	//
	// +kubebuilder:validation:Optional
	Name string `json:"name,omitempty"`

	// The NetworkInterface bound to this entry, in the instance's namespace. An
	// infrastructure provider follows it to configure the NIC, so it never has to
	// derive the name of the claim that produced it.
	//
	// +kubebuilder:validation:Optional
	NetworkInterfaceRef *networkingv1alpha.LocalNetworkInterfaceRef `json:"networkInterfaceRef,omitempty"`

	// The addresses the interface holds inside its network, each with its prefix
	// length and, once the location has a subnet, its gateway.
	//
	// +kubebuilder:validation:Optional
	Addresses []InstanceNetworkInterfaceAddress `json:"addresses,omitempty"`

	// The addresses the interface is reachable at from outside its network, one
	// per class requested in the spec. Each is a bare address with no prefix
	// length.
	//
	// +kubebuilder:validation:Optional
	ExternalAddresses []InstanceNetworkInterfaceExternalAddress `json:"externalAddresses,omitempty"`

	// The observations of this interface's current state. Known condition types
	// are "Allocated" and "Programmed".
	//
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Single address projections of the fields above, kept for clients that read
	// one address per interface.
	//
	// +kubebuilder:validation:Optional
	Assignments InstanceNetworkInterfaceAssignmentsStatus `json:"assignments,omitempty"`
}

// InstanceNetworkInterfaceAddress is an address the interface holds inside its
// network. These are configured on the NIC itself, and always carry a prefix
// length.
type InstanceNetworkInterfaceAddress struct {
	// The address family of this entry.
	//
	// +kubebuilder:validation:Required
	Family networkingv1alpha.IPFamily `json:"family"`

	// The address the interface holds, in CIDR notation, such as 10.128.0.2/32
	// or 2001:db8:a001::1/128.
	//
	// For IPv6 this may be a block delegated to the interface rather than a
	// single address, such as 2001:db8:a001::/96. The interface owns the whole
	// block and assigns within it.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=45
	Address string `json:"address"`

	// The next hop the interface routes through for this family, such as
	// 10.128.0.1. It is empty until the subnet backing the network in this
	// location exists.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=45
	Gateway string `json:"gateway,omitempty"`

	// Marks the address projected into `assignments.networkIP`.
	//
	// Exactly one address is primary for the interface as a whole, not one per
	// family. It is the address of the first family listed in `ipFamilies`.
	//
	// +kubebuilder:validation:Optional
	Primary bool `json:"primary,omitempty"`

	// The IPAM class this address was allocated from, such as private-ipv6. It
	// is empty for addresses requested by family rather than by class.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=63
	Class string `json:"class,omitempty"`
}

// InstanceNetworkInterfaceExternalAddress is an address reachable from outside
// the network, mapped onto an address the interface holds inside it. A public
// IPv4 address in front of a private address is the usual case.
//
// Unlike an interface address, it is a bare address with no prefix length,
// because nothing configures it on the NIC.
type InstanceNetworkInterfaceExternalAddress struct {
	// The address family of this entry.
	//
	// +kubebuilder:validation:Required
	Family networkingv1alpha.IPFamily `json:"family"`

	// The externally reachable address, such as 203.0.113.10. It carries no
	// prefix length.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=45
	Address string `json:"address"`

	// The IPAM class this address was allocated from, such as public-ipv4. It
	// matches a class requested in the interface's `addresses`.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Class string `json:"class"`
}

type InstanceNetworkInterfaceAssignmentsStatus struct {
	// The IP address assigned as the primary IP from the attached network. It is
	// a projection of the primary entry in the interface's `addresses`.
	NetworkIP *string `json:"networkIP,omitempty"`

	// The external IP address used for the interface. A one to one NAT will be
	// performed for this address with the interface's network IP. It is a
	// projection of the first entry in the interface's `externalAddresses`.
	ExternalIP *string `json:"externalIP,omitempty"`
}

type InstanceNetworkInterfaceNetworkPolicy struct {
	Ingress []networkingv1alpha.NetworkPolicyIngressRule `json:"ingress,omitempty"`
}

type InstanceVolume struct {
	// Name is used to reference the volume in `volumeAttachments` for
	// containers and VMs, and will be used to derive the platform resource
	// name when required by prefixing this name with the instance name upon
	// creation.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// The type of volume to create.
	VolumeSource `json:",inline"`
}

type VolumeSource struct {
	// A persistent disk backed volume.
	Disk *DiskTemplateVolumeSource `json:"disk,omitempty"`

	// A configMap that should populate this volume
	ConfigMap *corev1.ConfigMapVolumeSource `json:"configMap,omitempty"`

	// A secret that should populate this volume
	// TODO(jreese) consider our own struct to align with configMap.name vs secret.secretName
	Secret *corev1.SecretVolumeSource `json:"secret,omitempty"`
}

type DiskTemplateVolumeSource struct {
	// Specifies a unique device name that is reflected into the
	// `/dev/disk/by-id/datumcloud-*` tree of a Linux operating system
	// running within the instance. This name can be used to reference
	// the device for mounting, resizing, and so on, from within the
	// instance.
	//
	// If not specified, the server chooses a default device name to
	// apply to this disk, in the form persistent-disk-x, where x is a
	// number assigned by Datum Cloud.
	//
	DeviceName *string `json:"deviceName,omitempty"`

	// Settings to create a new disk for an attached disk
	//
	// +kubebuilder:validation:Required
	Template *DiskTemplateVolumeSourceTemplate `json:"template,omitempty"`
}

type DiskTemplateVolumeSourceTemplate struct {
	// Metadata of the disks created from this template
	//
	// +kubebuilder:validation:Optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Describes the desired configuration of a disk
	//
	// +kubebuilder:validation:Required
	Spec DiskSpec `json:"spec,omitempty"`
}

type DiskSpec struct {
	// The type the disk, such as `pd-standard`.
	//
	// +kubebuilder:default=pd-standard
	// +kubebuilder:validation:Optional
	Type string `json:"type"`

	// The resource requirements for the disk.
	//
	// +kubebuilder:validation:Optional
	Resources *DiskResourceRequirements `json:"resources,omitempty"`

	// Populator to use while initializing the disk.
	//
	// +kubebuilder:validation:Optional
	Populator *DiskPopulator `json:"populator,omitempty"`
}

type DiskResourceRequirements struct {
	// Requests describes the minimum amount of storage resources required.
	//
	// +kubebuilder:validation:Optional
	Requests corev1.ResourceList `json:"requests,omitempty"`
}

type DiskPopulator struct {
	// Populate the disk from an image
	Image *ImageDiskPopulator `json:"image,omitempty"`

	// Populate the disk with a filesystem
	Filesystem *FilesystemDiskPopulator `json:"filesystem,omitempty"`
}

type ImageDiskPopulator struct {
	// The name of the image to populate the disk with.
	//
	// TODO(jreese) should this be a Ref field? Would want to avoid stuttering
	// 	in `populator.image.imageRef.name` though.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

type FilesystemDiskPopulator struct {
	// The type of filesystem to populate the disk with.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=ext4
	Type string `json:"type"`
}

// InstanceStatus defines the observed state of Instance
type InstanceStatus struct {
	// Represents the observations of an instance's current state.
	// Known condition types are: "Available", "Progressing"
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Network interface information
	NetworkInterfaces []InstanceNetworkInterfaceStatus `json:"networkInterfaces,omitempty"`

	// Controller contains status information about the controller managing the instance.
	//
	// +kubebuilder:validation:Optional
	Controller *InstanceControllerStatus `json:"controller,omitempty"`

	// Suspended, when true, indicates that the instance's process should be stopped
	// without releasing its placement, disk attachments, or quota allocation.
	// The provider controller stops the running container/VM.
	//
	// +kubebuilder:validation:Optional
	Suspended bool `json:"suspended,omitempty"`
}

type InstanceControllerStatus struct {
	// ObservedTemplateHash is the hash of the instance template applied for this instance.
	//
	// +kubebuilder:validation:Required
	ObservedTemplateHash string `json:"observedTemplateHash"`
}

const (
	// InstanceReady indicates that the instance is ready
	InstanceReady = "Ready"

	// InstanceAvailable indicates that the instance is available. It is True
	// when the instance is serving and does not assert that a process is
	// actively running at this instant.
	InstanceAvailable = "Available"

	// InstanceProgrammed indicates that the instance has been programmed
	InstanceProgrammed = "Programmed"

	// InstanceQuotaGranted indicates whether quota has been allocated for the instance
	InstanceQuotaGranted = "QuotaGranted"

	// ReferencedDataReady indicates whether all ConfigMaps and Secrets referenced
	// by the workload template have been resolved and delivered to the cell.
	// This condition is set on both WorkloadDeployment (resolver view) and
	// Instance (cell view).
	ReferencedDataReady = "ReferencedDataReady"
)

// Condition types reported per network interface in
// InstanceNetworkInterfaceStatus.Conditions. They mirror the conditions the
// networking API reports on an interface claim, so a client reads the
// instance's interface rather than following the reference.
const (
	// InstanceNetworkInterfaceAllocated indicates that every requested address
	// family, and every requested class, holds an address.
	InstanceNetworkInterfaceAllocated = "Allocated"

	// InstanceNetworkInterfaceProgrammed indicates that the data plane carries
	// the interface's addresses. Traffic flows only once this is true.
	InstanceNetworkInterfaceProgrammed = "Programmed"
)

const (
	// ReferencedDataReasonResolving indicates the resolver is in the process of
	// reading source ConfigMaps/Secrets from the project control plane.
	ReferencedDataReasonResolving = "Resolving"

	// ReferencedDataReasonAwaitingPropagation indicates the expected companions
	// have not yet all arrived on the cell.
	ReferencedDataReasonAwaitingPropagation = "AwaitingPropagation"

	// ReferencedDataReasonSourceNotFound indicates one or more referenced
	// ConfigMaps or Secrets could not be found in the project namespace.
	ReferencedDataReasonSourceNotFound = "SourceNotFound"

	// ReferencedDataReasonSourceUnauthorized indicates the management identity
	// does not have permission to read one or more referenced objects.
	ReferencedDataReasonSourceUnauthorized = "SourceUnauthorized"

	// ReferencedDataReasonSourceTooLarge indicates one or more referenced objects
	// exceed the allowed size limit.
	ReferencedDataReasonSourceTooLarge = "SourceTooLarge"

	// ReferencedDataReasonReady indicates all referenced data has been resolved
	// and is present on the cell.
	ReferencedDataReasonReady = "Ready"
)

const (
	InstanceQuotaGrantedReasonPendingEvaluation = "PendingEvaluation"
	InstanceQuotaGrantedReasonQuotaAvailable    = "QuotaAvailable"
	InstanceQuotaGrantedReasonQuotaExceeded     = "QuotaExceeded"
	InstanceQuotaGrantedReasonValidationFailed  = "ValidationFailed"
	InstanceProgrammedReasonPendingQuota        = "PendingQuota"

	// InstanceQuotaGrantedReasonQuotaDisabled indicates quota enforcement is
	// intentionally disabled: no credential path was configured.
	InstanceQuotaGrantedReasonQuotaDisabled = "QuotaDisabled"

	// InstanceQuotaGrantedReasonBackendUnavailable indicates quota enforcement
	// is configured but the Milo quota backend is unreachable (network error,
	// TLS failure, 401/503).
	InstanceQuotaGrantedReasonBackendUnavailable = "QuotaBackendUnavailable"

	// InstanceQuotaGrantedReasonProjectNotFound indicates the Milo project
	// referenced by this instance does not exist (404 on the project control plane).
	InstanceQuotaGrantedReasonProjectNotFound = "QuotaProjectNotFound"

	// InstanceQuotaGrantedReasonNamespaceNotFound indicates the claim namespace
	// does not exist on the Milo project control plane (FM-5).
	InstanceQuotaGrantedReasonNamespaceNotFound = "QuotaNamespaceNotFound"

	// InstanceQuotaGrantedReasonMisconfigured indicates the ResourceClaim was
	// rejected by the Milo admission plugin (403/422): ResourceRegistration absent
	// or claimingRules mismatch.
	InstanceQuotaGrantedReasonMisconfigured = "QuotaMisconfigured"

	// InstanceQuotaGrantedReasonProjectIDUnresolvable indicates the namespace
	// label required to derive the Milo project ID is missing or unreadable.
	InstanceQuotaGrantedReasonProjectIDUnresolvable = "QuotaProjectIDUnresolvable"

	// InstanceQuotaGrantedReasonNoBudget indicates the ResourceClaim exists and
	// is pending because no AllowanceBucket has been configured for the project.
	// This is distinct from PendingEvaluation (claim not yet created or first eval
	// in progress) and from QuotaExceeded (explicitly denied).
	InstanceQuotaGrantedReasonNoBudget = "QuotaNoBudget"
)

const (
	// InstanceReadyReasonSchedulingGatesPresent indicates that the instance is not ready because scheduling gates are present.
	InstanceReadyReasonSchedulingGatesPresent = "SchedulingGatesPresent"

	// InstanceReadyReasonAvailable indicates that the instance is available
	InstanceReadyReasonAvailable = "Available"

	// InstanceReadyReasonImageUnavailable indicates the provider could not pull
	// the instance image (bad name, missing credentials, registry unreachable).
	// This matches the reason written by translateWaitingReason in the unikraft
	// provider when the container enters an image-pull waiting state.
	InstanceReadyReasonImageUnavailable = "ImageUnavailable"

	// InstanceReadyReasonInstanceCrashing indicates the instance process started
	// but is repeatedly exiting and being restarted (CrashLoopBackOff in the
	// underlying runtime). This is user-actionable: the application itself is
	// failing, not the platform.
	InstanceReadyReasonInstanceCrashing = "InstanceCrashing"

	// InstanceReadyReasonConfigurationError indicates the runtime rejected the
	// instance configuration before the process could start (e.g. invalid env
	// variable injection, missing device). User must correct the workload spec.
	InstanceReadyReasonConfigurationError = "ConfigurationError"

	// InstanceReadyReasonProvisioning indicates the instance runtime is still
	// setting up the execution environment (container being created, image being
	// unpacked). This is a transient, non-actionable state.
	InstanceReadyReasonProvisioning = "Provisioning"

	// InstanceAvailableReasonStopped indicates that the instance is stopped
	InstanceAvailableReasonStopped = "Stopped"

	// InstanceAvailableReasonStarting indicates that the instance is starting
	InstanceAvailableReasonStarting = "Starting"

	// InstanceAvailableReasonStopping indicates that the instance is stopping
	InstanceAvailableReasonStopping = "Stopping"

	// InstanceAvailableReasonAvailable indicates that the instance is available
	InstanceAvailableReasonAvailable = "Available"

	// InstanceReadyReasonSuspended indicates the instance is intentionally
	// stopped due to project suspension. Its placement, disk, and quota
	// allocation are retained; the process will restart from disk on reinstatement.
	InstanceReadyReasonSuspended = "Suspended"

	// InstanceAvailableReasonSuspended indicates the instance is suspended
	// and is not currently serving traffic.
	InstanceAvailableReasonSuspended = "Suspended"

	// InstanceProgrammedReasonPendingProgramming indicates that the instance has not been programmed
	InstanceProgrammedReasonPendingProgramming = "PendingProgramming"

	// InstanceProgrammedReasonProgrammingInProgress indicates that the instance is being programmed.
	InstanceProgrammedReasonProgrammingInProgress = "ProgrammingInProgress"

	// InstanceProgrammedReasonProgrammed indicates that the instance has been programmed
	InstanceProgrammedReasonProgrammed = "Programmed"

	// InstanceProgrammedReasonImageUnavailable indicates the instance image could
	// not be pulled. Set by the infrastructure provider.
	// User action required: fix the image reference in the workload spec.
	InstanceProgrammedReasonImageUnavailable = "ImageUnavailable"

	// InstanceProgrammedReasonInstanceCrashing indicates the instance keeps
	// crashing on startup. Set by the infrastructure provider.
	// User action required: fix the workload (check logs for crash details).
	InstanceProgrammedReasonInstanceCrashing = "InstanceCrashing"

	// InstanceProgrammedReasonConfigurationError indicates the instance failed to
	// start due to a bad configuration. Set by the infrastructure provider.
	// User action required: fix the workload configuration.
	InstanceProgrammedReasonConfigurationError = "ConfigurationError"
)

// Reason constants for the top-level readiness conditions (Instance.Ready,
// WorkloadDeployment.Available, Workload.Available). These are the stable,
// machine-readable values that clients consume; they appear alongside human-readable
// messages so a single condition read is sufficient to diagnose a blocking cause.
const (
	// WorkloadReasonNetworkNotFound is set on Workload.Available when one or more
	// networks referenced by network interfaces do not exist.
	WorkloadReasonNetworkNotFound = "NetworkNotFound"

	// WorkloadDeploymentReasonNoMatchingLocation is set on WorkloadDeployment.Available
	// while the cell has not been told which location it serves, so the deployment
	// cannot be given one. The value is kept for compatibility with clients that
	// already match on it.
	WorkloadDeploymentReasonNoMatchingLocation = "NoMatchingLocation"

	// WorkloadDeploymentReasonAmbiguousServingLocation is set on
	// WorkloadDeployment.Available when more than one location has been delivered
	// to the cell. The cell will not guess which one it serves, so the deployment
	// waits until the platform resolves the conflict.
	WorkloadDeploymentReasonAmbiguousServingLocation = "AmbiguousServingLocation"

	// WorkloadDeploymentReasonCityCodeMismatch is set on
	// WorkloadDeployment.Available when the deployment asks for one city and the
	// cell serves another. It means the deployment was placed on the wrong cell,
	// which is a platform fault rather than anything the user can correct.
	WorkloadDeploymentReasonCityCodeMismatch = "CityCodeMismatch"

	// WorkloadDeploymentReasonNetworkProvisioning is set on WorkloadDeployment.Available
	// while the network binding or subnet is still being provisioned.
	// Replaces the previously-emitted inline literal "ProvisioningNetwork".
	WorkloadDeploymentReasonNetworkProvisioning = "NetworkProvisioning"

	// WorkloadDeploymentReasonInstancesProvisioning is set on WorkloadDeployment.Available
	// while instances exist but none are ready yet.
	// Replaces the previously-emitted inline literal "ProvisioningInstances".
	WorkloadDeploymentReasonInstancesProvisioning = "InstancesProvisioning"

	// WorkloadDeploymentReasonStableInstanceFound is set on WorkloadDeployment.Available
	// when at least one ready instance is present.
	WorkloadDeploymentReasonStableInstanceFound = "StableInstanceFound"

	// WorkloadDeploymentReasonReferencedDataNotReady is set on WorkloadDeployment.Available
	// and Workload.Available when the worst-blocking sub-condition is a ReferencedData
	// failure. The message carries the ReferencedDataReady sub-condition's message verbatim.
	WorkloadDeploymentReasonReferencedDataNotReady = "ReferencedDataNotReady"

	// WorkloadDeploymentReasonQuotaNotGranted is set on WorkloadDeployment.Available and
	// Workload.Available when quota is blocking one or more instances.
	WorkloadDeploymentReasonQuotaNotGranted = "QuotaNotGranted"

	// WorkloadReasonNoAvailablePlacements is set on Workload.Available when all
	// placements report no available deployments. Used as the last-resort default.
	WorkloadReasonNoAvailablePlacements = "NoAvailablePlacements"

	// WorkloadReasonNoAvailableDeployments is set on a placement's Available
	// condition when no deployment in that placement is available.
	WorkloadReasonNoAvailableDeployments = "NoAvailableDeployments"
)

type InstanceTemplateSpec struct {
	// Metadata of the instances created from this template
	//
	// +kubebuilder:validation:Optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Describes the desired configuration of an instance
	// +kubebuilder:validation:Required
	Spec InstanceSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="discovery.miloapis.com/parent-contexts=Project"

// Instance is the Schema for the instances API
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Network IP",type=string,JSONPath=`.status.networkInterfaces[0].assignments.networkIP`
// +kubebuilder:printcolumn:name="External IP",type=string,JSONPath=`.status.networkInterfaces[0].assignments.externalIP`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].message`,priority=1
// +kubebuilder:printcolumn:name="Quota",type=string,JSONPath=`.status.conditions[?(@.type=="QuotaGranted")].reason`,priority=1
type Instance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of an Instance.
	Spec InstanceSpec `json:"spec,omitempty"`

	// Status defines the current state of an Instance.
	//
	// +kubebuilder:default={conditions:{{type:"Programmed",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"},{type:"Available",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"},{type:"Ready",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"},{type:"QuotaGranted",status:"Unknown",reason:"PendingEvaluation",message:"Waiting for quota evaluation",lastTransitionTime:"1970-01-01T00:00:00Z"}}}
	Status InstanceStatus `json:"status,omitempty"`
}

// TODO(jreese) consider another type that can be owned by an `Instance`, such
// as an `InstanceRevision`, that is actually what drives lifecycle events. Need
// to think through live migration needs, and how to clearly communicate the
// current state of an instance during a config change rollout.

// +kubebuilder:object:root=true

// InstanceList contains a list of Instance
type InstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Instance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Instance{}, &InstanceList{})
}
