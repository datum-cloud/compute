package v1alpha

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
)
