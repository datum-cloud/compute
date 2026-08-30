// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RuntimeClassControllerName is the name of the controller that implements a
// runtime class, as a domain-prefixed path. For example,
// "compute.datumapis.com/unikraft-provider".
//
// The name identifies which controller realizes the class, not where the class
// can run. A cell advertises the classes it can serve separately, through
// RuntimeClassServedLabel. The two are independent: a class can have a
// controller and no capacity anywhere, or capacity in a cell whose controller
// has not accepted the class.
//
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*\/[A-Za-z0-9\/\-._~%!$&'()*+,;=:]+$`
type RuntimeClassControllerName string

// RuntimeClassFeature is an optional part of the instance API that a runtime
// class may or may not serve. Features name customer-visible capabilities
// rather than implementation mechanisms, because the class contract publishes
// them and rejection messages quote them back to the customer.
//
// The set grows only when the instance API gains a capability a class could
// decline. Enumerating the features keeps a class from declaring one that no
// provider can interpret.
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
	// cannot serve disk-backed volumes.
	RuntimeClassFeatureDiskVolumes RuntimeClassFeature = "diskVolumes"

	// RuntimeClassFeatureDeviceVolumeAttachments is the ability to attach a
	// volume as a raw device. The attachment has no mount path, so the guest
	// formats and mounts it.
	RuntimeClassFeatureDeviceVolumeAttachments RuntimeClassFeature = "deviceVolumeAttachments"

	// RuntimeClassFeatureEnvFrom is the ability to populate a container's
	// environment from a whole ConfigMap or Secret rather than key by key.
	RuntimeClassFeatureEnvFrom RuntimeClassFeature = "envFrom"

	// RuntimeClassFeatureImagePullSecrets is the ability to authenticate to a
	// registry with customer-supplied credentials when pulling an instance
	// image.
	RuntimeClassFeatureImagePullSecrets RuntimeClassFeature = "imagePullSecrets"
)

// runtimeClassFeatureDescriptions maps each feature to its customer-facing
// phrase. Rejection messages quote the phrase rather than the API value so the
// message reads as product language instead of a field name.
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

// Description returns the customer-facing phrase for the feature. It falls back
// to the API value so a newly added feature stays readable without a
// description.
func (f RuntimeClassFeature) Description() string {
	if description, ok := runtimeClassFeatureDescriptions[f]; ok {
		return description
	}
	return string(f)
}

// RuntimeClassLifecycleOperation is a lifecycle action a class can perform on a
// running instance. These actions are not uniformly available across isolation
// tiers, so each class states which ones it offers rather than the platform
// implying them.
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

// RuntimeClassIsolation describes what separates a workload in the class from
// other tenants' workloads. Multi-tenant customers report this boundary to
// their own auditors, so the API publishes it and holds it stable across
// providers instead of leaving it to whichever runtime a cell runs.
type RuntimeClassIsolation struct {
	// A short, stable token for the boundary, for example "unikernel" or
	// "virtual-machine". The values are deliberately not enumerated. The
	// boundaries the platform offers grow with the catalog, and fixing them in
	// the schema would make each new tier an API change.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Required
	Boundary string `json:"boundary"`

	// A description of the boundary and what it separates, suitable for a
	// customer to show an auditor.
	//
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`
}

// RuntimeClassCapabilities describes what the class can serve. The features are
// the machine-readable half: submission rejects a workload that asks for a
// feature absent here, naming the class. The compatibility statement is the
// half a customer reads before committing an image to the tier.
type RuntimeClassCapabilities struct {
	// The optional parts of the instance API this class serves. Anything absent
	// is unsupported, so a class that omits a feature rejects requests for it
	// rather than serving it by accident.
	//
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:Optional
	Features []RuntimeClassFeature `json:"features,omitempty"`

	// What runs unmodified in this class and what does not. Customers need this
	// statement before committing an image to the tier.
	//
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Optional
	Compatibility string `json:"compatibility,omitempty"`
}

// RuntimeClassLifecycle describes what a customer can expect of an instance in
// this class: how long it takes to start, and which operations apply to it
// while it runs.
type RuntimeClassLifecycle struct {
	// The cold start a customer should plan for, measured from instance
	// creation to the instance running. Startup time is the main difference
	// between tiers, so the class publishes it rather than leaving customers to
	// measure it.
	//
	// +kubebuilder:validation:Optional
	TypicalStartupTime *metav1.Duration `json:"typicalStartupTime,omitempty"`

	// The lifecycle operations this class offers. Declaring none is accurate
	// for a class whose isolation boundary does not allow them.
	//
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:Optional
	Operations []RuntimeClassLifecycleOperation `json:"operations,omitempty"`

	// Anything about startup or lifecycle a customer needs that the fields
	// above cannot express.
	//
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`
}

