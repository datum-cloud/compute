// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SubnetClaimSpec defines the desired state of SubnetClaim
type SubnetClaimSpec struct {
	// The class of subnet required
	//
	// +kubebuilder:validation:Required
	SubnetClass string `json:"subnetClass"`

	// The network context to claim a subnet in
	//
	// +kubebuilder:validation:Required
	NetworkContext LocalNetworkContextRef `json:"networkContext"`

	// The location which a subnet claim is associated with
	//
	// +kubebuilder:validation:Required
	Location LocationReference `json:"location,omitempty"`

	// The IP family of a subnet claim
	//
	// +kubebuilder:validation:Required
	IPFamily IPFamily `json:"ipFamily"`

	// The start address of a subnet claim
	//
	// +kubebuilder:validation:Optional
	StartAddress *string `json:"startAddress,omitempty"`

	// The prefix length of a subnet claim
	//
	// +kubebuilder:validation:Optional
	PrefixLength *int32 `json:"prefixLength,omitempty"`
}

// SubnetClaimStatus defines the observed state of SubnetClaim
type SubnetClaimStatus struct {
	// The subnet which has been claimed from
	SubnetRef *LocalSubnetReference `json:"subnetRef,omitempty"`

	// The start address of a subnet claim
	StartAddress *string `json:"startAddress,omitempty"`

	// The prefix length of a subnet claim
	PrefixLength *int32 `json:"prefixLength,omitempty"`

	// Represents the observations of a subnet claim's current state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// SubnetClaim is the Schema for the subnetclaims API
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
type SubnetClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec SubnetClaimSpec `json:"spec,omitempty"`

	// +kubebuilder:default={conditions:{{type:"Allocated",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"},{type:"Programmed",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"},{type:"Ready",status:"Unknown",reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"}}}
	Status SubnetClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SubnetClaimList contains a list of SubnetClaim
type SubnetClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubnetClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SubnetClaim{}, &SubnetClaimList{})
}
