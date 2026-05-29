// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// ─── Log capture helper ───────────────────────────────────────────────────────

// logEntry holds a single captured log line (message + formatted key-value pairs).
type logEntry struct {
	msg string
	kvs string // funcr renders key-value pairs as a single string
}

// captureLogger returns a logr.Logger backed by an in-memory sink and a pointer
// to the slice of captured entries. Thread-safe; safe to call from parallel tests.
func captureLogger() (logr.Logger, *[]logEntry) {
	var mu sync.Mutex
	var entries []logEntry
	logger := funcr.New(func(prefix, args string) {
		mu.Lock()
		defer mu.Unlock()
		entries = append(entries, logEntry{msg: prefix, kvs: args})
	}, funcr.Options{})
	return logger, &entries
}

// ─── write-back test constants ────────────────────────────────────────────────

const (
	wbTestClusterName    = "edge-cluster"
	wbTestNamespace      = "ns-proj-uid-1234"
	wbTestInstanceName   = "inst-0"
	wbTestWorkloadUID    = "wl-uid-aaaa-bbbb"
	wbTestWDUID          = "wd-uid-cccc-dddd"
	wbTestInstanceIndex  = "0"
	wbTestUpstreamNS     = "proj-namespace"
	wbTestEncodedCluster = "cluster-" + wbTestClusterName
)

// wbTestCellInstance builds a cell-side Instance with the three linking labels
// pre-populated, as addInstanceControllerLabels would produce.
func wbTestCellInstance() *computev1alpha.Instance {
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wbTestInstanceName,
			Namespace: wbTestNamespace,
			Labels: map[string]string{
				computev1alpha.WorkloadUIDLabel:           wbTestWorkloadUID,
				computev1alpha.WorkloadDeploymentUIDLabel: wbTestWDUID,
				computev1alpha.InstanceIndexLabel:         wbTestInstanceIndex,
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
			},
		},
		Status: computev1alpha.InstanceStatus{
			Conditions: []metav1.Condition{
				{
					Type:               computev1alpha.InstanceReady,
					Status:             metav1.ConditionTrue,
					Reason:             computev1alpha.InstanceReadyReasonRunning,
					Message:            "Instance is ready",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
}

// wbTestDownstreamNS returns a Namespace object in the downstream (Karmada)
// control plane that carries the upstream routing labels, simulating the
// namespace stamped by NSO's MappedNamespaceResourceStrategy.
func wbTestDownstreamNS() *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: wbTestNamespace,
			Labels: map[string]string{
				downstreamclient.UpstreamOwnerNamespaceLabel:   wbTestUpstreamNS,
				downstreamclient.UpstreamOwnerClusterNameLabel: wbTestEncodedCluster,
			},
		},
	}
}

// newWriteBackReconciler wires an InstanceReconciler whose FederationClient is set
// to federationClient and whose local cluster has a single cell instance.
func newWriteBackReconciler(federationClient client.Client) *InstanceReconciler {
	return &InstanceReconciler{
		FederationClient: federationClient,
	}
}

// ─── Tests ───────────────────────────────────────────────────────────────────

