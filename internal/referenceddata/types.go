// SPDX-License-Identifier: AGPL-3.0-only

// Package referenceddata is the capability seam for resolving and delivering
// ConfigMaps and Secrets referenced by workload templates. It is designed to
// be promotable into a shared platform library.
//
// Phase 0 provides: types, the reference collector, companion naming, and the
// ProjectConfigSecretReader interface + implementation. The resolver controller
// (Phase 1) and cell gate-clearing (Phase 2) build on top of this foundation.
package referenceddata

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
)

// ErrSourceNotFound indicates that a referenced ConfigMap or Secret could not
// be found in the project namespace (HTTP 404 from the project control plane).
var ErrSourceNotFound = errors.New("referenced source not found")

// ErrSourceUnauthorized indicates that the management identity does not have
// permission to read the referenced object (HTTP 401 or 403 from the project
// control plane).
var ErrSourceUnauthorized = errors.New("not authorized to read referenced source")

// ObjectRef identifies a ConfigMap or Secret by kind and name within a
// namespace. References are always same-namespace (the Workload namespace).
type ObjectRef struct {
	// Kind is either "ConfigMap" or "Secret".
	Kind string

	// Name is the name of the ConfigMap or Secret.
	Name string

	// Namespace is the namespace containing the object.
	Namespace string
}

// ReferencedSet is a deduplicated, deterministically-ordered set of ObjectRefs
// collected from a workload template.
type ReferencedSet []ObjectRef

// SubjectRef identifies the end-user making the admission request. Used by the
// SAR admission check to verify that the submitting user can read each
// referenced object.
type SubjectRef struct {
	Username string
	Groups   []string
	UID      string
}

// DeliveryRequest carries everything the resolver needs to materialise companion
// objects on the downstream (cell) namespace.
type DeliveryRequest struct {
	// Subject is the user identity used for admission authorisation.
	Subject SubjectRef

	// ReferencedObjects is the deduplicated set of ConfigMaps and Secrets to deliver.
	ReferencedObjects ReferencedSet

	// DownstreamNamespace is the namespace on the cell where companions will land
	// (typically ns-{project-uid}).
	DownstreamNamespace string
}

// DeliveryResult is returned by the resolver after materialising companions.
type DeliveryResult struct {
	// CompanionNames is the deterministic list of companion object names produced
	// by CompanionName, sorted the same way as the expected-set annotation.
	CompanionNames []string

	// AllDelivered is true when every companion in CompanionNames was
	// successfully written (created or updated) to the downstream.
	AllDelivered bool
}

// ProjectConfigSecretReader reads ConfigMaps and Secrets from a project's
// control plane. Implementations must classify errors as ErrSourceNotFound or
// ErrSourceUnauthorized where appropriate.
//
// The interface is intentionally narrow — it does not expose list, update, or
// delete — so that the implementation can be tightly scoped to the read-only
// path the management identity requires.
type ProjectConfigSecretReader interface {
	// GetConfigMap returns the named ConfigMap from the given project namespace.
	// Returns ErrSourceNotFound if the object does not exist, or
	// ErrSourceUnauthorized if the caller lacks permission.
	GetConfigMap(ctx context.Context, projectID, namespace, name string) (*corev1.ConfigMap, error)

	// GetSecret returns the named Secret from the given project namespace.
	// Returns ErrSourceNotFound if the object does not exist, or
	// ErrSourceUnauthorized if the caller lacks permission.
	GetSecret(ctx context.Context, projectID, namespace, name string) (*corev1.Secret, error)
}
