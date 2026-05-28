// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:Enum=IPv4;IPv6
type IPFamily string

const (
	IPv4Protocol IPFamily = "IPv4"
	IPv6Protocol IPFamily = "IPv6"
)

// NetworkSpec defines the desired state of a Network
type NetworkSpec struct {

	// IPAM settings for the network.
	//
	// +kubebuilder:validation:Required
	IPAM NetworkIPAM `json:"ipam,omitempty"`

	// IP Families to permit on a network. Defaults to IPv4.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={IPv4}
	IPFamilies []IPFamily `json:"ipFamilies,omitempty"`

	// Network MTU. May be between 1300 and 8856.
	//
	// +kubebuilder:validation:Minimum=1300
	// +kubebuilder:validation:Maximum=8856
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1460
	MTU int32 `json:"mtu,omitempty"`
}

type NetworkIPAMMode string

const (
	// Automatically allocate subnets in the network
	NetworkIPAMModeAuto NetworkIPAMMode = "Auto"

	// Leverage allocation policies or manually created subnets
	NetworkIPAMModePolicy NetworkIPAMMode = "Policy"
)

type NetworkIPAM struct {
	// IPAM mode
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=Auto;Policy
	Mode NetworkIPAMMode `json:"mode"`

	// IPv4 range to use in auto mode networks. Defaults to 10.128.0.0/9.
	//
	// +kubebuilder:validation:Optional
	IPV4Range *string `json:"ipv4Range,omitempty"`

	// IPv6 range to use in auto mode networks. Defaults to a /48 allocated from `fd20::/20`.
	//
	// +kubebuilder:validation:Optional
	IPV6Range *string `json:"ipv6Range,omitempty"`
}

// NetworkStatus defines the observed state of Network
type NetworkStatus struct {
	// Represents the observations of a network's current state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Name",type="string",JSONPath=".metadata.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type==\"Ready\")].status`
// +kubebuilder:printcolumn:name="Reason",type="string",JSONPath=`.status.conditions[?(@.type==\"Ready\")].reason`
// +kubebuilder:printcolumn:name="IPAM",type="string",JSONPath=".spec.ipam.mode"
// +kubebuilder:printcolumn:name="IPFamilies",type="string",JSONPath=".spec.ipFamilies"
// +kubebuilder:printcolumn:name="MTU",type="integer",JSONPath=".spec.mtu"

// Network is the Schema for the networks API
type Network struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec   NetworkSpec   `json:"spec,omitempty"`
	Status NetworkStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NetworkList contains a list of Network
type NetworkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Network `json:"items"`
}

type NetworkRef struct {
	// The network namespace.
	//
	// Defaults to the namespace for the type the reference is embedded in.
	//
	// +kubebuilder:validation:Optional
	Namespace string `json:"namespace,omitempty"`

	// The network name
	//
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`
}

type LocalNetworkRef struct {
	// The network name
	//
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`
}

func init() {
	SchemeBuilder.Register(&Network{}, &NetworkList{})
}
