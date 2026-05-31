package v1alpha

const (
	AnnotationNamespace = "compute.datumapis.com"

	SSHKeysAnnotation = AnnotationNamespace + "/ssh-keys"

	// ExpectedReferencedDataAnnotation is set on a WorkloadDeployment by the
	// ReferencedDataController. Its value is a JSON-encoded array of companion
	// object names (sorted deterministically) that the cell should expect.
	// The cell does a pure set-membership check against labeled companions
	// without recomputing names.
	//
	// Example value: ["configmap.app-config","secret.db-creds"]
	ExpectedReferencedDataAnnotation = AnnotationNamespace + "/expected-referenced-data"

	// RestartedAtAnnotation may be set on an InstanceTemplateSpec's annotations
	// to trigger a rolling restart. The value is an RFC3339 timestamp. Because
	// this annotation lives in the template metadata, it is included in the
	// template hash and triggers the existing ordered in-place roll.
	RestartedAtAnnotation = AnnotationNamespace + "/restartedAt"

	// ReferencedDataGateStartAnnotation is stamped on an Instance by the cell
	// InstanceReconciler the first time it observes the ReferencedData scheduling
	// gate. Its value is an RFC3339 timestamp. Used to compute gate-wait duration
	// for the compute_referenced_data_gate_wait_seconds histogram.
	ReferencedDataGateStartAnnotation = AnnotationNamespace + "/referenced-data-gate-start"
)
