package v1alpha

const (
	AnnotationNamespace = "compute.datumapis.com"

	SSHKeysAnnotation = AnnotationNamespace + "/ssh-keys"

	// RestartedAtAnnotation may be set on an InstanceTemplateSpec's annotations
	// (an RFC3339 timestamp) to request a rolling restart. It is included in the
	// template hash and triggers the controller's ordered instance roll.
	RestartedAtAnnotation = AnnotationNamespace + "/restartedAt"
)
