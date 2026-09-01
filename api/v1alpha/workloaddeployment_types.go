package v1alpha

import (
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkloadDeploymentSpec defines the desired state of WorkloadDeployment
type WorkloadDeploymentSpec struct {
	// The workload that a deployment belongs to
	//
	// +kubebuilder:validation:Required
	WorkloadRef WorkloadReference `json:"workloadRef"`

	// The placement in the workload which is driving a deployment
	//
	// +kubebuilder:validation:Required
	PlacementName string `json:"placementName"`

	// The location where this deployment runs.
	//
	// +kubebuilder:validation:Required
	LocationRef locationsv1alpha1.LocationReference `json:"locationRef"`

	// Defines settings for each instance.
	//
	// +kubebuilder:validation:Required
	Template InstanceTemplateSpec `json:"template,omitempty"`

	// Scale settings such as minimum and maximum replica counts.
	//
	// +kubebuilder:validation:Required
	ScaleSettings HorizontalScaleSettings `json:"scaleSettings"`

	// Replicas is the current desired replica target for this deployment. When
	// unset, the deployment reconciles to scaleSettings.minReplicas.
	//
	// +kubebuilder:validation:Optional
	Replicas *int32 `json:"replicas,omitempty"`
}

// WorkloadDeploymentStatus defines the observed state of WorkloadDeployment
type WorkloadDeploymentStatus struct {
	// Represents the observations of a deployment's current state.
	// Known condition types are: "Available", "Progressing"
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The number of instances created
	Replicas int32 `json:"replicas"`

	// The number of instances which have the latest workload settings applied
	// and are programmed (a subset of UpdatedReplicas that are ready to serve).
	CurrentReplicas int32 `json:"currentReplicas"`

	// The number of instances updated to the latest template revision, i.e.
	// whose observed template hash matches the desired template, regardless of
	// readiness. Lags Replicas during a rolling update or restart, then catches
	// back up — making an in-progress roll observable.
	UpdatedReplicas int32 `json:"updatedReplicas"`

	// The desired number of instances
	DesiredReplicas int32 `json:"desiredReplicas"`

	// The number of instances which are ready.
	ReadyReplicas int32 `json:"readyReplicas"`

	// Selector is the label selector that identifies Pods backing this deployment.
	//
	// +kubebuilder:validation:Optional
	Selector string `json:"selector,omitempty"`

	// The most recent generation observed by the deployment controller. When
	// this matches metadata.generation, the controller has reconciled the
	// latest spec (e.g. a restart request).
	//
	// +kubebuilder:validation:Optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Suspended, when true, requests that all instances managed by this deployment
	// be stopped without releasing their placement, disk attachments, or quota allocation.
	//
	// +kubebuilder:validation:Optional
	Suspended bool `json:"suspended,omitempty"`
}

const (
	// WorkloadDeploymentAvailable indicates that at least one instance has come
	// online.
	WorkloadDeploymentAvailable = "Available"

	// WorkloadDeploymentReplicasReady indicates whether all desired replicas are ready.
	WorkloadDeploymentReplicasReady = "ReplicasReady"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:metadata:annotations="discovery.miloapis.com/parent-contexts=Project"

// WorkloadDeployment is the Schema for the workloaddeployments API
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].reason`
// +kubebuilder:printcolumn:name="Replicas",type=string,JSONPath=`.status.replicas`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Desired",type=string,JSONPath=`.status.desiredReplicas`
// +kubebuilder:printcolumn:name="Up-to-date",type=string,JSONPath=`.status.updatedReplicas`
// +kubebuilder:printcolumn:name="Location",type=string,JSONPath=`.spec.locationRef.name`
type WorkloadDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkloadDeploymentSpec   `json:"spec,omitempty"`
	Status WorkloadDeploymentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkloadDeploymentList contains a list of WorkloadDeployment
type WorkloadDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkloadDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkloadDeployment{}, &WorkloadDeploymentList{})
}