// TestWriteBackToUpstream_CreatePath_AllLabels (Case A) verifies that the first
// write-back to an empty Karmada control plane creates an Instance with all five
// expected labels (two routing + three linking) and also writes the cell-side
// status via Status().Update.
func TestWriteBackToUpstream_CreatePath_AllLabels(t *testing.T) {
	t.Parallel()

	s := newKarmadaScheme()
	upstreamClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(wbTestDownstreamNS()).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	r := newWriteBackReconciler(upstreamClient)
	cellInstance := wbTestCellInstance()

	err := r.writeBackToUpstream(context.Background(), multicluster.ClusterName(wbTestClusterName), cellInstance)
	require.NoError(t, err)

	// Verify the created Karmada Instance carries all five expected labels.
	var created computev1alpha.Instance
	require.NoError(t, upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
		&created))

	assert.Equal(t, wbTestEncodedCluster, created.Labels[downstreamclient.UpstreamOwnerClusterNameLabel],
		"UpstreamOwnerClusterNameLabel must be set")
	assert.Equal(t, wbTestUpstreamNS, created.Labels[downstreamclient.UpstreamOwnerNamespaceLabel],
		"UpstreamOwnerNamespaceLabel must be set")
	assert.Equal(t, wbTestWorkloadUID, created.Labels[computev1alpha.WorkloadUIDLabel],
		"WorkloadUIDLabel must be propagated from cell instance")
	assert.Equal(t, wbTestWDUID, created.Labels[computev1alpha.WorkloadDeploymentUIDLabel],
		"WorkloadDeploymentUIDLabel must be propagated from cell instance")
	assert.Equal(t, wbTestInstanceIndex, created.Labels[computev1alpha.InstanceIndexLabel],
		"InstanceIndexLabel must be propagated from cell instance")

	// Status must have been written via Status().Update after Create.
	require.Len(t, created.Status.Conditions, 1,
		"Status().Update must be called after Create; condition should be present")
	assert.Equal(t, computev1alpha.InstanceReady, created.Status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, created.Status.Conditions[0].Status)
}

// TestWriteBackToUpstream_UpdatePath_LabelMerge (Case B) verifies that an
// existing Karmada Instance with a Karmada-managed label retains that label
// after the update path runs, while all five owned labels are written correctly.
func TestWriteBackToUpstream_UpdatePath_LabelMerge(t *testing.T) {
	t.Parallel()

	karmadaManagedLabel := "karmada.io/managed"

	// Pre-populate the Karmada control plane with an Instance that has the old
	// two-label map plus a simulated Karmada-managed label.
	existingKarmadaInstance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wbTestInstanceName,
			Namespace: wbTestNamespace,
			Labels: map[string]string{
				downstreamclient.UpstreamOwnerClusterNameLabel: wbTestEncodedCluster,
				downstreamclient.UpstreamOwnerNamespaceLabel:   wbTestUpstreamNS,
				karmadaManagedLabel:                            "true",
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
			},
		},
	}

	s := newKarmadaScheme()
	upstreamClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(wbTestDownstreamNS(), existingKarmadaInstance).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	r := newWriteBackReconciler(upstreamClient)
	cellInstance := wbTestCellInstance()

	err := r.writeBackToUpstream(context.Background(), multicluster.ClusterName(wbTestClusterName), cellInstance)
	require.NoError(t, err)

	var updated computev1alpha.Instance
	require.NoError(t, upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
		&updated))

	// All five owned labels must be present with correct values.
	assert.Equal(t, wbTestEncodedCluster, updated.Labels[downstreamclient.UpstreamOwnerClusterNameLabel])
	assert.Equal(t, wbTestUpstreamNS, updated.Labels[downstreamclient.UpstreamOwnerNamespaceLabel])
	assert.Equal(t, wbTestWorkloadUID, updated.Labels[computev1alpha.WorkloadUIDLabel])
	assert.Equal(t, wbTestWDUID, updated.Labels[computev1alpha.WorkloadDeploymentUIDLabel])
	assert.Equal(t, wbTestInstanceIndex, updated.Labels[computev1alpha.InstanceIndexLabel])

	// The Karmada-managed label must survive the merge (not be replaced/deleted).
	assert.Equal(t, "true", updated.Labels[karmadaManagedLabel],
		"Karmada-managed label must be preserved after merge; should not be overwritten")
}

