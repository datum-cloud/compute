// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

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

	// The four self-describing labels.
	wbTestWDName       = "my-workload-deployment"
	wbTestCityCode     = "DFW"
	wbTestWorkloadName = "my-workload"
	wbTestPlacement    = "us-central"
)

// wbTestCellInstance builds a cell-side Instance with all seven owned labels
// pre-populated, as addInstanceControllerLabels would produce.
func wbTestCellInstance() *computev1alpha.Instance {
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wbTestInstanceName,
			Namespace: wbTestNamespace,
			Labels: map[string]string{
				computev1alpha.WorkloadUIDLabel:            wbTestWorkloadUID,
				computev1alpha.WorkloadDeploymentUIDLabel:  wbTestWDUID,
				computev1alpha.InstanceIndexLabel:          wbTestInstanceIndex,
				computev1alpha.WorkloadDeploymentNameLabel: wbTestWDName,
				computev1alpha.CityCodeLabel:               wbTestCityCode,
				computev1alpha.WorkloadNameLabel:           wbTestWorkloadName,
				computev1alpha.PlacementNameLabel:          wbTestPlacement,
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
					Reason:             computev1alpha.InstanceReadyReasonAvailable,
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

// TestWriteBackToUpstream_CreatePath_AllLabels verifies that the first
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

	err := r.writeBackToUpstream(context.Background(), cellInstance)
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

// TestWriteBackToUpstream_UpdatePath_LabelMerge verifies that an
// existing Karmada Instance with a Karmada-managed label retains that label
// after the update path runs, while all five owned labels are written correctly.
func TestWriteBackToUpstream_UpdatePath_LabelMerge(t *testing.T) {
	t.Parallel()

	karmadaManagedLabel := "karmada.io/managed"

	// Pre-populate the Karmada control plane with a pre-existing Instance
	// carrying only the two linking labels plus a simulated Karmada-managed label.
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

	err := r.writeBackToUpstream(context.Background(), cellInstance)
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

// TestWriteBackToUpstream_LabelChangeTriggerUpdate verifies that
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

	err := r.writeBackToUpstream(context.Background(), cellInstance)
	require.NoError(t, err)

	var updated computev1alpha.Instance
	require.NoError(t, upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
		&updated))

	assert.Equal(t, newWorkloadUID, updated.Labels[computev1alpha.WorkloadUIDLabel],
		"WorkloadUIDLabel change on the cell instance must be reflected in the Karmada object")
}

// TestWriteBackToUpstream_MissingLinkingLabels_Error verifies that
// writeBackToUpstream refuses to create an upstream copy when the cell-side
// Instance lacks the linking labels (e.g. before the stateful control
// strategy's backfill has converged it). The error must name every missing
// label so the wait is diagnosable, and no upstream object may be created —
// an Instance with empty identity labels could never be linked back to its
// owners.
func TestWriteBackToUpstream_MissingLinkingLabels_Error(t *testing.T) {
	t.Parallel()

	s := newKarmadaScheme()
	upstreamClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(wbTestDownstreamNS()).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	r := newWriteBackReconciler(upstreamClient)

	// Instance with nil Labels — simulates an early reconcile before the
	// linking labels are stamped.
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

	err := r.writeBackToUpstream(context.Background(), cellInstance)
	require.Error(t, err)
	for _, key := range []string{
		computev1alpha.WorkloadUIDLabel,
		computev1alpha.WorkloadDeploymentUIDLabel,
		computev1alpha.InstanceIndexLabel,
		computev1alpha.WorkloadDeploymentNameLabel,
		computev1alpha.CityCodeLabel,
		computev1alpha.WorkloadNameLabel,
		computev1alpha.PlacementNameLabel,
	} {
		assert.Contains(t, err.Error(), key,
			"error must name missing label %q", key)
	}

	// No upstream Instance may be created with empty identity labels.
	var created computev1alpha.Instance
	getErr := upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
		&created)
	assert.True(t, apierrors.IsNotFound(getErr),
		"no upstream write-back copy may be created when linking labels are missing (got err: %v)", getErr)
}

