// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RuntimeClassControllerName is the name of the controller that implements a
// runtime class, as a domain-prefixed path — for example
// "compute.datumapis.com/unikraft-provider".
//
// This is who realizes the class, not where it can be run. A cell separately
// advertises the classes it can serve (see RuntimeClassServedLabel), and the
// two answers are independent: a class can have a provider and no capacity
// anywhere, or capacity in a cell whose provider has not accepted the class.
//
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*\/[A-Za-z0-9\/\-._~%!$&'()*+,;=:]+$`
type RuntimeClassControllerName string

// RuntimeClassFeature is an optional part of the instance API that a runtime
// class may or may not be able to serve. Features name customer-visible
// capabilities, not implementation mechanisms, because they are published in
// the class contract and quoted back in the message a customer reads when a
// class rejects their instance.
//
// The set of features grows only when the instance API grows a capability a
// class could decline, which is why it is enumerated here: a class that
// declares a feature nobody defined would promise something no provider can
// read.
//
// +kubebuilder:validation:Enum=sandboxRuntime;virtualMachineRuntime;configMapVolumes;secretVolumes;diskVolumes;deviceVolumeAttachments;envFrom;imagePullSecrets
type RuntimeClassFeature string

const (
	// RuntimeClassFeatureSandboxRuntime is the ability to run an instance
	// shaped as a sandbox of containers.
	RuntimeClassFeatureSandboxRuntime RuntimeClassFeature = "sandboxRuntime"

	// RuntimeClassFeatureVirtualMachineRuntime is the ability to run an
	// instance shaped as a virtual machine booting a customer-supplied image.
	RuntimeClassFeatureVirtualMachineRuntime RuntimeClassFeature = "virtualMachineRuntime"

	// RuntimeClassFeatureConfigMapVolumes is the ability to present a ConfigMap
	// to an instance as a volume.
	RuntimeClassFeatureConfigMapVolumes RuntimeClassFeature = "configMapVolumes"

	// RuntimeClassFeatureSecretVolumes is the ability to present a Secret to an
	// instance as a volume.
	RuntimeClassFeatureSecretVolumes RuntimeClassFeature = "secretVolumes"

	// RuntimeClassFeatureDiskVolumes is the ability to back a volume with a
	// persistent disk. A class whose root filesystem lives in RAM typically
	// cannot.
	RuntimeClassFeatureDiskVolumes RuntimeClassFeature = "diskVolumes"

	// RuntimeClassFeatureDeviceVolumeAttachments is the ability to attach a
	// volume as a raw device — an attachment with no mount path — leaving the
	// guest to format and mount it.
	RuntimeClassFeatureDeviceVolumeAttachments RuntimeClassFeature = "deviceVolumeAttachments"

	// RuntimeClassFeatureEnvFrom is the ability to populate a container's
	// environment from a whole ConfigMap or Secret rather than key by key.
	RuntimeClassFeatureEnvFrom RuntimeClassFeature = "envFrom"

	// RuntimeClassFeatureImagePullSecrets is the ability to authenticate to a
	// registry with customer-supplied credentials when pulling an instance
	// image.
	RuntimeClassFeatureImagePullSecrets RuntimeClassFeature = "imagePullSecrets"
)

// runtimeClassFeatureDescriptions carries the customer-facing phrase for each
// feature. Rejections quote this rather than the API value so the message reads
// as product language instead of a field name.
var runtimeClassFeatureDescriptions = map[RuntimeClassFeature]string{
	RuntimeClassFeatureSandboxRuntime:          "container sandbox instances",
	RuntimeClassFeatureVirtualMachineRuntime:   "virtual machine instances",
	RuntimeClassFeatureConfigMapVolumes:        "ConfigMap-backed volumes",
	RuntimeClassFeatureSecretVolumes:           "Secret-backed volumes",
	RuntimeClassFeatureDiskVolumes:             "disk-backed volumes",
	RuntimeClassFeatureDeviceVolumeAttachments: "volumes attached as raw devices",
	RuntimeClassFeatureEnvFrom:                 "environment variables sourced from a whole ConfigMap or Secret",
	RuntimeClassFeatureImagePullSecrets:        "image pull secrets",
}

// Description returns the customer-facing phrase for the feature, falling back
// to the API value so a newly added feature is still readable if its
// description was forgotten.
func (f RuntimeClassFeature) Description() string {
	if description, ok := runtimeClassFeatureDescriptions[f]; ok {
		return description
	}
	return string(f)
}

// RuntimeClassLifecycleOperation is a lifecycle action a class can perform on a
// running instance. These are what the roadmap's cost story leans on, and they
// are not uniformly available across isolation tiers, so each class states
// which of them it offers rather than the platform implying them.
//
// +kubebuilder:validation:Enum=Suspend;Resume;Snapshot
type RuntimeClassLifecycleOperation string

const (
	// RuntimeClassLifecycleSuspend stops an instance's execution while keeping
	// it resumable.
	RuntimeClassLifecycleSuspend RuntimeClassLifecycleOperation = "Suspend"

	// RuntimeClassLifecycleResume returns a suspended instance to execution.
	RuntimeClassLifecycleResume RuntimeClassLifecycleOperation = "Resume"

	// RuntimeClassLifecycleSnapshot captures an instance's state so a later
	// instance can start from it rather than from boot.
	RuntimeClassLifecycleSnapshot RuntimeClassLifecycleOperation = "Snapshot"
)

// RuntimeClassIsolation is the class's answer to "what separates my workload
// from my neighbor's". Multi-tenant customers have to give this answer to their
// own auditors, so it is published in the API and held stable across providers
// rather than being a property of whichever runtime a cell happens to run.
type RuntimeClassIsolation struct {
	// A short, stable token for the boundary — for example "unikernel" or
	// "virtual-machine". It is deliberately not enumerated: the boundaries the
	// platform can offer grow with the catalog, and pinning them in the schema
	// would make each new tier an API change.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Required
	Boundary string `json:"boundary"`

	// A sentence a customer can put in front of an auditor describing what the
	// boundary is and what it separates.
	//
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`
}

// RuntimeClassCapabilities is what the class can serve. The features are the
// machine-readable half — a workload asking for anything absent here is
// rejected when it is submitted, naming the class — and the compatibility
// statement is the half a customer reads before committing.
type RuntimeClassCapabilities struct {
	// The optional parts of the instance API this class serves. Anything absent
	// is unsupported, so a class that forgets to declare a feature turns the
	// request down loudly rather than serving it by accident.
	//
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:Optional
	Features []RuntimeClassFeature `json:"features,omitempty"`

	// What runs unmodified in this class and what does not. This is the honest,
	// unglamorous part of the contract, and the part customers most need before
	// they commit an image to a tier.
	//
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Optional
	Compatibility string `json:"compatibility,omitempty"`
}

// RuntimeClassLifecycle is what a customer can expect of an instance in this
// class from the moment it is created: how long it takes to be running, and
// what can be done to it while it is.
type RuntimeClassLifecycle struct {
	// The cold start a customer should plan for — the time from an instance
	// being created to it running. It is the headline difference between tiers
	// and the reason a customer picks one, so it is published rather than
	// discovered.
	//
	// +kubebuilder:validation:Optional
	TypicalStartupTime *metav1.Duration `json:"typicalStartupTime,omitempty"`

	// The lifecycle operations this class offers. Declaring none is a truthful
	// answer, and a better one than implying a tier can do what its isolation
	// boundary does not allow.
	//
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:Optional
	Operations []RuntimeClassLifecycleOperation `json:"operations,omitempty"`

	// Anything about startup or lifecycle a customer needs that the fields
	// above cannot say.
	//
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`
}

// RuntimeClassSpec is the published contract for an execution tier.
//
// Pricing is deliberately absent. Price and the billing dimensions a class is
// metered on are published by the service catalog, which is what customers are
// actually billed against; restating a number here would create a second source
// of truth for it.
//
// There is deliberately no reference to provider-specific parameters either.
// Everything here is what a customer is promised, and which runtime a provider
// reaches for to keep that promise is not a customer-visible decision — the
// platform reserves the right to change it. Handing providers a slot on this
// object would also put runtime configuration one RBAC mistake away from a
// tenant, and tenant-influenced runtime configuration is the escape path this
// design is meant to close. Provider configuration stays with the provider's
// own deployment, and can be given a sanctioned home later without breaking
// anything published here.
type RuntimeClassSpec struct {
	// The controller that implements this class. A provider watches for the
	// classes carrying its own controller name, claims them, and reports
	// whether it can honor what they declare via the Accepted condition. A
	// class whose controller never appears is a promise nothing implements,
	// which is exactly what this field makes visible.
	//
	// This says which provider realizes the class. It does not say where the
	// class can run: cells advertise that separately, and placement uses that
	// declaration.
	//
	// +kubebuilder:validation:XValidation:message="controllerName is immutable",rule="self == oldSelf"
	// +kubebuilder:validation:Required
	ControllerName RuntimeClassControllerName `json:"controllerName"`

	// The name to show a customer choosing a tier, e.g. "Unikernel fast path".
	//
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Optional
	DisplayName string `json:"displayName,omitempty"`

	// What this tier is for and who should choose it.
	//
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`

	// Whether an instance that selects no class runs in this one.
	//
	// The default is stamped onto a workload when it is admitted, never
	// resolved at read time, so moving this marker changes what new workloads
	// get and leaves running ones in the tier, cost, and startup profile they
	// were created with. At most one class in the catalog may set it.
	//
	// +kubebuilder:validation:Optional
	Default bool `json:"default,omitempty"`

	// What separates a workload in this class from its neighbors.
	//
	// +kubebuilder:validation:Required
	Isolation RuntimeClassIsolation `json:"isolation"`

	// What this class can serve, and what it cannot.
	//
	// +kubebuilder:validation:Required
	Capabilities RuntimeClassCapabilities `json:"capabilities"`

	// How quickly instances in this class start, and what can be done to them
	// once they are running.
	//
	// +kubebuilder:validation:Optional
	Lifecycle RuntimeClassLifecycle `json:"lifecycle,omitempty"`
}

// Condition types reported on a RuntimeClass.
const (
	// RuntimeClassConditionAccepted reports whether the controller named in
	// spec.controllerName has claimed this class and can honor everything it
	// declares. It stays Unknown until that controller reconciles the class, so
	// a class nothing implements is visibly unclaimed rather than silently
	// broken.
	RuntimeClassConditionAccepted = "Accepted"
)

// Reasons for the Accepted condition. These are customer-facing: they appear
// when a customer asks why a tier they selected is not usable.
const (
	// RuntimeClassReasonAccepted is set when the class's controller has claimed
	// it and can serve everything the class declares.
	RuntimeClassReasonAccepted = "Accepted"

	// RuntimeClassReasonPending is the starting state, and means no controller
	// has reported on this class yet — normally because the provider that
	// implements it has not been deployed, or has not reconciled since the
	// class was published.
	RuntimeClassReasonPending = "Pending"

	// RuntimeClassReasonUnsupportedFeature is set when the class declares a
	// capability its controller cannot serve. The message names the features,
	// which is what turns "instances in this tier never start" into a gap
	// visible in the catalog.
	RuntimeClassReasonUnsupportedFeature = "UnsupportedFeature"

	// RuntimeClassReasonContractNotHonored is set when the controller cannot
	// keep some other part of the published contract — the isolation boundary
	// it declares, or a lifecycle operation it offers.
	RuntimeClassReasonContractNotHonored = "ContractNotHonored"
)

// RuntimeClassStatus is what the controller implementing the class reports
// back about it. Availability per location is not reported here: cells
// advertise the classes they serve, and that answer belongs with placement.
type RuntimeClassStatus struct {
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:metadata:annotations="discovery.miloapis.com/parent-contexts=Platform,Project"

// RuntimeClass is an execution tier a workload can be run in: a published
// promise about the isolation surrounding it, what images run in it unmodified,
// how fast it starts, and what lifecycle operations it offers.
//
// The catalog is owned and published by Datum. Customers select a class by name
// on a workload; they never create one. That is what lets the machinery behind
// a class change without a customer-visible API change, as long as the promise
// on this object still holds.
//
// The class is authoritative in the platform control plane and projected
// read-only into project control planes, so a customer can read the contract
// they are selecting from without reaching the platform's own plane.
//
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Isolation",type=string,JSONPath=`.spec.isolation.boundary`
// +kubebuilder:printcolumn:name="Default",type=boolean,JSONPath=`.spec.default`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Controller",type=string,JSONPath=`.spec.controllerName`,priority=1
// +kubebuilder:printcolumn:name="Startup",type=string,JSONPath=`.spec.lifecycle.typicalStartupTime`,priority=1
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].message`,priority=1
type RuntimeClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the published contract for this execution tier.
	Spec RuntimeClassSpec `json:"spec,omitempty"`

	// Status is what the controller implementing this class reports about it.
	//
	// +kubebuilder:default={conditions:{{type:"Accepted",status:"Unknown",reason:"Pending",message:"Waiting for the class controller",lastTransitionTime:"1970-01-01T00:00:00Z"}}}
	Status RuntimeClassStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RuntimeClassList contains a list of RuntimeClass
type RuntimeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RuntimeClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RuntimeClass{}, &RuntimeClassList{})
}
