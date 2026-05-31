// SPDX-License-Identifier: AGPL-3.0-only

package referenceddata

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// projectControlPlanePath returns the API path for a project's control plane.
// This mirrors the rewrite performed by the Milo multicluster provider:
// https://github.com/datum-cloud/milo/blob/main/pkg/multicluster-runtime/milo/provider.go
func projectControlPlanePath(projectID string) string {
	return fmt.Sprintf("/apis/resourcemanager.miloapis.com/v1alpha1/projects/%s/control-plane", projectID)
}

// ProjectReader implements ProjectConfigSecretReader by rewriting a base
// management-plane *rest.Config to the per-project control-plane path and
// caching one controller-runtime client per project.
//
// This uses the same host-rewriting technique as the Milo multicluster
// provider, but WITHOUT the quota sub-path — the quota client cannot read core
// Kubernetes objects (ConfigMaps/Secrets).
type ProjectReader struct {
	// baseConfig is the management identity REST config. It is never mutated;
	// per-project copies are created via rest.CopyConfig.
	baseConfig *rest.Config

	// schemeBuilder is used when constructing per-project clients.
	scheme interface{ AddToScheme(s interface{}) error }

	mu      sync.Mutex
	clients map[string]client.Client
}

// NewProjectReader creates a ProjectReader that will rewrite baseConfig to each
// project's control plane. The caller must supply a *runtime.Scheme that
// includes corev1; typically clientgoscheme is sufficient.
func NewProjectReader(baseConfig *rest.Config, scheme interface {
	AddToScheme(s interface{}) error
}) *ProjectReader {
	return &ProjectReader{
		baseConfig: baseConfig,
		scheme:     scheme,
		clients:    make(map[string]client.Client),
	}
}

// clientFor returns (creating if necessary) a cached controller-runtime client
// pointed at the given project's control plane.
func (r *ProjectReader) clientFor(projectID string) (client.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cl, ok := r.clients[projectID]; ok {
		return cl, nil
	}

	cfg := rest.CopyConfig(r.baseConfig)
	apiHost, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("referenceddata: failed to parse base config host: %w", err)
	}
	apiHost.Path = projectControlPlanePath(projectID)
	cfg.Host = apiHost.String()

	cl, err := client.New(cfg, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("referenceddata: failed to create project client for %q: %w", projectID, err)
	}

	r.clients[projectID] = cl
	return cl, nil
}

// GetConfigMap implements ProjectConfigSecretReader.
func (r *ProjectReader) GetConfigMap(ctx context.Context, projectID, namespace, name string) (*corev1.ConfigMap, error) {
	cl, err := r.clientFor(projectID)
	if err != nil {
		return nil, err
	}

	var cm corev1.ConfigMap
	if err := cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cm); err != nil {
		return nil, classifyError(err, "ConfigMap", namespace, name)
	}
	return &cm, nil
}

// GetSecret implements ProjectConfigSecretReader.
func (r *ProjectReader) GetSecret(ctx context.Context, projectID, namespace, name string) (*corev1.Secret, error) {
	cl, err := r.clientFor(projectID)
	if err != nil {
		return nil, err
	}

	var secret corev1.Secret
	if err := cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &secret); err != nil {
		return nil, classifyError(err, "Secret", namespace, name)
	}
	return &secret, nil
}

// classifyError maps Kubernetes API errors to the sentinel errors defined in
// this package. All other errors are returned unchanged.
func classifyError(err error, kind, namespace, name string) error {
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: %s %s/%s", ErrSourceNotFound, kind, namespace, name)
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return fmt.Errorf("%w: %s %s/%s", ErrSourceUnauthorized, kind, namespace, name)
	}
	return err
}

// LocalReader implements ProjectConfigSecretReader backed by a single
// controller-runtime client. Intended for single-cluster / dev environments
// where the management and project planes are the same cluster, or for
// testing without a Milo control plane.
type LocalReader struct {
	client client.Client
}

// NewLocalReader creates a LocalReader that reads from the given client.
func NewLocalReader(cl client.Client) *LocalReader {
	return &LocalReader{client: cl}
}

// GetConfigMap implements ProjectConfigSecretReader.
// The projectID parameter is ignored; objects are read from the local client.
func (r *LocalReader) GetConfigMap(ctx context.Context, _ string, namespace, name string) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cm); err != nil {
		return nil, classifyError(err, "ConfigMap", namespace, name)
	}
	return &cm, nil
}

// GetSecret implements ProjectConfigSecretReader.
// The projectID parameter is ignored; objects are read from the local client.
func (r *LocalReader) GetSecret(ctx context.Context, _ string, namespace, name string) (*corev1.Secret, error) {
	var secret corev1.Secret
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &secret); err != nil {
		return nil, classifyError(err, "Secret", namespace, name)
	}
	return &secret, nil
}