// TestWriteBackToUpstream_MissingLinkingLabels_NoUpdate verifies that an
// existing upstream copy is left untouched when the cell-side Instance has
// lost its linking labels: the write-back must error out before the update
// path can overwrite the previously written identity with empty values.
func TestWriteBackToUpstream_MissingLinkingLabels_NoUpdate(t *testing.T) {
	t.Parallel()

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

	// Cell instance lost its labels (only the index label remains).
	cellInstance := wbTestCellInstance()
	delete(cellInstance.Labels, computev1alpha.WorkloadUIDLabel)
	delete(cellInstance.Labels, computev1alpha.WorkloadDeploymentUIDLabel)

	err := r.writeBackToUpstream(context.Background(), cellInstance)
	require.Error(t, err)
	assert.Contains(t, err.Error(), computev1alpha.WorkloadUIDLabel)
	assert.Contains(t, err.Error(), computev1alpha.WorkloadDeploymentUIDLabel)
	assert.NotContains(t, err.Error(), computev1alpha.InstanceIndexLabel,
		"a present label must not be reported missing")

	// The existing upstream copy must keep its previously written identity.
	var existing computev1alpha.Instance
	require.NoError(t, upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
		&existing))
	assert.Equal(t, wbTestWorkloadUID, existing.Labels[computev1alpha.WorkloadUIDLabel],
		"existing WorkloadUIDLabel must not be overwritten with an empty value")
	assert.Equal(t, wbTestWDUID, existing.Labels[computev1alpha.WorkloadDeploymentUIDLabel],
		"existing WorkloadDeploymentUIDLabel must not be overwritten with an empty value")
}

// TestWriteBackToUpstream_MissingSelfDescribingLabel_Error verifies that the
// self-describing labels are required, not best-effort: a cell Instance
// missing only WorkloadDeploymentNameLabel must fail write-back with an error
// naming exactly that label, and no upstream copy may be created.
func TestWriteBackToUpstream_MissingSelfDescribingLabel_Error(t *testing.T) {
	t.Parallel()

	s := newKarmadaScheme()
	upstreamClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(wbTestDownstreamNS()).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	r := newWriteBackReconciler(upstreamClient)

	cellInstance := wbTestCellInstance()
	delete(cellInstance.Labels, computev1alpha.WorkloadDeploymentNameLabel)

	err := r.writeBackToUpstream(context.Background(), cellInstance)
	require.Error(t, err)
	assert.Contains(t, err.Error(), computev1alpha.WorkloadDeploymentNameLabel,
		"error must name the missing label")
	assert.NotContains(t, err.Error(), computev1alpha.CityCodeLabel,
		"a present label must not be reported missing")

	var created computev1alpha.Instance
	getErr := upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
		&created)
	assert.True(t, apierrors.IsNotFound(getErr),
		"no upstream write-back copy may be created when a required label is missing (got err: %v)", getErr)
}

// TestWriteBackToUpstream_NamespaceIdentity_Errors verifies that the
// federation-plane namespace is the strict source of upstream identity:
// a missing namespace or a namespace lacking either upstream-owner label must
// fail the write-back with an error naming the namespace (and label), and no
// upstream copy may be created — there are no fallback identity values.
func TestWriteBackToUpstream_NamespaceIdentity_Errors(t *testing.T) {
	t.Parallel()

	nsWithoutLabel := func(missing string) *corev1.Namespace {
		ns := wbTestDownstreamNS()
		delete(ns.Labels, missing)
		return ns
	}

	tests := []struct {
		name string
		// ns is the federation-plane namespace; nil means it does not exist.
		ns *corev1.Namespace
		// wantInError must all appear in the returned error.
		wantInError []string
	}{
		{
			name:        "namespace missing — error, no copy",
			ns:          nil,
			wantInError: []string{wbTestNamespace},
		},
		{
			name:        "namespace lacks upstream-namespace label — error names namespace and label",
			ns:          nsWithoutLabel(downstreamclient.UpstreamOwnerNamespaceLabel),
			wantInError: []string{wbTestNamespace, downstreamclient.UpstreamOwnerNamespaceLabel},
		},
		{
			name:        "namespace lacks upstream-cluster-name label — error names namespace and label",
			ns:          nsWithoutLabel(downstreamclient.UpstreamOwnerClusterNameLabel),
			wantInError: []string{wbTestNamespace, downstreamclient.UpstreamOwnerClusterNameLabel},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := fake.NewClientBuilder().
				WithScheme(newKarmadaScheme()).
				WithStatusSubresource(&computev1alpha.Instance{})
			if tt.ns != nil {
				builder = builder.WithObjects(tt.ns)
			}
			upstreamClient := builder.Build()

			r := newWriteBackReconciler(upstreamClient)

			err := r.writeBackToUpstream(context.Background(), wbTestCellInstance())
			require.Error(t, err)
			for _, want := range tt.wantInError {
				assert.Contains(t, err.Error(), want)
			}

			var created computev1alpha.Instance
			getErr := upstreamClient.Get(context.Background(),
				types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
				&created)
			assert.True(t, apierrors.IsNotFound(getErr),
				"no upstream write-back copy may be created when upstream identity is unresolvable (got err: %v)", getErr)
		})
	}
}

