// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SubnetSpec defines the desired state of Subnet
type SubnetSpec struct {
	// The class of subnet
	//
	// +kubebuilder:validation:Required
	SubnetClass string `json:"subnetClass"`

	// A subnet's network context
	//
	// +kubebuilder:validation:Required
	NetworkContext LocalNetworkContextRef `json:"networkContext"`

	// The location which a subnet is associated with
	//
	// +kubebuilder:validation:Required
	Location LocationReference `json:"location,omitempty"`

	// The IP family of a subnet
	//
	// +kubebuilder:validation:Required
	IPFamily IPFamily `json:"ipFamily"`

	// The start address of a subnet
	//
	// +kubebuilder:validation:Required
	StartAddress string `json:"startAddress"`

	// The prefix length of a subnet
	//
	// +kubebuilder:validation:Required
	PrefixLength int32 `json:"prefixLength"`
}

// SubnetStatus defines the observed state of a Subnet
type SubnetStatus struct {
	// The start address of a subnet
	StartAddress *string `json:"startAddress,omitempty"`

	// The prefix length of a subnet
	PrefixLength *int32 `json:"prefixLength,omitempty"`

	// Represents the observations of a subnet's current state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

const (
	// SubnetAllocated indicates that the subnet has been allocated a prefix
	SubnetAllocated = "Allocated"

	// SubnetProgrammed indicates that the subnet has been programmed
	SubnetProgrammed = "Programmed"

	// SubnetReady indicates that the subnet is ready to use
	SubnetReady = "Ready"
)

const (
	// SubnetProgrammedReasonNotProgrammed indicates that the subnet has not been programmed
	SubnetProgrammedReasonNotProgrammed = "NotProgrammed"

	// SubnetProgrammedReasonProgrammingInProgress indicates that the subnet is being programmed.
	SubnetProgrammedReasonProgrammingInProgress = "ProgrammingInProgress"

	// SubnetProgrammedReasonProgrammed indicates that the subnet has been programmed
	SubnetProgrammedReasonProgrammed = "Programmed"

	// SubnetReadyReasonReady indicates that the subnet is ready to use
	SubnetReadyReasonReady = "Ready"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Subnet is the Schema for the subnets API
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Start Address",type=string,JSONPath=`.status.startAddress`
// +kubebuilder:printcolumn:name="Prefix Length",type=string,JSONPath=`.status.prefixLength`
type Subnet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec SubnetSpec `json:"spec,omitempty"`

	// +kubebuilder:default={conditions:{{type:"Allocated",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"},{type:"Programmed",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"},{type:"Ready",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"}}}
	Status SubnetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SubnetList contains a list of Subnet
type SubnetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Subnet `json:"items"`
}

type LocalSubnetReference struct {
	Name string `json:"name"`
}

func init() {
	SchemeBuilder.Register(&Subnet{}, &SubnetList{})
}
