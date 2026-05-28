// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetworkBindingSpec defines the desired state of NetworkBinding
type NetworkBindingSpec struct {
	// The network that the binding is for.
	//
	// +kubebuilder:validation:Required
	Network NetworkRef `json:"network,omitempty"`

	// The location of where a network binding exists.
	//
	// +kubebuilder:validation:Required
	Location LocationReference `json:"location,omitempty"`
}

// NetworkBindingObjectReference contains sufficient information for
// controllers to leverage unstructured or structured clients to interact with
// the bound resources.
type NetworkBindingObjectReference struct {
	// API version of the referent.
	//
	// +kubebuilder:validation:Required
	APIVersion string `json:"apiVersion"`

	// Kind of the referent.
	//
	// +kubebuilder:validation:Required
	Kind string `json:"kind,omitempty"`

	// Namespace of the referent.
	//
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace,omitempty"`

	// Name of the referent.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`
}

// NetworkBindingStatus defines the observed state of NetworkBinding
type NetworkBindingStatus struct {
	NetworkContextRef *NetworkContextRef `json:"networkContextRef,omitempty"`

	// Represents the observations of a network binding's current state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

const (
	// NetworkBindingReady indicates that the network binding has been associated
	// with a NetworkContext and the owning resource should expect functional
	// network features.
	NetworkBindingReady = "Ready"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NetworkBinding is the Schema for the networkbindings API
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
type NetworkBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec NetworkBindingSpec `json:"spec,omitempty"`

	// +kubebuilder:default={conditions:{{type:"Ready",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"}}}
	Status NetworkBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NetworkBindingList contains a list of NetworkBinding
type NetworkBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetworkBinding{}, &NetworkBindingList{})
}
