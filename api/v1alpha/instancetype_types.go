package v1alpha

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstanceTypeLifecyclePhase defines the lifecycle phase of an InstanceType.
type InstanceTypeLifecyclePhase string

const (
	InstanceTypePhaseActive     InstanceTypeLifecyclePhase = "Active"
	InstanceTypePhaseDeprecated InstanceTypeLifecyclePhase = "Deprecated"
	InstanceTypePhaseDisabled   InstanceTypeLifecyclePhase = "Disabled"
)

// InstanceTypeLifecycle defines the lifecycle management of an InstanceType.
type InstanceTypeLifecycle struct {
	// Phase defines the lifecycle phase of this instance type.
	// Valid values are "Active", "Deprecated", and "Disabled".
	// The lifecycle progression must follow Active -> Deprecated -> Disabled;
	// transitions cannot skip states or go backwards.
	//
	// +kubebuilder:validation:Enum=Active;Deprecated;Disabled
	// +kubebuilder:validation:Required
	Phase InstanceTypeLifecyclePhase `json:"phase"`

	// ReplacementInstanceType specifies the optional successor tier when Phase is Deprecated or Disabled.
	//
	// +kubebuilder:validation:Optional
	ReplacementInstanceType string `json:"replacementInstanceType,omitempty"`
}

// InstanceTypeResources defines the core compute dimensions of an InstanceType.
type InstanceTypeResources struct {
	// CPU specifies the compute capacity (e.g. "2" or "2000m") representing corev1.ResourceCPU.
	//
	// +kubebuilder:validation:Required
	CPU resource.Quantity `json:"cpu"`

	// Memory specifies the memory capacity (e.g. "4Gi" or "4096Mi") representing corev1.ResourceMemory.
	//
	// +kubebuilder:validation:Required
	Memory resource.Quantity `json:"memory"`
}

// InstanceTypeSpec defines the desired state of InstanceType
type InstanceTypeSpec struct {
	// DisplayName is a human-friendly display name.
	//
	// +kubebuilder:validation:Optional
	DisplayName string `json:"displayName,omitempty"`

	// Description is a human-friendly description of the instance type.
	//
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`

	// Resources defines the core dimensions of the instance type.
	//
	// +kubebuilder:validation:Required
	Resources InstanceTypeResources `json:"resources"`

	// Lifecycle declares the operator-managed lifecycle of this tier.
	//
	// +kubebuilder:validation:Required
	Lifecycle InstanceTypeLifecycle `json:"lifecycle,omitempty"`
}

// InstanceTypeStatus defines the observed state of InstanceType
type InstanceTypeStatus struct {
	// DeprecatedAt records when this instance type entered the Deprecated phase.
	// It is set by the operator and used to gate the Deprecated -> Disabled
	// transition on the configured grace period.
	// +optional
	DeprecatedAt *metav1.Time `json:"deprecatedAt,omitempty"`

	// Conditions hold the latest available observations of the InstanceType's state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

const (
	// InstanceTypeConditionReady indicates the instance type is ready to be used and valid.
	InstanceTypeConditionReady = "Ready"
)

// Condition types and reasons surfaced on Workload status when the referenced
// InstanceType leaves the Active phase. The type and reason share the same
// string, so a client can match on either without knowing the other.
const (
	// InstanceTypeConditionDeprecated is set on a Workload's status.conditions
	// when the InstanceType it references has phase Deprecated, so developers
	// see the migration target without reading the catalog themselves.
	InstanceTypeConditionDeprecated = "InstanceTypeDeprecated"
	// InstanceTypeConditionDisabled is set on a Workload's status.conditions
	// when the InstanceType it references has phase Disabled.
	InstanceTypeConditionDisabled = "InstanceTypeDisabled"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.spec.lifecycle.phase`
// +kubebuilder:printcolumn:name="CPU",type=string,JSONPath=`.spec.resources.cpu`
// +kubebuilder:printcolumn:name="Memory",type=string,JSONPath=`.spec.resources.memory`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// InstanceType is the Schema for the instancetypes API
type InstanceType struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceTypeSpec   `json:"spec,omitempty"`
	Status InstanceTypeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// InstanceTypeList contains a list of InstanceType
type InstanceTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InstanceType `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InstanceType{}, &InstanceTypeList{})
}
