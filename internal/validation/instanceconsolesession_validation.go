// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	"go.datum.net/compute/pkg/runtimeclass"
)

// InstanceConsoleSessionValidationOptions carries what admission read from the
// project the session is created in.
type InstanceConsoleSessionValidationOptions struct {
	// Instance is the project instance named by spec.instanceRef.name, or nil
	// when the project has no instance by that name.
	Instance *computev1alpha.Instance

	// RuntimeClasses is the catalog published to the project.
	RuntimeClasses runtimeclass.Catalog
}

// ValidateInstanceConsoleSessionCreate refuses a session that is already known
// to fail, so a client learns why at once instead of from a terminal status.
func ValidateInstanceConsoleSessionCreate(
	session *computev1alpha.InstanceConsoleSession,
	opts InstanceConsoleSessionValidationOptions,
) field.ErrorList {
	specPath := field.NewPath("spec")
	if !features.FeatureGate.Enabled(features.InstanceConsoleSessions) {
		return field.ErrorList{field.Forbidden(specPath, "shell sessions are not available in this environment")}
	}

	if errs := validateSessionMetadata(session); len(errs) > 0 {
		return errs
	}

	refPath := specPath.Child("instanceRef")
	ref := session.Spec.InstanceRef
	instance := opts.Instance
	if instance == nil {
		notFound := field.NotFound(refPath.Child("name"), ref.Name)
		notFound.Detail = "the project has no instance with that name"
		return field.ErrorList{notFound}
	}
	if instance.UID != ref.UID {
		return field.ErrorList{field.Invalid(refPath.Child("uid"), ref.UID, fmt.Sprintf(
			"instance %q has been replaced; read the instance again and use its current uid", ref.Name))}
	}
	if !instance.DeletionTimestamp.IsZero() {
		return field.ErrorList{field.Invalid(refPath.Child("name"), ref.Name, "the instance is being deleted")}
	}

	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateSessionContainer(session.Spec.ContainerName, instance, specPath.Child("containerName"))...)
	allErrs = append(allErrs, validateSessionRuntimeClass(instance, opts.RuntimeClasses, refPath)...)
	return allErrs
}

func validateSessionContainer(containerName string, instance *computev1alpha.Instance, fldPath *field.Path) field.ErrorList {
	sandbox := instance.Spec.Runtime.Sandbox
	if sandbox == nil {
		return field.ErrorList{field.Invalid(fldPath, containerName, fmt.Sprintf(
			"instance %q runs a virtual machine, which has no containers to open a session in", instance.Name))}
	}

	names := make([]string, 0, len(sandbox.Containers))
	for _, container := range sandbox.Containers {
		if container.Name == containerName {
			return nil
		}
		names = append(names, container.Name)
	}
	return field.ErrorList{field.NotSupported(fldPath, containerName, names)}
}

func validateSessionRuntimeClass(instance *computev1alpha.Instance, catalog runtimeclass.Catalog, fldPath *field.Path) field.ErrorList {
	className := instance.Spec.Runtime.Class
	if className == "" {
		className = instance.Labels[computev1alpha.RuntimeClassLabel]
	}

	class := catalog.Find(className)
	if className == "" {
		class = catalog.Default()
	}

	capabilities := runtimeclass.CapabilitiesFrom(class)
	if capabilities.Class == "" {
		capabilities.Class = className
	}
	if class == nil || !capabilities.Supports(runtimeclass.FeatureExec) {
		return field.ErrorList{field.Forbidden(fldPath, fmt.Sprintf(
			"%s does not support shell sessions", capabilities.ClassDescription()))}
	}
	return nil
}

// validateSessionMetadata refuses labels and annotations in compute's own
// namespaces, which compute's controllers and cell agents read and write.
// Admission sets the requester annotation itself, overwriting any value the
// client sent.
func validateSessionMetadata(session *computev1alpha.InstanceConsoleSession) field.ErrorList {
	metaPath := field.NewPath("metadata")
	allErrs := field.ErrorList{}
	for _, key := range slices.Sorted(maps.Keys(session.Annotations)) {
		if key != computev1alpha.InstanceConsoleSessionRequesterAnnotation && reservedKey(key) {
			allErrs = append(allErrs, field.Forbidden(metaPath.Child("annotations").Key(key),
				"annotations in the "+computev1alpha.AnnotationNamespace+" namespace are set by the platform"))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(session.Labels)) {
		if reservedKey(key) {
			allErrs = append(allErrs, field.Forbidden(metaPath.Child("labels").Key(key),
				"labels in the "+computev1alpha.AnnotationNamespace+" namespace are set by the platform"))
		}
	}
	return allErrs
}

func reservedKey(key string) bool {
	prefix, _, found := strings.Cut(key, "/")
	if !found {
		return false
	}
	prefix = strings.ToLower(prefix)
	return prefix == computev1alpha.AnnotationNamespace || strings.HasSuffix(prefix, "."+computev1alpha.AnnotationNamespace)
}
