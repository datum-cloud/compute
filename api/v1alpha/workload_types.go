package v1alpha

import (
	k8scorev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// WorkloadSpec defines the desired state of Workload
type WorkloadSpec struct {
	// Defines settings for each instance.
	//
	// +kubebuilder:validation:Required
	Template InstanceTemplateSpec `json:"template,omitempty"`

	// Defines where instances should be deployed, and at what scope a deployment
	// will live in, such as in a city, or region.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Placements []WorkloadPlacement `json:"placements,omitempty"`

	// Workload specific gateway
	//
	// TODO(jreese) make plural?
	//
	// +kubebuilder:validation:Optional
	// Gateway *WorkloadGateway `json:"gateway,omitempty"`
}

type WorkloadGateway struct {
	// +kubebuilder:validation:Required
	Template WorkloadGatewayTemplate `json:"template"`

	// +kubebuilder:validation:Optional
	TCPRoutes []gatewayv1alpha2.TCPRouteSpec `json:"tcpRoutes,omitempty"`
}

type WorkloadGatewayTemplate struct {
	// Workload specific gateway
	//
	// +kubebuilder:validation:Optional
	Spec gatewayv1.GatewaySpec `json:"spec"`
}

// WorkloadStatus defines the observed state of Workload
type WorkloadStatus struct {
	// Represents the observations of a workload's current state.
	// Known condition types are: "Available", "Progressing"
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The number of deployments that currently exist
	Deployments int32 `json:"deployments"`

	// The number of instances that currently exist
	Replicas int32 `json:"replicas"`

	// The number of instances which have the latest workload settings applied.
	CurrentReplicas int32 `json:"currentReplicas"`

	// The desired number of instances
	DesiredReplicas int32 `json:"desiredReplicas"`

	// The number of instances which are ready.
	ReadyReplicas int32 `json:"readyReplicas"`

	// The current status of placemetns in a workload.
	Placements []WorkloadPlacementStatus `json:"placements,omitempty"`

	// The status of the workload gateway if configured.
	Gateway *WorkloadGatewayStatus `json:"gateway,omitempty"`
}

const (
	// WorkloadAvailable indicates that at least one instance has come online.
	WorkloadAvailable = "Available"
)

type WorkloadGatewayStatus struct {
	gatewayv1.GatewayStatus `json:",inline"`

	// TODO(jreese) route status? Doesn't seem to be much value for routes right
	// now, as the TCPRoute and even HTTPRoute status only inlines RouteStatus,
	// and that reports on status of the gateway it'd be attached to.
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="discovery.miloapis.com/parent-contexts=Project"

// Workload is the Schema for the workloads API
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].reason`
// +kubebuilder:printcolumn:name="Deployments",type=string,JSONPath=`.status.deployments`
// +kubebuilder:printcolumn:name="Replicas",type=string,JSONPath=`.status.replicas`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Desired",type=string,JSONPath=`.status.desiredReplicas`
// +kubebuilder:printcolumn:name="Up-to-date",type=string,JSONPath=`.status.currentReplicas`
type Workload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec   WorkloadSpec   `json:"spec,omitempty"`
	Status WorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkloadList contains a list of Workload
type WorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workload `json:"items"`
}

type WorkloadPlacement struct {
	// The name of the placement
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// A list of city codes that define where the instances should be deployed.
	//
	// +kubebuilder:validation:Required
	CityCodes []string `json:"cityCodes,omitempty"`

	// Scale settings such as minimum and maximum replica counts.
	//
	// +kubebuilder:validation:Required
	ScaleSettings HorizontalScaleSettings `json:"scaleSettings"`
}

type WorkloadPlacementStatus struct {
	// The name of the placement
	Name string `json:"name"`

	// Represents the observations of a placement's current state.
	// Known condition types are: "Available", "Progressing"
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The number of instances that currently exist
	Replicas int32 `json:"replicas"`

	// The number of instances which have the latest workload settings applied.
	CurrentReplicas int32 `json:"currentReplicas"`

	// The desired number of instances
	DesiredReplicas int32 `json:"desiredReplicas"`

	// The number of instances which are ready.
	ReadyReplicas int32 `json:"readyReplicas"`
}

type HorizontalScaleSettings struct {
	// The minimum number of replicas.
	//
	// +kubebuilder:validation:Required
	MinReplicas int32 `json:"minReplicas"`

	// The maximum number of replicas.
	//
	// +kubebuilder:validation:Optional
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`

	// A list of metrics that determine scaling behavior, such as external metrics.
	//
	// +kubebuilder:validation:Optional
	Metrics []MetricSpec `json:"metrics,omitempty"`

	// TODO(jreese) wire in behavior
	// See https://github.com/kubernetes/kubernetes/blob/dd87bc064631354885193fc1a97d0e7b603e77b4/staging/src/k8s.io/api/autoscaling/v2/types.go#L84
	// Defines the policy for managing instances.

	// TODO(jreese) Add instance update policy? RollingUpdate vs OrderedReady

	// Controls how instances are managed during scale up and down, as well as
	// during maintenance events.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:default=OrderedReady
	InstanceManagementPolicy InstanceManagementPolicyType `json:"instanceManagementPolicy,omitempty"`
}

type InstanceManagementPolicyType string

const (
	OrderedReadyInstanceManagementPolicyType InstanceManagementPolicyType = "OrderedReady"
	// ParallelInstanceManagementPolicyType     InstanceManagementPolicyType = "Parallel"
)

type MetricSpec struct {
	// Resource metrics known to Datum.
	//
	// +kubebuilder:validation:Optional
	Resource *ResourceMetricSource `json:"resource,omitempty"`
}

type ResourceMetricSource struct {
	// The name of the resource in question.
	//
	// +kubebuilder:validation:Required
	Name k8scorev1.ResourceName `json:"name"`

	// The target value for the given metric
	//
	// +kubebuilder:validation:Required
	Target MetricTarget `json:"target"`
}

// MetricTarget defines the target value, average value, or average utilization of a specific metric
type MetricTarget struct {
	// The target value of the metric (as a quantity).
	//
	// +kubebuilder:validation:Optional
	Value *resource.Quantity `json:"value,omitempty"`

	// The target value of the average of the metric across all relevant instances
	// (as a quantity)
	//
	// +kubebuilder:validation:Optional
	AverageValue *resource.Quantity `json:"averageValue,omitempty"`

	// The target value of the average of the
	// resource metric across all relevant instances, represented as a percentage of
	// the requested value of the resource for the instances.
	//
	// +kubebuilder:validation:Optional
	AverageUtilization *int32 `json:"averageUtilization,omitempty"`
}

type WorkloadReference struct {
	// The name of the workload
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// UID of the Workload
	//
	// +kubebuilder:validation:Required
	UID types.UID `json:"uid"`
}

func init() {
	SchemeBuilder.Register(&Workload{}, &WorkloadList{})
}