// RuntimeClassSpec is the published contract for an execution tier.
//
// Pricing is deliberately absent. The service catalog publishes price and the
// billing dimensions a class is metered on, and customers are billed against
// the catalog. Restating a price here would create a second source of truth.
//
// Provider-specific parameters are deliberately absent as well. Everything here
// is what a customer is promised, and the platform reserves the right to change
// which runtime a provider uses to keep that promise. A provider slot on this
// object would also put runtime configuration one RBAC mistake away from a
// tenant, which is the escape path this design closes. Provider configuration
// stays with the provider's own deployment.
type RuntimeClassSpec struct {
	// The controller that implements this class. A provider watches for classes
	// carrying its own controller name, claims them, and reports through the
	// Accepted condition whether it can honor what they declare. A class whose
	// controller never appears stays unclaimed, which this field makes visible.
	//
	// The field says which provider realizes the class. It does not say where
	// the class can run. Cells advertise that separately, and placement uses
	// their declaration.
	//
	// +kubebuilder:validation:XValidation:message="controllerName is immutable",rule="self == oldSelf"
	// +kubebuilder:validation:Required
	ControllerName RuntimeClassControllerName `json:"controllerName"`

	// The name to show a customer choosing a tier, for example "Unikernel fast
	// path".
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
	// Admission stamps the default onto a workload and never resolves it at
	// read time. Moving the marker changes what new workloads get and leaves
	// running ones in the tier, cost, and startup profile they were created
	// with. At most one class in the catalog may set it.
	//
	// +kubebuilder:validation:Optional
	Default bool `json:"default,omitempty"`

	// What separates a workload in this class from other tenants' workloads.
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
	// declares. The condition stays Unknown until that controller reconciles
	// the class, so a class that no controller implements is visibly
	// unclaimed.
	RuntimeClassConditionAccepted = "Accepted"
)

// Reasons for the Accepted condition. These are customer-facing: they appear
// when a customer asks why a tier they selected is not usable.
const (
	// RuntimeClassReasonAccepted is set when the class's controller has claimed
	// it and can serve everything the class declares.
	RuntimeClassReasonAccepted = "Accepted"

	// RuntimeClassReasonPending is the starting state and means no controller
	// has reported on this class yet. Typically the provider that implements
	// the class is not deployed, or has not reconciled since the class was
	// published.
	RuntimeClassReasonPending = "Pending"

	// RuntimeClassReasonUnsupportedFeature is set when the class declares a
	// capability its controller cannot serve. The message names the features,
	// which makes the gap visible in the catalog instead of leaving instances
	// in the tier failing to start.
	RuntimeClassReasonUnsupportedFeature = "UnsupportedFeature"

	// RuntimeClassReasonContractNotHonored is set when the controller cannot
	// keep some other part of the published contract, such as the isolation
	// boundary it declares or a lifecycle operation it offers.
	RuntimeClassReasonContractNotHonored = "ContractNotHonored"
)

// RuntimeClassStatus is what the controller implementing the class reports
// about it. Per-location availability is not reported here. Cells advertise the
// classes they serve, and that answer belongs with placement.
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

// RuntimeClass is an execution tier a workload can run in. It publishes the
// isolation surrounding the workload, which images run unmodified, how fast
// instances start, and which lifecycle operations the tier offers.
//
// Datum owns and publishes the catalog. Customers select a class by name on a
// workload and never create one. That restriction lets the machinery behind a
// class change without a customer-visible API change, as long as the contract
// on this object still holds.
//
// The class is authoritative in the platform control plane and projected
// read-only into project control planes, so a customer can read the contract
// they select from without reaching the platform control plane.
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

// RuntimeClassList contains a list of RuntimeClass objects.
type RuntimeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RuntimeClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RuntimeClass{}, &RuntimeClassList{})
}
