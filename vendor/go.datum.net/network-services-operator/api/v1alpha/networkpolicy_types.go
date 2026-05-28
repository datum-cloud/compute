// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// NetworkPolicySpec defines the desired state of NetworkPolicy
type NetworkPolicySpec struct {
	// TODO(jreese) complete this - currently the ingress rule is used directly
	// in an instance's interface policy.
}

// See k8s network policy types for inspiration here
type NetworkPolicyIngressRule struct {
	// ports is a list of ports which should be made accessible on the instances selected for
	// this rule. Each item in this list is combined using a logical OR. If this field is
	// empty or missing, this rule matches all ports (traffic not restricted by port).
	// If this field is present and contains at least one item, then this rule allows
	// traffic only if the traffic matches at least one port in the list.
	//
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Ports []NetworkPolicyPort `json:"ports,omitempty"`

	// from is a list of sources which should be able to access the instances selected for this rule.
	// Items in this list are combined using a logical OR operation. If this field is
	// empty or missing, this rule matches all sources (traffic not restricted by
	// source). If this field is present and contains at least one item, this rule
	// allows traffic only if the traffic matches at least one item in the from list.
	//
	// +kubebuilder:validation:Optional
	// +listType=atomic
	From []NetworkPolicyPeer `json:"from,omitempty"`
}

// NetworkPolicyPeer describes a peer to allow traffic to/from. Only certain combinations of
// fields are allowed
type NetworkPolicyPeer struct {
	// ipBlock defines policy on a particular IPBlock. If this field is set then
	// neither of the other fields can be.
	//
	// +kubebuilder:validation:Optional
	IPBlock *IPBlock `json:"ipBlock,omitempty"`
}

// NetworkPolicyPort describes a port to allow traffic on
type NetworkPolicyPort struct {
	// protocol represents the protocol (TCP, UDP, or SCTP) which traffic must match.
	// If not specified, this field defaults to TCP.
	//
	// +kubebuilder:validation:Optional
	Protocol *corev1.Protocol `json:"protocol,omitempty"`

	// port represents the port on the given protocol. This can either be a numerical or named
	// port on an instance. If this field is not provided, this matches all port names and
	// numbers.
	// If present, only traffic on the specified protocol AND port will be matched.
	//
	// +kubebuilder:validation:Optional
	Port *intstr.IntOrString `json:"port,omitempty"`

	// endPort indicates that the range of ports from port to endPort if set, inclusive,
	// should be allowed by the policy. This field cannot be defined if the port field
	// is not defined or if the port field is defined as a named (string) port.
	// The endPort must be equal or greater than port.
	//
	// +kubebuilder:validation:Optional
	EndPort *int32 `json:"endPort,omitempty"`
}

// IPBlock describes a particular CIDR (Ex. "192.168.1.0/24","2001:db8::/64")
// that is allowed to the targets matched by a network policy. The except entry
// describes CIDRs that should not be included within this rule.
type IPBlock struct {
	// cidr is a string representing the IPBlock
	// Valid examples are "192.168.1.0/24" or "2001:db8::/64"
	//
	// +kubebuilder:validation:Required
	CIDR string `json:"cidr"`

	// except is a slice of CIDRs that should not be included within an IPBlock
	// Valid examples are "192.168.1.0/24" or "2001:db8::/64"
	// Except values will be rejected if they are outside the cidr range
	//
	// +listType=atomic
	// +kubebuilder:validation:Optional
	Except []string `json:"except,omitempty"`
}

// NetworkPolicyStatus defines the observed state of NetworkPolicy
type NetworkPolicyStatus struct {
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NetworkPolicy is the Schema for the networkpolicies API
type NetworkPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetworkPolicySpec   `json:"spec,omitempty"`
	Status NetworkPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NetworkPolicyList contains a list of NetworkPolicy
type NetworkPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetworkPolicy{}, &NetworkPolicyList{})
}
