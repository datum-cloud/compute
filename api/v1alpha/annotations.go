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

	// FederationNamespaceAnnotation records the federation-hub namespace that a
	// project WorkloadDeployment's hub copy was written to. The federator stamps
	// it when it federates the deployment.
	//
	// Finalization must remove the hub copy before the project object can go
	// away. Reading the namespace from the object being finalized keeps that step
	// from depending on a separate object with its own lifetime. Deployments
	// federated before this annotation existed resolve the namespace live
	// instead.
	FederationNamespaceAnnotation = AnnotationNamespace + "/federation-namespace"

	// QuarantineReasonAnnotation marks a federation-hub object that the
	// InstanceProjector cannot project, in a state no retry can change. Its value
	// is one of the terminal reasons in internal/federation. The projector
	// reports a quarantined object once and then skips it, so the object no
	// longer counts against the projector's reconcile error ratio.
	QuarantineReasonAnnotation = AnnotationNamespace + "/quarantine-reason"

	// QuarantineMessageAnnotation explains in plain text why the object was
	// quarantined.
	QuarantineMessageAnnotation = AnnotationNamespace + "/quarantine-message"

	// QuarantineFingerprintAnnotation records a digest of the object state the
	// quarantine was based on. The projector evaluates the object again whenever
	// the digest stops matching, so repairing that state, such as restoring a
	// missing identity label, retries the object immediately.
	QuarantineFingerprintAnnotation = AnnotationNamespace + "/quarantine-fingerprint"

	// QuarantinedAtAnnotation records when the object was quarantined, as an
	// RFC3339 timestamp.
	QuarantinedAtAnnotation = AnnotationNamespace + "/quarantined-at"

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
)
