// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TODO(jreese) move this definition out of network-services-operator. It's here
// right now for convenience as both workload-operator and infra-provider-gcp
// will need to leverage the type.

// LocationSpec defines the desired state of Location.
type LocationSpec struct {
	// The location class that indicates control plane behavior of entities
	// associated with the location.
	//
	// Valid values are:
	//	- datum-managed
	//	- self-managed
	//
	// +kubebuilder:validation:Required
	LocationClassName string `json:"locationClassName,omitempty"`

	// The topology of the location
	//
	// This may contain arbitrary topology keys. Some keys may be well known, such
	// as:
	//	- topology.datum.net/city-code
	//
	// +kubebuilder:validation:Required
	Topology map[string]string `json:"topology"`

	// The location provider
	//
	// +kubebuilder:validation:Required
	Provider LocationProvider `json:"provider"`
}

type LocationProvider struct {
	GCP *GCPLocationProvider `json:"gcp,omitempty"`
}

type GCPLocationProvider struct {
	// The GCP project servicing the location
	//
	// For locations with the class of `datum-managed`, a service account will be
	// required for each unique GCP project ID across all locations registered in a
	// namespace.
	//
	// +kubebuilder:validation:Required
	ProjectID string `json:"projectId,omitempty"`

	// The GCP region servicing the location
	//
	// +kubebuilder:validation:Required
	Region string `json:"region,omitempty"`

	// The GCP zone servicing the location
	//
	// +kubebuilder:validation:Required
	Zone string `json:"zone,omitempty"`
}

// LocationStatus defines the observed state of Location.
type LocationStatus struct {
	// Represents the observations of a location's current state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Class",type="string",JSONPath=".spec.locationClassName"
// +kubebuilder:printcolumn:name="City",type="string",JSONPath=`.spec.topology.topology\.datum\.net/city-code`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type==\"Ready\")].status`
// +kubebuilder:printcolumn:name="Reason",type="string",JSONPath=`.status.conditions[?(@.type==\"Ready\")].reason`

// Location is the Schema for the locations API.
type Location struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LocationSpec   `json:"spec,omitempty"`
	Status LocationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LocationList contains a list of Location.
type LocationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Location `json:"items"`
}

type LocationReference struct {
	// Name of a datum location
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace for the datum location
	//
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`
}

func init() {
	SchemeBuilder.Register(&Location{}, &LocationList{})
}
