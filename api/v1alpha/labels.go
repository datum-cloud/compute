package v1alpha

const (
	LabelNamespace = "compute.datumapis.com"

	WorkloadUIDLabel           = LabelNamespace + "/workload-uid"
	WorkloadDeploymentUIDLabel = LabelNamespace + "/workload-deployment-uid"
	// WorkloadDeploymentNameLabel carries the WorkloadDeployment name on each
	// Instance. Unlike WorkloadDeploymentUIDLabel — which carries the
	// edge/Karmada UID and therefore differs across federation planes —
	// WorkloadDeploymentNameLabel is identical in the project cluster, Karmada,
	// and on the edge, making it safe for cross-plane owner-ref resolution and
	// CLI lookup.
	WorkloadDeploymentNameLabel = LabelNamespace + "/workload-deployment-name"

	InstanceIndexLabel = LabelNamespace + "/instance-index"

	// CityCodeLabel carries the city code (e.g. "DFW") that the Instance is
	// scheduled to. Stamped at creation time and immutable.
	CityCodeLabel = LabelNamespace + "/city-code"

	// WorkloadNameLabel carries the name of the Workload that owns this
	// Instance. Stamped at creation time and immutable.
	WorkloadNameLabel = LabelNamespace + "/workload-name"

	// PlacementNameLabel carries the name of the placement entry within the
	// Workload spec that produced this Instance. Stamped at creation time and
	// immutable.
	PlacementNameLabel = LabelNamespace + "/placement-name"
)
