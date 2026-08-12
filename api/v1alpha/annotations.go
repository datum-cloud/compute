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

	// FederationNamespaceAnnotation records, on a project WorkloadDeployment,
	// the federation-hub namespace its hub copy was written to. It is stamped at
	// federation time so that finalization — which must remove the hub copy
	// before the project object may go away — is self-contained: it reads only
	// the object being finalized, never a separate upstream object whose
	// lifetime it does not control. Objects federated before this annotation
	// existed fall back to resolving the namespace live.
	FederationNamespaceAnnotation = AnnotationNamespace + "/federation-namespace"

	// QuarantineReasonAnnotation is stamped on a federation-hub object by the
	// InstanceProjector when it reaches a state no retry can change. Its value is
	// the terminal reason (see internal/federation metric reasons). A quarantined
	// object is reported once and then skipped, so it can no longer pin the
	// projector's reconcile error ratio.
	QuarantineReasonAnnotation = AnnotationNamespace + "/quarantine-reason"

	// QuarantineMessageAnnotation carries the human-readable explanation that
	// accompanied the quarantine decision.
	QuarantineMessageAnnotation = AnnotationNamespace + "/quarantine-message"

	// QuarantineFingerprintAnnotation records a digest of the object state that
	// produced the quarantine. The projector re-evaluates from scratch whenever
	// the digest of the live object stops matching, so repairing the state an
	// operator can repair (a missing identity label) yields an immediate retry.
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
