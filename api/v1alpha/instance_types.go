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
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
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

type InstanceNetworkInterface struct {
	// The network to attach the network interface to.
	//
	// +kubebuilder:validation:Required
	Network networkingv1alpha.NetworkRef `json:"network"`

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

type InstanceNetworkInterfaceStatus struct {
	Assignments InstanceNetworkInterfaceAssignmentsStatus `json:"assignments,omitempty"`
}

type InstanceNetworkInterfaceAssignmentsStatus struct {
	// The IP address assigned as the primary IP from the attached network.
	NetworkIP *string `json:"networkIP,omitempty"`

	// The external IP address used for the interface. A one to one NAT will be
	// performed for this address with the interface's network IP.
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

	// InstanceRunning indicates that the instance is running
	InstanceRunning = "Running"

	// InstanceProgrammed indicates that the instance has been programmed
	InstanceProgrammed = "Programmed"
)

const (
	// InstanceReadyReasonSchedulingGatesPresent indicates that the instance is not ready because scheduling gates are present.
	InstanceReadyReasonSchedulingGatesPresent = "SchedulingGatesPresent"

	// InstanceReadyReasonRunning indicates that the instance is running
	InstanceReadyReasonRunning = "Running"

	// InstanceRunningReasonStopped indicates that the instance is stopped
	InstanceRunningReasonStopped = "Stopped"

	// InstanceRunningReasonStarting indicates that the instance is starting
	InstanceRunningReasonStarting = "Starting"

	// InstanceRunningReasonStopping indicates that the instance is stopping
	InstanceRunningReasonStopping = "Stopping"

	// InstanceRunningReasonRunning indicates that the instance is running
	InstanceRunningReasonRunning = "Running"

	// InstanceProgrammedReasonPendingProgramming indicates that the instance has not been programmed
	InstanceProgrammedReasonPendingProgramming = "PendingProgramming"

	// InstanceProgrammedReasonProgrammingInProgress indicates that the instance is being programmed.
	InstanceProgrammedReasonProgrammingInProgress = "ProgrammingInProgress"

	// InstanceProgrammedReasonProgrammed indicates that the instance has been programmed
	InstanceProgrammedReasonProgrammed = "Programmed"
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
type Instance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of an Instance.
	Spec InstanceSpec `json:"spec,omitempty"`

	// Status defines the current state of an Instance.
	//
	// +kubebuilder:default={conditions:{{type:"Programmed",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"},{type:"Running",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"},{type:"Ready",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"}}}
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
