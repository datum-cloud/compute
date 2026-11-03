package v1alpha

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	LabelNamespace = "compute.datumapis.com"

	WorkloadUIDLabel           = LabelNamespace + "/workload-uid"
	WorkloadDeploymentUIDLabel = LabelNamespace + "/workload-deployment-uid"

	InstanceIndexLabel = LabelNamespace + "/instance-index"
	// InstanceSourceUIDLabel pins the edge Instance lifetime on its projections.
	InstanceSourceUIDLabel = LabelNamespace + "/source-instance-uid"

	// WorkloadDeploymentNameLabel carries the name of the WorkloadDeployment
	// that owns an Instance. Stamped at creation and kept current on updates.
	WorkloadDeploymentNameLabel = LabelNamespace + "/workload-deployment-name"

	// LocationLabel carries the canonical location name of the
	// WorkloadDeployment that owns an Instance.
	LocationLabel = LabelNamespace + "/location"

	// WorkloadNameLabel carries the name of the Workload that an Instance
	// ultimately belongs to, sourced from WorkloadDeploymentSpec.WorkloadRef.Name.
	WorkloadNameLabel = LabelNamespace + "/workload-name"

	// PlacementNameLabel carries the placement name from the Workload that drove
	// this Instance's deployment, sourced from WorkloadDeploymentSpec.PlacementName.
	PlacementNameLabel = LabelNamespace + "/placement-name"

	// ReferencedDataLabel is stamped on companion ConfigMaps and Secrets
	// materialized by the ReferencedDataController, and on WorkloadDeployments
	// that reference external ConfigMaps or Secrets.
	//
	// It identifies companions to the controllers that read them: the cell-side
	// scheduling gate lists labeled objects to decide whether the data it
	// expects has arrived, and companion cleanup scopes its informer cache with
	// it. Propagation does not use the label — a companion reaches cells as a
	// deployment's declared dependency, named by the
	// expected-referenced-data annotation rather than matched by a selector.
	ReferencedDataLabel = LabelNamespace + "/referenced-data"

	// ReferencedDataLabelValue is the value used for ReferencedDataLabel.
	ReferencedDataLabelValue = "true"

	// InstanceConsoleSessionUIDLabel carries the project session's UID on the
	// hub and cell copies of an InstanceConsoleSession. Clients sign with it.
	InstanceConsoleSessionUIDLabel = LabelNamespace + "/session-uid"

	// InstanceConsoleSessionInstanceNameLabel carries the name of the Instance
	// a copied InstanceConsoleSession targets. The hub and cell Instance share
	// the copy's namespace and this name.
	InstanceConsoleSessionInstanceNameLabel = LabelNamespace + "/instance-name"

	// InstanceTypeCatalogLabel marks an InstanceType on the federation hub as a
	// copy of the platform catalog written by the InstanceTypeProjector. The
	// instance-types ClusterPropagationPolicy selects on it to carry the catalog
	// to every cell, and the projector prunes only labelled copies, so an
	// InstanceType it did not write is neither propagated nor removed.
	InstanceTypeCatalogLabel = LabelNamespace + "/instance-type-catalog"

	// InstanceTypeCatalogLabelValue is the value used for InstanceTypeCatalogLabel.
	InstanceTypeCatalogLabelValue = "true"
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

// NetworkHostLabel names the Node whose kernel carries an instance Pod's
// network. A virtual machine runtime may bind the Pod to a Node other than that
// host: a virtual kubelet registers one Node per host and runs every guest Pod
// bound to it on the host's network. Network consumers select an instance's
// Pods by this label rather than inferring the host from how one runtime names
// or addresses its Nodes.
//
// The provider that realizes the Pod owns the label. It sets the label when it
// builds the Pod if it already knows the host, and otherwise as soon as the Pod
// is bound, before anything else reconciles the Pod's network. The host is the
// Pod's spec.nodeName only when that Node's own kubelet runs the Pod. A
// provider whose Pods bind to a virtual kubelet Node maps that Node to its host
// first. The value is
// NetworkHostLabelValue of the host Node's name. A Pod never changes Nodes, so
// an instance that moves gets a new Pod carrying its new host. A Pod without
// the label has no known host yet.
const NetworkHostLabel = LabelNamespace + "/network-host"

// networkHostHashLength is the number of hex characters of the host name's
// SHA-256 digest that a NetworkHostLabel value keeps when the name is too long
// for a label value. Sixteen characters keep collisions between Node names in
// one cluster out of practical reach.
const networkHostHashLength = 16

// NetworkHostLabelValue returns the NetworkHostLabel value for a host Node.
// A Node name that is a valid label value is used as is. A longer name becomes
// its leading characters joined to a hash of the whole name, and any other name
// becomes the hash alone, so the result is always a valid label value. The
// result is stable, so a consumer can compute the value for its own Node and
// compare or select on it.
func NetworkHostLabelValue(nodeName string) string {
	if len(validation.IsValidLabelValue(nodeName)) == 0 {
		return nodeName
	}
	sum := sha256.Sum256([]byte(nodeName))
	digest := hex.EncodeToString(sum[:])[:networkHostHashLength]
	prefix := nodeName[:min(len(nodeName), validation.LabelValueMaxLength-networkHostHashLength-1)]
	prefix = strings.TrimRight(prefix, "-_.")
	if value := prefix + "-" + digest; prefix != "" && len(validation.IsValidLabelValue(value)) == 0 {
		return value
	}
	return digest
}

// NetworkHostSelector returns the selector for the instance Pods whose network
// the given host Node carries. An empty name selects nothing, so a consumer
// that cannot name its own Node claims no Pods rather than every unplaced one.
func NetworkHostSelector(nodeName string) labels.Selector {
	if nodeName == "" {
		return labels.Nothing()
	}
	return labels.SelectorFromSet(labels.Set{
		NetworkHostLabel: NetworkHostLabelValue(nodeName),
	})
}