// TestWriteBackToUpstream_LabelChangeTriggerUpdate (Case C) verifies that
// a changed linking label on the cell instance causes the Karmada object to
// be updated with the new value.
func TestWriteBackToUpstream_LabelChangeTriggerUpdate(t *testing.T) {
	t.Parallel()

	newWorkloadUID := "wl-uid-CHANGED"

	// Pre-populate with the five-label map from a previous write-back.
	existingKarmadaInstance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wbTestInstanceName,
			Namespace: wbTestNamespace,
			Labels: map[string]string{
				downstreamclient.UpstreamOwnerClusterNameLabel: wbTestEncodedCluster,
				downstreamclient.UpstreamOwnerNamespaceLabel:   wbTestUpstreamNS,
				computev1alpha.WorkloadUIDLabel:                wbTestWorkloadUID,
				computev1alpha.WorkloadDeploymentUIDLabel:      wbTestWDUID,
				computev1alpha.InstanceIndexLabel:              wbTestInstanceIndex,
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
			},
		},
	}

	s := newKarmadaScheme()
	upstreamClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(wbTestDownstreamNS(), existingKarmadaInstance).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	r := newWriteBackReconciler(upstreamClient)

	// Modify the WorkloadUIDLabel on the cell instance.
	cellInstance := wbTestCellInstance()
	cellInstance.Labels[computev1alpha.WorkloadUIDLabel] = newWorkloadUID

	err := r.writeBackToUpstream(context.Background(), multicluster.ClusterName(wbTestClusterName), cellInstance)
	require.NoError(t, err)

	var updated computev1alpha.Instance
	require.NoError(t, upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
		&updated))

	assert.Equal(t, newWorkloadUID, updated.Labels[computev1alpha.WorkloadUIDLabel],
		"WorkloadUIDLabel change on the cell instance must be reflected in the Karmada object")
}

// TestWriteBackToUpstream_EmptyLinkingLabels_NonFatal (Case D) verifies that
// writeBackToUpstream completes without error when the cell-side Instance has
// no linking labels (e.g. during an early reconcile before
// addInstanceControllerLabels has run). The created Karmada object will carry
// empty string values for the three linking labels, and the RC-2 warning log
// must fire listing all three missing label keys.
func TestWriteBackToUpstream_EmptyLinkingLabels_NonFatal(t *testing.T) {
	t.Parallel()

	s := newKarmadaScheme()
	upstreamClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(wbTestDownstreamNS()).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	r := newWriteBackReconciler(upstreamClient)

	// Instance with nil Labels — simulates an early reconcile with no linking labels.
	cellInstance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wbTestInstanceName,
			Namespace: wbTestNamespace,
		},
		Spec: computev1alpha.InstanceSpec{
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{InstanceType: testInstanceType},
			},
		},
	}

	// Inject a capturing logger so we can assert the RC-2 warning fires.
	capLogger, entries := captureLogger()
	ctx := log.IntoContext(context.Background(), capLogger)

	// Must not return an error — empty labels are non-fatal.
	err := r.writeBackToUpstream(ctx, multicluster.ClusterName(wbTestClusterName), cellInstance)
	require.NoError(t, err)

	// The Karmada object should exist with empty string values for the linking labels.
	var created computev1alpha.Instance
	require.NoError(t, upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
		&created))

	assert.Equal(t, "", created.Labels[computev1alpha.WorkloadUIDLabel],
		"WorkloadUIDLabel should be empty string when not set on cell instance")
	assert.Equal(t, "", created.Labels[computev1alpha.WorkloadDeploymentUIDLabel],
		"WorkloadDeploymentUIDLabel should be empty string when not set on cell instance")
	assert.Equal(t, "", created.Labels[computev1alpha.InstanceIndexLabel],
		"InstanceIndexLabel should be empty string when not set on cell instance")

	// Assert the RC-2 warning was emitted and named all three missing label keys.
	// funcr encodes both the message and key-value pairs into the args string;
	// we search across the full rendered output for each required substring.
	warnMsg := "instance is missing linking labels for write-back"
	allRendered := func() string {
		parts := make([]string, len(*entries))
		for i, e := range *entries {
			parts[i] = fmt.Sprintf("%s %s", e.msg, e.kvs)
		}
		return strings.Join(parts, "\n")
	}()

	assert.True(t, strings.Contains(allRendered, warnMsg),
		"expected RC-2 warning %q to be logged; got:\n%s", warnMsg, allRendered)
	for _, key := range []string{
		computev1alpha.WorkloadUIDLabel,
		computev1alpha.WorkloadDeploymentUIDLabel,
		computev1alpha.InstanceIndexLabel,
	} {
		assert.True(t, strings.Contains(allRendered, key),
			"expected missing label key %q to appear in warning log; got:\n%s", key, allRendered)
	}
}
