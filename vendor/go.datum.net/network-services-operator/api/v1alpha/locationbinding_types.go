// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LocationBindingSpec defines the desired state of LocationBinding.
type LocationBindingSpec struct {
	// LocationRef references the canonical cluster-scoped Location object.
	LocationRef corev1.LocalObjectReference `json:"locationRef"`

	// LocationClassName mirrors spec.locationClassName from the referenced Location.
	LocationClassName string `json:"locationClassName,omitempty"`

	// DisplayName is a human-readable label for the location.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Topology mirrors spec.topology from the referenced Location, containing
	// well-known keys like topology.datum.net/city-code and topology.datum.net/region.
	// +optional
	Topology map[string]string `json:"topology,omitempty"`
}

// LocationBindingStatus defines the observed state of LocationBinding.
type LocationBindingStatus struct {
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Location",type="string",JSONPath=".spec.locationRef.name"
// +kubebuilder:printcolumn:name="Class",type="string",JSONPath=".spec.locationClassName"
// +kubebuilder:printcolumn:name="Available",type="string",JSONPath=`.status.conditions[?(@.type=="Available")].status`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// LocationBinding is the Schema for the locationbindings API. It is a
// cluster-scoped projection of a cluster-scoped Location into a project's
// virtual control plane, created once the location's class is supported, the
// Location is Ready, and the corresponding ServiceAvailability is Available.
type LocationBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LocationBindingSpec   `json:"spec,omitempty"`
	Status LocationBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LocationBindingList contains a list of LocationBinding.
type LocationBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LocationBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LocationBinding{}, &LocationBindingList{})
}
