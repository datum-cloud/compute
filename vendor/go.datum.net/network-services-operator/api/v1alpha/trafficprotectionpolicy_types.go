// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// TrafficProtectionPolicyMode defines the mode of traffic protection to apply.
//
// +kubebuilder:validation:Enum=Observe;Enforce;Disabled
type TrafficProtectionPolicyMode string

// TrafficProtectionPolicyMode constants.
const (
	// Observe will log violations but not block traffic.
	TrafficProtectionPolicyObserve TrafficProtectionPolicyMode = "Observe"

	// Enforce will block traffic that violates the policy.
	TrafficProtectionPolicyEnforce TrafficProtectionPolicyMode = "Enforce"

	// Disabled will turn off traffic protection.
	TrafficProtectionPolicyDisabled TrafficProtectionPolicyMode = "Disabled"
)

// TrafficProtectionPolicySpec defines the desired state of TrafficProtectionPolicy.
//
// +kubebuilder:validation:XValidation:rule="has(self.targetRefs) ? self.targetRefs.all(ref, ref.group == 'gateway.networking.k8s.io') : true ", message="this policy can only have a targetRefs[*].group of gateway.networking.k8s.io"
// +kubebuilder:validation:XValidation:rule="has(self.targetRefs) ? self.targetRefs.all(ref, ref.kind in ['Gateway', 'HTTPRoute']) : true ", message="this policy can only have a targetRefs[*].kind of Gateway/HTTPRoute"
type TrafficProtectionPolicySpec struct {

	// TargetRefs are the names of the Gateway resources this policy
	// is being attached to.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	TargetRefs []gatewayv1alpha2.LocalPolicyTargetReferenceWithSectionName `json:"targetRefs,omitempty"`

	// Mode specifies the mode of traffic protection to apply.
	// If not specified, defaults to "Observe".
	//
	// +kubebuilder:default=Observe
	Mode TrafficProtectionPolicyMode `json:"mode,omitempty"`

	// SamplingPercentage controls the percentage of traffic that will be processed
	// by the TrafficProtectionPolicy.
	//
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	SamplingPercentage int `json:"samplingPercentage,omitempty"`

	// RuleSets specifies the TrafficProtectionPolicy rulesets to apply.
	//
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:default={{"type": "OWASPCoreRuleSet", "owaspCoreRuleSet": {}}}
	// +kubebuilder:validation:XValidation:message="OWASPCoreRuleSet filter cannot be repeated",rule="self.filter(f, f.type == 'OWASPCoreRuleSet').size() <= 1"
	RuleSets []TrafficProtectionPolicyRuleSet `json:"ruleSets,omitempty"`
}

// TrafficProtectionPolicyRuleSetType identifies a type of TrafficProtectionPolicy ruleset.
type TrafficProtectionPolicyRuleSetType string

const (
	TrafficProtectionPolicyOWASPCoreRuleSet TrafficProtectionPolicyRuleSetType = "OWASPCoreRuleSet"
)

type TrafficProtectionPolicyRuleSet struct {
	// Type specifies the type of TrafficProtectionPolicy ruleset.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=OWASPCoreRuleSet
	Type TrafficProtectionPolicyRuleSetType `json:"type"`

	// OWASPCoreRuleSet defines configuration options for the OWASP ModSecurity
	// Core Rule Set (CRS).
	//
	// +kubebuilder:validation:Optional
	OWASPCoreRuleSet OWASPCRS `json:"owaspCoreRuleSet"`
}

// OWASPCRS defines configuration options for the OWASP ModSecurity Core Rule Set (CRS).
type OWASPCRS struct {

	// ParanoiaLevels specifies the OWASP ModSecurity Core Rule Set (CRS)
	// paranoia levels to use.
	//
	// +kubebuilder:default={}
	ParanoiaLevels ParanoiaLevels `json:"paranoiaLevels,omitempty"`

	// ScoreThresholds specifies the OWASP ModSecurity Core Rule Set (CRS)
	// score thresholds to block a request or response.
	//
	// See: https://coreruleset.org/docs/2-how-crs-works/2-1-anomaly_scoring/
	//
	// +kubebuilder:default={}
	ScoreThresholds OWASPScoreThresholds `json:"scoreThresholds,omitempty"`

	// RuleExclusions can be used to disable specific OWASP ModSecurity Rules.
	// This allows operators to disable specific rules that may be causing false
	// positives.
	//
	// +kubebuilder:validation:Optional
	RuleExclusions *OWASPRuleExclusions `json:"ruleExclusions,omitempty"`
}

type ParanoiaLevels struct {
	// Blocking specifies the paranoia level for blocking requests or responses.
	//
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4
	Blocking int `json:"blocking,omitempty"`

	// Detection specifies the paranoia level for detection only. This allows
	// setting a higher paranoia level for detection while keeping blocking at a
	// lower level.
	//
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4
	Detection int `json:"detection,omitempty"`
}

type OWASPRuleExclusions struct {
	// Tags is a list of rule tags to disable.
	//
	// +kubebuilder:validation:MaxItems=100
	Tags []OWASPTag `json:"tags,omitempty"`

	// IDs is a list of specific rule IDs to disable
	//
	// +kubebuilder:validation:MaxItems=100
	IDs []int `json:"ids,omitempty"`

	// IDRanges is a list of specific rule ID ranges to disable.
	//
	// +kubebuilder:validation:MaxItems=100
	IDRanges []OWASPIDRange `json:"idRanges,omitempty"`
}

// OWASPIDRange is a range of OWASP ModSecurity Rule IDs.
//
// +kubebuilder:validation:MaxLength=21
// +kubebuilder:validation:Pattern=`^\d{1,10}-\d{1,10}$`
// +kubebuilder:validation:XValidation:message="Max must be greater than min",rule="int(self.split('-')[1]) > int(self.split('-')[0])"
type OWASPIDRange string

// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_\-/]+$`
type OWASPTag string

type OWASPScoreThresholds struct {
	// Inbound is the score threshold for blocking inbound (request) traffic.
	//
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10000
	Inbound int `json:"inbound,omitempty"`

	// Outbound is the score threshold for blocking outbound (response) traffic.
	//
	// +kubebuilder:default=4
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10000
	Outbound int `json:"outbound,omitempty"`
}

// TrafficProtectionPolicyStatus defines the observed state of TrafficProtectionPolicy.
type TrafficProtectionPolicyStatus struct {
	gatewayv1alpha2.PolicyStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tpp

// TrafficProtectionPolicy is the Schema for the trafficprotectionpolicies API.
type TrafficProtectionPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec   TrafficProtectionPolicySpec   `json:"spec,omitempty"`
	Status TrafficProtectionPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TrafficProtectionPolicyList contains a list of TrafficProtectionPolicy.
type TrafficProtectionPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TrafficProtectionPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TrafficProtectionPolicy{}, &TrafficProtectionPolicyList{})
}
