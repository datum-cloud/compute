// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

const (
	// smTestEdgeNS mirrors the ns-{uid} edge namespaces NSO creates.
	smTestEdgeNS = "ns-efdf8ca1-7b6e-4a30-9b1c-0d6f55555555"
	// smTestEncodedCluster mirrors the "cluster-<project>" encoding stamped by
	// NSO's MappedNamespaceResourceStrategy.
	smTestEncodedCluster = "cluster-datum-cloud"
	smTestProjectID      = "datum-cloud"
	smTestProjectNS      = "default"
	smTestCluster        = "single"
)

// smEdgeNamespace builds an edge namespace shaped like production: both
// identity labels are stamped together at creation. Passing nil labels models
// convention drift where the stamping never happened.
func smEdgeNamespace(labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   smTestEdgeNS,
			Labels: labels,
		},
	}
}

func smInstance() *computev1alpha.Instance {
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-instance",
			Namespace: smTestEdgeNS,
		},
	}
}

func TestNewSingleModeProjectID(t *testing.T) {
	t.Run("label present: decodes cluster-<name> to the project ID", func(t *testing.T) {
		ns := smEdgeNamespace(map[string]string{
			downstreamclient.UpstreamOwnerClusterNameLabel: smTestEncodedCluster,
			downstreamclient.UpstreamOwnerNamespaceLabel:   smTestProjectNS,
		})
		mgr := newFakeMCManager(smTestCluster, newFakeCluster(newProjectFakeClient(ns)))

		projectID, err := NewSingleModeProjectID(mgr)(context.Background(), smTestCluster, smInstance())
		require.NoError(t, err)
		assert.Equal(t, smTestProjectID, projectID)
	})

	t.Run("label absent: returns errProjectIdentityUnresolvable naming the label", func(t *testing.T) {
		// Only the namespace label is present — the cluster-name label was never
		// stamped (convention drift, not a propagation race).
		ns := smEdgeNamespace(map[string]string{
			downstreamclient.UpstreamOwnerNamespaceLabel: smTestProjectNS,
		})
		mgr := newFakeMCManager(smTestCluster, newFakeCluster(newProjectFakeClient(ns)))

		_, err := NewSingleModeProjectID(mgr)(context.Background(), smTestCluster, smInstance())
		require.Error(t, err)
		assert.ErrorIs(t, err, errProjectIdentityUnresolvable)
		assert.Contains(t, err.Error(), smTestEdgeNS,
			"error must name the edge namespace")
		assert.Contains(t, err.Error(), downstreamclient.UpstreamOwnerClusterNameLabel,
			"error must name the missing label")
	})

	t.Run("namespace read failure: transient error, not the sentinel", func(t *testing.T) {
		mgr := newFakeMCManager(smTestCluster, newFakeCluster(newProjectFakeClient()))

		_, err := NewSingleModeProjectID(mgr)(context.Background(), smTestCluster, smInstance())
		require.Error(t, err)
		assert.False(t, errors.Is(err, errProjectIdentityUnresolvable),
			"a failed namespace read is transient and must not be classified as unresolvable identity")
	})
}

func TestNewSingleModeProjectNamespace(t *testing.T) {
	t.Run("label present: returns the in-project namespace", func(t *testing.T) {
		ns := smEdgeNamespace(map[string]string{
			downstreamclient.UpstreamOwnerClusterNameLabel: smTestEncodedCluster,
			downstreamclient.UpstreamOwnerNamespaceLabel:   smTestProjectNS,
		})
		mgr := newFakeMCManager(smTestCluster, newFakeCluster(newProjectFakeClient(ns)))

		projectNS, err := NewSingleModeProjectNamespace(mgr)(context.Background(), smTestCluster, smInstance())
		require.NoError(t, err)
		assert.Equal(t, smTestProjectNS, projectNS)
	})

	t.Run("label absent: returns errProjectIdentityUnresolvable naming the label", func(t *testing.T) {
		ns := smEdgeNamespace(map[string]string{
			downstreamclient.UpstreamOwnerClusterNameLabel: smTestEncodedCluster,
		})
		mgr := newFakeMCManager(smTestCluster, newFakeCluster(newProjectFakeClient(ns)))

		_, err := NewSingleModeProjectNamespace(mgr)(context.Background(), smTestCluster, smInstance())
		require.Error(t, err)
		assert.ErrorIs(t, err, errProjectIdentityUnresolvable)
		assert.Contains(t, err.Error(), smTestEdgeNS,
			"error must name the edge namespace")
		assert.Contains(t, err.Error(), downstreamclient.UpstreamOwnerNamespaceLabel,
			"error must name the missing label")
	})

	t.Run("namespace read failure: transient error, not the sentinel", func(t *testing.T) {
		mgr := newFakeMCManager(smTestCluster, newFakeCluster(newProjectFakeClient()))

		_, err := NewSingleModeProjectNamespace(mgr)(context.Background(), smTestCluster, smInstance())
		require.Error(t, err)
		assert.False(t, errors.Is(err, errProjectIdentityUnresolvable),
			"a failed namespace read is transient and must not be classified as unresolvable identity")
	})
}
