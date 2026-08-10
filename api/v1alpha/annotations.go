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

	// ReferencedDataErrorAnnotation is stamped on a WorkloadDeployment by the
	// ReferencedDataController when a terminal source error occurs (SourceNotFound,
	// SourceUnauthorized, or SourceTooLarge). Its value is a JSON object with
	// "reason" and "message" fields carrying the authoritative resolver verdict.
	//
	// Example value:
	//   {"reason":"SourceNotFound","message":"ConfigMap \"app-config\" not found in namespace \"default\""}
	//
	// This annotation bridges the federation boundary: Karmada propagates
	// metadata.annotations hub→cell alongside WorkloadDeployment objects, but
	// status.conditions do not propagate in that direction. The cell
	// InstanceReconciler reads this annotation from the cell WD copy (returned by
	// fetchOwnerWorkloadDeployment) and promotes it to the Instance's
	// ReferencedDataReady condition so the terminal error is visible at the Instance
	// level without requiring a cross-plane condition read.
	//
	// The annotation is removed when the error resolves (companion materialises /
	// ReferencedDataReady flips True), so the absence of the annotation means
	// either no error or the error has cleared.
	ReferencedDataErrorAnnotation = AnnotationNamespace + "/referenced-data-error"

	// SuspendedAnnotation is set on a project-namespace WorkloadDeployment by the
	// ComputeSuspend/ComputeResume consumer hooks to request that the cell stop
	// (or resume) the instances it manages, in response to a project suspension
	// signal. Its value is the string "true" or "false".
	//
	// This is an annotation rather than a direct Status.Suspended write for the
	// same federation-boundary reason as ReferencedDataErrorAnnotation, but in
	// the opposite direction: Karmada propagates metadata.annotations hub→cell,
	// but the hub's WorkloadDeploymentFederator does not propagate Status
	// hub→cell either — it only ever pulls the cell's aggregated status back up
	// (syncStatusFromDownstream). A hub-side Status.Suspended write is therefore
	// invisible to the cell and gets silently overwritten by the next status
	// sync from downstream. The cell's own WorkloadDeploymentReconciler reads
	// this annotation on its local copy and sets Status.Suspended itself, which
	// then aggregates back up to the hub normally.
	SuspendedAnnotation = AnnotationNamespace + "/suspended"
)