// TestWriteBackToUpstream_NamespaceGetFailure_Error verifies that a transient
// failure reading the federation-plane namespace aborts the write-back instead
// of proceeding with derived identity values.
func TestWriteBackToUpstream_NamespaceGetFailure_Error(t *testing.T) {
	t.Parallel()

	getFailure := errors.New("federation API unavailable")
	upstreamClient := fake.NewClientBuilder().
		WithScheme(newKarmadaScheme()).
		WithObjects(wbTestDownstreamNS()).
		WithStatusSubresource(&computev1alpha.Instance{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Namespace); ok {
					return getFailure
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	r := newWriteBackReconciler(upstreamClient)

	err := r.writeBackToUpstream(context.Background(), wbTestCellInstance())
	require.ErrorIs(t, err, getFailure)

	var created computev1alpha.Instance
	getErr := upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
		&created)
	assert.True(t, apierrors.IsNotFound(getErr),
		"no upstream write-back copy may be created when the namespace read fails (got err: %v)", getErr)
}

// TestWriteBackToUpstream_FourNewLabels_CreatePath verifies that the four
// self-describing labels (WorkloadDeploymentName, CityCode, WorkloadName,
// PlacementName) are written to the Karmada object on the create path.
func TestWriteBackToUpstream_FourNewLabels_CreatePath(t *testing.T) {
	t.Parallel()

	s := newKarmadaScheme()
	upstreamClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(wbTestDownstreamNS()).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()

	r := newWriteBackReconciler(upstreamClient)
	cellInstance := wbTestCellInstance()

	err := r.writeBackToUpstream(context.Background(), cellInstance)
	require.NoError(t, err)

	var created computev1alpha.Instance
	require.NoError(t, upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
		&created))

	assert.Equal(t, wbTestWDName, created.Labels[computev1alpha.WorkloadDeploymentNameLabel],
		"WorkloadDeploymentNameLabel must propagate to Karmada object")
	assert.Equal(t, wbTestCityCode, created.Labels[computev1alpha.CityCodeLabel],
		"CityCodeLabel must propagate to Karmada object")
	assert.Equal(t, wbTestWorkloadName, created.Labels[computev1alpha.WorkloadNameLabel],
		"WorkloadNameLabel must propagate to Karmada object")
	assert.Equal(t, wbTestPlacement, created.Labels[computev1alpha.PlacementNameLabel],
		"PlacementNameLabel must propagate to Karmada object")
}

// TestWriteBackToUpstream_FourNewLabels_UpdatePath verifies that the four
// self-describing labels are written on the update path and existing Karmada-
// managed labels on the downstream object are preserved.
func TestWriteBackToUpstream_FourNewLabels_UpdatePath(t *testing.T) {
	t.Parallel()

	karmadaManagedLabel := "karmada.io/managed"

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

	err := r.writeBackToUpstream(context.Background(), cellInstance)
	require.NoError(t, err)

	var updated computev1alpha.Instance
	require.NoError(t, upstreamClient.Get(context.Background(),
		types.NamespacedName{Namespace: wbTestNamespace, Name: wbTestInstanceName},
		&updated))

	assert.Equal(t, wbTestWDName, updated.Labels[computev1alpha.WorkloadDeploymentNameLabel],
		"WorkloadDeploymentNameLabel must be set on update path")
	assert.Equal(t, wbTestCityCode, updated.Labels[computev1alpha.CityCodeLabel],
		"CityCodeLabel must be set on update path")
	assert.Equal(t, wbTestWorkloadName, updated.Labels[computev1alpha.WorkloadNameLabel],
		"WorkloadNameLabel must be set on update path")
	assert.Equal(t, wbTestPlacement, updated.Labels[computev1alpha.PlacementNameLabel],
		"PlacementNameLabel must be set on update path")

	// Karmada-managed label must survive the merge.
	assert.Equal(t, "true", updated.Labels[karmadaManagedLabel],
		"Karmada-managed label must be preserved after the update merge")
}
