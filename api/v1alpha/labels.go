package v1alpha

import "k8s.io/apimachinery/pkg/labels"

const (
	LabelNamespace = "compute.datumapis.com"

	WorkloadUIDLabel           = LabelNamespace + "/workload-uid"
	WorkloadDeploymentUIDLabel = LabelNamespace + "/workload-deployment-uid"

	InstanceIndexLabel = LabelNamespace + "/instance-index"

	// WorkloadDeploymentNameLabel carries the name of the WorkloadDeployment
	// that owns an Instance. Stamped at creation and kept current on updates.
	WorkloadDeploymentNameLabel = LabelNamespace + "/workload-deployment-name"

	// CityCodeLabel carries the city code of the WorkloadDeployment that owns
	// an Instance, matching WorkloadDeploymentSpec.CityCode.
	CityCodeLabel = LabelNamespace + "/city-code"

	// WorkloadNameLabel carries the name of the Workload that an Instance
	// ultimately belongs to, sourced from WorkloadDeploymentSpec.WorkloadRef.Name.
	WorkloadNameLabel = LabelNamespace + "/workload-name"

	// PlacementNameLabel carries the placement name from the Workload that drove
	// this Instance's deployment, sourced from WorkloadDeploymentSpec.PlacementName.
	PlacementNameLabel = LabelNamespace + "/placement-name"

	// ReferencedDataLabel is stamped on companion ConfigMaps and Secrets
	// materialized by the ReferencedDataController, and on WorkloadDeployments
	// that reference external ConfigMaps or Secrets. Used as a label selector
	// by the Karmada PropagationPolicy to propagate companions to cells.
	ReferencedDataLabel = LabelNamespace + "/referenced-data"

	// ReferencedDataLabelValue is the value used for ReferencedDataLabel.
	ReferencedDataLabelValue = "true"
)

// Runtime class labels. Placement, provider dispatch, and per-tier metrics all
// key off these labels, so other repositories such as cell providers and
// cluster registration implement against them.
const (
	// RuntimeClassLabel carries the runtime class an object is placed and run
	// in. A class-aware PropagationPolicy selects on the label on the hub copy
	// of a WorkloadDeployment. A provider filters on the label on an Instance
	// to decide whether it realizes that instance.
	RuntimeClassLabel = LabelNamespace + "/runtime-class"

	// RuntimeClassServedLabelPrefix builds the label key a cell's Cluster
	// object carries for every runtime class it can serve, for example
	// compute.datumapis.com/runtime-class.<class-name>=true. One key per class
	// lets a cell advertise more than one class while placement still selects
	// with equality matching.
	RuntimeClassServedLabelPrefix = LabelNamespace + "/runtime-class."

	// RuntimeClassServedLabelValue is the value a cell sets on a served-class
	// label. Only the key's presence carries meaning. The value is fixed so
	// equality selectors work without knowing how a cell was registered.
	RuntimeClassServedLabelValue = "true"
)

// RuntimeClassServedLabel returns the label key a cell carries to advertise
// that it can serve the given runtime class.
func RuntimeClassServedLabel(class string) string {
	return RuntimeClassServedLabelPrefix + class
}

// InstanceRuntimeClassSelector returns the selector a provider uses to claim
// only the Instances in the runtime class it serves. Two providers running in
// the same cell must partition Instances between them by class. Filtering on
// workload shape instead leaves an Instance claimed by both providers or by
// neither, and status reports neither outcome.
//
// The provider names its own class, from its configuration and the classes the
// catalog says it controls. The name is deliberately not resolvable from the
// platform API, because a class name compiled into the platform would be a tier
// the catalog could not retire.
//
// An Instance carries the class label only after a class is resolved for it. In
// a cell whose control plane does not publish the label, no class selector
// matches any Instance, and a provider there must fall back to claiming
// Instances without the selector until the cell publishes the label.
//
// A provider MUST apply the selector to its informer cache through
// cache.Options.ByObject, not only as a controller-runtime event predicate. A
// predicate filters events after the cache has already stored every Instance in
// the cell. That has OOM crash-looped a provider in this system, and a crashed
// provider stops running delete reconciles, which leaves instances stuck in
// Terminating.
func InstanceRuntimeClassSelector(class string) labels.Selector {
	return labels.SelectorFromSet(labels.Set{
		RuntimeClassLabel: class,
	})
}
