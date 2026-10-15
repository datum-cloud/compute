// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// NewSingleModeProjectID returns an InstanceProjectIDFunc for single-cell mode.
// It reads the upstream-cluster-name label on the edge namespace (e.g.
// "cluster-datum-cloud") and decodes it to the project ID ("datum-cloud").
// This is the inverse of the "cluster-<name>" encoding used by NSO's
// MappedNamespaceResourceStrategy when stamping cluster-scoped namespace labels.
// The label is stamped atomically at namespace creation, before any Instance
// can exist in the namespace, so an absent label is misconfiguration: the
// returned error wraps errProjectIdentityUnresolvable and names the namespace
// and the missing label. Transient API failures return ordinary errors
// (requeue with backoff).
func NewSingleModeProjectID(mgr mcmanager.Manager) InstanceProjectIDFunc {
	return func(ctx context.Context, cn multicluster.ClusterName, inst *computev1alpha.Instance) (string, error) {
		ns, err := readEdgeNamespace(ctx, mgr, cn, inst.Namespace)
		if err != nil {
			return "", err
		}
		encoded := ns.Labels[downstreamclient.UpstreamOwnerClusterNameLabel]
		if encoded == "" {
			return "", fmt.Errorf("edge namespace %q is missing label %q: %w",
				inst.Namespace, downstreamclient.UpstreamOwnerClusterNameLabel, errProjectIdentityUnresolvable)
		}
		return DecodeClusterName(encoded), nil
	}
}

// NewSingleModeProjectNamespace returns an InstanceProjectNamespaceFunc for
// single-cell mode. It reads the upstream-namespace label on the edge namespace
// (e.g. "ns-efdf8ca1-...") to find the in-project namespace ("default") where
// ResourceClaims must be created in the project control plane.
// The label is stamped atomically at namespace creation, before any Instance
// can exist in the namespace, so an absent label is misconfiguration: the
// returned error wraps errProjectIdentityUnresolvable and names the namespace
// and the missing label. Transient API failures return ordinary errors
// (requeue with backoff).
func NewSingleModeProjectNamespace(mgr mcmanager.Manager) InstanceProjectNamespaceFunc {
	return func(ctx context.Context, cn multicluster.ClusterName, inst *computev1alpha.Instance) (string, error) {
		ns, err := readEdgeNamespace(ctx, mgr, cn, inst.Namespace)
		if err != nil {
			return "", err
		}
		projectNS := ns.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
		if projectNS == "" {
			return "", fmt.Errorf("edge namespace %q is missing label %q: %w",
				inst.Namespace, downstreamclient.UpstreamOwnerNamespaceLabel, errProjectIdentityUnresolvable)
		}
		return projectNS, nil
	}
}

// readEdgeNamespace reads the edge namespace object via the uncached APIReader
// (no informer started, no cache sync required) with a short deadline.
// Returns a transient error on API failures so callers can requeue with backoff.
func readEdgeNamespace(
	ctx context.Context,
	mgr mcmanager.Manager,
	clusterName multicluster.ClusterName,
	namespace string,
) (corev1.Namespace, error) {
	cl, err := mgr.GetCluster(ctx, clusterName)
	if err != nil {
		return corev1.Namespace{}, fmt.Errorf("readEdgeNamespace: getting cluster %q: %w", clusterName, err)
	}
	var ns corev1.Namespace
	getCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := cl.GetAPIReader().Get(getCtx, client.ObjectKey{Name: namespace}, &ns); err != nil {
		return corev1.Namespace{}, fmt.Errorf("readEdgeNamespace: reading namespace %q: %w", namespace, err)
	}
	return ns, nil
}
