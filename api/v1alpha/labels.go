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

// Runtime class labels. Placement, provider dispatch, and per-tier
// observability all key off these, so they are part of the contract other
// repositories (cell providers, cluster registration) implement against.
const (
	// RuntimeClassLabel carries the runtime class an object is placed and run
	// in: on the hub copy of a WorkloadDeployment it is what a class-aware
	// PropagationPolicy selects, and on an Instance it is what a provider
	// filters by to decide whether the instance is its to realize.
	RuntimeClassLabel = LabelNamespace + "/runtime-class"

	// RuntimeClassServedLabelPrefix builds the label a cell's Cluster object
	// carries for every runtime class it can serve, e.g.
	// compute.datumapis.com/runtime-class.<class-name>=true. One key per class,
	// rather than a single valued label, is what lets a cell advertise more
	// than one class while placement still selects with equality matching.
	RuntimeClassServedLabelPrefix = LabelNamespace + "/runtime-class."

	// RuntimeClassServedLabelValue is the value a cell sets on a served-class
	// label. Only the key's presence carries meaning; the value is fixed so
	// equality selectors can be written without knowing how a cell was
	// registered.
	RuntimeClassServedLabelValue = "true"
)

// RuntimeClassServedLabel returns the label key a cell carries to advertise
// that it can serve the given runtime class.
func RuntimeClassServedLabel(class string) string {
	return RuntimeClassServedLabelPrefix + class
}

// InstanceRuntimeClassSelector returns the selector a provider uses to claim
// only the Instances in the runtime class it serves. Two providers running in
// the same cell must partition the Instances between them by class: filtering
// on workload shape instead leaves an instance owned by both providers or by
// neither, and neither failure is visible in status.
//
// The class is the provider's own to name, from its configuration and the
// classes the catalog says it controls. It is deliberately not resolvable from
// the platform API: a class name the platform compiled in would be a tier the
// catalog could not retire.
//
// An Instance carries this label only once a class has been resolved for it, so
// a cell where runtime class selection has never been enabled holds Instances
// that no class selector matches. A provider deployed into such a cell must
// claim its Instances the way it did before classes existed until the cell's
// control plane is publishing them.
//
// A provider MUST apply this selector to its informer CACHE —
// cache.Options.ByObject{&Instance{}: {Label: selector}} — and not only as a
// controller-runtime event predicate. A predicate filters events after the
// cache has already stored every Instance in the cell; that has OOM
// crash-looped a provider in this system before, at which point delete
// reconciles stop running and instances wedge in Terminating.
func InstanceRuntimeClassSelector(class string) labels.Selector {
	return labels.SelectorFromSet(labels.Set{
		RuntimeClassLabel: class,
	})
}
