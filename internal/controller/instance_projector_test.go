// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// ─── Test constants ───────────────────────────────────────────────────────────

const (
	// projTestCluster is the project cluster name used in projector tests.
	projTestCluster = "project-cluster"

	// projTestProjNS is the project namespace name.
	projTestProjNS = "proj-namespace"

	// projTestProjNSUID is the project namespace UID embedded in the Karmada
	// namespace name below.
	projTestProjNSUID = types.UID("deadbeef-1111-2222-3333-444455556666")

	// projTestKarmadaNS is the Karmada namespace derived from the UID above
	// via the ns-<uid> convention.
	projTestKarmadaNS = "ns-deadbeef-1111-2222-3333-444455556666"

	// projTestInstanceName is the name of the Karmada (and projected) Instance.
	// Follows the "<wd-name>-<ordinal>" convention: "my-wd-0".
	projTestInstanceName = "my-wd-0"

	// projTestWDUID is the UID of the owning WorkloadDeployment as it exists in
	// the PROJECT cluster. This is the UID that owner references must use, since
	// Kubernetes GC in the project cluster only knows this UID.
	projTestWDUID = types.UID("project-wd-uid-9999-aaaa-bbbb-cccc")

	// projTestEdgeWDUID is the UID of the WorkloadDeployment as it exists on the
	// EDGE/Karmada plane. Each plane mints its own UID, so this is intentionally
	// distinct from projTestWDUID. The WorkloadDeploymentUIDLabel on downstream
	// Instances carries this edge UID — NOT the project UID.
	projTestEdgeWDUID = types.UID("edge-uid-0000-1111-2222-3333")

	// projTestWDName is the name of the owning WorkloadDeployment. The name is
	// the same across all planes (project cluster, Karmada, edge) and is the
	// correct cross-plane stable identifier.
	projTestWDName = "my-wd"

	// projTestWorkloadUID is the UID of the owning Workload (carried via WorkloadUIDLabel).
	projTestWorkloadUID = "wl-uid-1111-2222-3333-4444"

	// projTestInstanceIndex is the ordinal index of the instance (carried via InstanceIndexLabel).
	projTestInstanceIndex = "0"
)

// encodedCluster returns the value of the UpstreamOwnerClusterNameLabel for
// projTestCluster ("cluster-<name>").
func encodedCluster() string {
	return "cluster-" + projTestCluster
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// projTestProjectNS builds the project cluster Namespace with the stable test UID.
func projTestProjectNS() *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: projTestProjNS,
			UID:  projTestProjNSUID,
		},
	}
}

func projTestHubNamespace() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: projTestKarmadaNS,
		Labels: map[string]string{
			downstreamclient.UpstreamOwnerClusterNameLabel: encodedCluster(),
			downstreamclient.UpstreamOwnerNamespaceLabel:   projTestProjNS,
		},
	}}
}

// projTestWorkloadDeployment builds the project WorkloadDeployment that owns
// projected Instances.
func projTestWorkloadDeployment() *computev1alpha.WorkloadDeployment {
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      projTestWDName,
			Namespace: projTestProjNS,
			UID:       projTestWDUID,
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			LocationRef:   locationsv1alpha1.LocationReference{Name: testWestLocationName},
			PlacementName: testDefaultPlacement,
			WorkloadRef:   computev1alpha.WorkloadReference{Name: "my-workload"},
			ScaleSettings: computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
		},
	}
}

// projTestKarmadaInstance builds a Karmada Instance with the default labels
// needed for the InstanceProjector to act on it.  Optional label overrides are
// applied last.
func projTestKarmadaInstance(labelOverrides map[string]string) *computev1alpha.Instance {
	labels := map[string]string{
		downstreamclient.UpstreamOwnerClusterNameLabel: encodedCluster(),
		downstreamclient.UpstreamOwnerNamespaceLabel:   projTestProjNS,
		// WorkloadDeploymentUIDLabel carries the EDGE UID — intentionally distinct
		// from projTestWDUID (the project-cluster WD UID). Owner references must
		// never be built from this value.
		computev1alpha.WorkloadDeploymentUIDLabel:  string(projTestEdgeWDUID),
		computev1alpha.WorkloadDeploymentNameLabel: projTestWDName,
		computev1alpha.WorkloadUIDLabel:            projTestWorkloadUID,
		computev1alpha.InstanceIndexLabel:          projTestInstanceIndex,
	}
	maps.Copy(labels, labelOverrides)
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      projTestInstanceName,
			Namespace: projTestKarmadaNS,
			Labels:    labels,
		},
		Spec: computev1alpha.InstanceSpec{
			// Minimal valid spec — actual content is copied to the projection.
		},
	}
}

// newTestProjector wires an InstanceProjector with the given downstream client and
// a project cluster that serves the supplied project client.
func newTestProjector(karmadaClient client.Client, projectClient client.Client) *InstanceProjector {
	projectCluster := newFakeCluster(projectClient)
	mgr := newFakeMCManager(projTestCluster, projectCluster)
	return &InstanceProjector{
		FederationClient: karmadaClient,
		MCManager:        mgr,
	}
}

// projectorRequest builds a ctrl.Request for the test Instance in Karmada.
func projectorRequest() ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      projTestInstanceName,
			Namespace: projTestKarmadaNS,
		},
	}
}

// ─── Tests ───────────────────────────────────────────────────────────────────

// TestInstanceProjector_Reconcile is the primary table-driven test.
func TestInstanceProjector_Reconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string

		// karmadaInstance is what exists in the Karmada API server.
		// A nil value means the Instance does not exist (not-found path).
		karmadaInstance *computev1alpha.Instance

		// projectObjs are pre-populated in the project cluster fake client.
		projectObjs []client.Object

		// wantProjection controls whether a projected Instance should appear.
		wantProjection bool

		// wantOwnerRef controls whether the projected Instance should have an
		// owner reference pointing to the project WorkloadDeployment.
		wantOwnerRef bool

		// wantErr controls whether the reconcile should return an error.
		wantErr bool
	}{
		{
			name:            "happy path — instance projected with owner reference",
			karmadaInstance: projTestKarmadaInstance(nil),
			projectObjs: []client.Object{
				projTestProjectNS(),
				projTestWorkloadDeployment(),
			},
			wantProjection: true,
			wantOwnerRef:   true,
		},
		{
			// Cross-plane UID regression test: the Karmada Instance carries the EDGE
			// WD UID in WorkloadDeploymentUIDLabel (projTestEdgeWDUID), which is
			// intentionally different from the project-cluster WD UID (projTestWDUID).
			// The owner reference on the projection must use the project-cluster UID.
			// This test fails if someone reintroduces UID-based matching against the
			// edge/Karmada plane.
			name:            "WD name label present, edge UID differs from project UID — owner ref UID equals project WD UID",
			karmadaInstance: projTestKarmadaInstance(nil), // carries projTestEdgeWDUID, not projTestWDUID
			projectObjs: []client.Object{
				projTestProjectNS(),
				projTestWorkloadDeployment(), // UID is projTestWDUID
			},
			wantProjection: true,
			wantOwnerRef:   true,
		},
		{
			// When the project WD does not yet exist (transient ordering race —
			// Instance projected before WorkloadReconciler created the project WD)
			// the projector must return an error and NOT create an ownerless
			// projection: its only watch is the Instance, so nothing fires when
			// the WD appears — error backoff is the retry mechanism.
			name:            "project WD not found — error, no ownerless projection created",
			karmadaInstance: projTestKarmadaInstance(nil),
			projectObjs: []client.Object{
				projTestProjectNS(),
				// No WorkloadDeployment — simulates the transient ordering race.
			},
			wantProjection: false,
			wantErr:        true,
		},
		{
			// A write-back copy always carries its WorkloadDeployment name; the
			// label is stamped when the copy is created.
			name: "WD name label absent — error, no projection",
			karmadaInstance: projTestKarmadaInstance(map[string]string{
				computev1alpha.WorkloadDeploymentNameLabel: "",
			}),
			projectObjs:    []client.Object{projTestProjectNS()},
			wantProjection: false,
			wantErr:        true,
		},
		{
			// Federation-plane Instances are exclusively write-back copies and the
			// write-back stamps both upstream-owner labels atomically, so a missing
			// cluster label is a stamping-invariant violation, not a foreign object.
			name: "missing upstream-cluster-name label — error, no projection",
			karmadaInstance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      projTestInstanceName,
					Namespace: projTestKarmadaNS,
					// Intentionally no UpstreamOwnerClusterNameLabel.
					Labels: map[string]string{
						"some-other-label": "some-other-value",
					},
				},
			},
			projectObjs:    []client.Object{projTestProjectNS()},
			wantProjection: false,
			wantErr:        true,
		},
		{
			// The write-back stamps both upstream-owner labels together, so a
			// cluster label without a namespace label is the same invariant
			// violation.
			name: "missing upstream-namespace label — error, no projection",
			karmadaInstance: projTestKarmadaInstance(map[string]string{
				// Override: remove the upstream namespace label.
				downstreamclient.UpstreamOwnerNamespaceLabel: "",
			}),
			projectObjs:    []client.Object{projTestProjectNS()},
			wantProjection: false,
			wantErr:        true,
		},
		{
			name:            "karmada instance not found — no-op",
			karmadaInstance: nil, // causes Get to return NotFound
			projectObjs:     []client.Object{projTestProjectNS()},
			wantProjection:  false,
		},
		{
			// Verify that all linking labels (WorkloadUID, WorkloadDeploymentUID,
			// WorkloadDeploymentNameLabel, InstanceIndex) survive from the Karmada
			// write-back object through to the projection.
			name:            "all linking labels propagated from Karmada to projection",
			karmadaInstance: projTestKarmadaInstance(nil),
			projectObjs: []client.Object{
				projTestProjectNS(),
				projTestWorkloadDeployment(),
			},
			wantProjection: true,
			wantOwnerRef:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var karmadaObjs []client.Object
			if tt.karmadaInstance != nil {
				karmadaObjs = append(karmadaObjs, tt.karmadaInstance)
			}
			karmadaClient := newKarmadaFakeClient(karmadaObjs...)

			projectClient := fake.NewClientBuilder().
				WithScheme(newProjectScheme()).
				WithObjects(tt.projectObjs...).
				WithStatusSubresource(&computev1alpha.Instance{}).
				Build()

			r := newTestProjector(karmadaClient, projectClient)

			req := projectorRequest()
			result, err := r.Reconcile(context.Background(), req)

			if tt.wantErr {
				require.Error(t, err)
				assert.Zero(t, result.RequeueAfter,
					"errors rely on controller backoff, not a flat requeue")
				// No error path may leave a projection behind — in particular,
				// an ownerless projection must never be created.
				var projection computev1alpha.Instance
				getErr := projectClient.Get(context.Background(), types.NamespacedName{
					Name:      req.Name,
					Namespace: projTestProjNS,
				}, &projection)
				assert.True(t, isNotFound(getErr),
					"expected no projection in project namespace on error, but found one (or unexpected error: %v)", getErr)
				return
			}
			require.NoError(t, err)

			ctx := context.Background()

			if tt.wantProjection {
				assert.Equal(t, ctrl.Result{}, result)
			}

			// Check whether a projected Instance exists in the project namespace.
			var projection computev1alpha.Instance
			err = projectClient.Get(ctx, types.NamespacedName{
				Name:      projTestInstanceName,
				Namespace: projTestProjNS,
			}, &projection)

			if !tt.wantProjection {
				assert.True(t, isNotFound(err),
					"expected no projection in project namespace, but found one (or unexpected error: %v)", err)
				return
			}

			require.NoError(t, err, "expected projection to exist in project namespace")

			// Labels should be copied from the Karmada instance.
			if tt.karmadaInstance != nil {
				for k, v := range tt.karmadaInstance.Labels {
					assert.Equal(t, v, projection.Labels[k],
						"projection label %q should match Karmada instance label", k)
				}
			}

			// Linking labels must survive from the Karmada instance to the projection
			// so that the CLI can resolve Workload name, city, and instance ordinal.
			if tt.wantProjection && tt.karmadaInstance != nil {
				assert.Equal(t,
					tt.karmadaInstance.Labels[computev1alpha.WorkloadUIDLabel],
					projection.Labels[computev1alpha.WorkloadUIDLabel],
					"WorkloadUIDLabel must be propagated to the projection")
				assert.Equal(t,
					tt.karmadaInstance.Labels[computev1alpha.WorkloadDeploymentUIDLabel],
					projection.Labels[computev1alpha.WorkloadDeploymentUIDLabel],
					"WorkloadDeploymentUIDLabel must be propagated to the projection")
				assert.Equal(t,
					tt.karmadaInstance.Labels[computev1alpha.WorkloadDeploymentNameLabel],
					projection.Labels[computev1alpha.WorkloadDeploymentNameLabel],
					"WorkloadDeploymentNameLabel must be propagated to the projection")
				assert.Equal(t,
					tt.karmadaInstance.Labels[computev1alpha.InstanceIndexLabel],
					projection.Labels[computev1alpha.InstanceIndexLabel],
					"InstanceIndexLabel must be propagated to the projection")
			}

			if tt.wantOwnerRef {
				require.NotEmpty(t, projection.OwnerReferences,
					"projected instance should have an owner reference to the WorkloadDeployment")
				ownerRef := projection.OwnerReferences[0]
				// Core invariant: owner ref UID must be the PROJECT-cluster WD UID.
				assert.Equal(t, string(projTestWDUID), string(ownerRef.UID),
					"owner reference UID must match the project-cluster WorkloadDeployment UID")
				// Regression guard: the edge UID must NOT appear in the owner ref.
				// If this assertion fails, someone reintroduced cross-plane UID matching.
				assert.NotEqual(t, string(projTestEdgeWDUID), string(ownerRef.UID),
					"owner reference UID must NOT be the edge/Karmada WD UID")
				assert.Equal(t, projTestWDName, ownerRef.Name,
					"owner reference name should match the WorkloadDeployment name")
			} else {
				assert.Empty(t, projection.OwnerReferences,
					"projected instance should have no owner reference")
			}
		})
	}
}

func TestInstanceProjector_RemovesProjectionOnScaleDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		source            *computev1alpha.Instance
		hubNamespace      bool
		projectionExists  bool
		projectionMatches bool
		wantDeleted       bool
		wantErr           bool
	}{
		{
			name:              "write-back already gone",
			hubNamespace:      true,
			projectionExists:  true,
			projectionMatches: true,
			wantDeleted:       true,
		},
		{
			name: "write-back deleting",
			source: func() *computev1alpha.Instance {
				instance := projTestKarmadaInstance(nil)
				now := metav1.Now()
				instance.DeletionTimestamp = &now
				instance.Finalizers = []string{instanceProjectionFinalizer, "test-finalizer"}
				return instance
			}(),
			projectionExists:  true,
			projectionMatches: true,
			wantDeleted:       true,
		},
		{
			name:              "already removed",
			hubNamespace:      true,
			projectionMatches: true,
			wantDeleted:       true,
		},
		{
			name:              "different route is preserved",
			hubNamespace:      true,
			projectionExists:  true,
			projectionMatches: false,
			wantErr:           true,
		},
		{
			name:              "hub namespace removed during project teardown",
			projectionExists:  true,
			projectionMatches: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var hubObjects []client.Object
			if tt.source != nil {
				hubObjects = append(hubObjects, tt.source)
			}
			if tt.hubNamespace {
				hubObjects = append(hubObjects, projTestHubNamespace())
			}
			var projectObjects []client.Object
			projectObjects = append(projectObjects, projTestProjectNS(), projTestWorkloadDeployment())
			if tt.projectionExists {
				projection := projTestKarmadaInstance(nil)
				projection.Namespace = projTestProjNS
				if !tt.projectionMatches {
					projection.Labels[downstreamclient.UpstreamOwnerNamespaceLabel] = "another-namespace"
				}
				projectObjects = append(projectObjects, projection)
			}

			projectClient := fake.NewClientBuilder().
				WithScheme(newProjectScheme()).
				WithObjects(projectObjects...).
				Build()
			r := newTestProjector(newKarmadaFakeClient(hubObjects...), projectClient)
			_, err := r.Reconcile(context.Background(), projectorRequest())
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			var projection computev1alpha.Instance
			err = projectClient.Get(context.Background(), types.NamespacedName{
				Name: projTestInstanceName, Namespace: projTestProjNS,
			}, &projection)
			if tt.wantDeleted || !tt.projectionExists {
				assert.True(t, apierrors.IsNotFound(err), "projection should be absent")
			} else {
				require.NoError(t, err, "unrelated projection should remain")
			}
			if tt.source != nil {
				var writeBack computev1alpha.Instance
				require.NoError(t, r.FederationClient.Get(context.Background(), projectorRequest().NamespacedName, &writeBack))
				assert.NotContains(t, writeBack.Finalizers, instanceProjectionFinalizer,
					"project projection must be deleted before releasing the write-back")
			}
			var owner computev1alpha.WorkloadDeployment
			require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{
				Name: projTestWDName, Namespace: projTestProjNS,
			}, &owner), "scale-down must preserve the WorkloadDeployment")
		})
	}
}

func TestInstanceProjectionDeletedBeforeCellInstanceFinalizes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	writeBack := projTestKarmadaInstance(nil)
	writeBack.Finalizers = []string{instanceProjectionFinalizer}
	hubClient := newKarmadaFakeClient(projTestHubNamespace(), writeBack)
	projection := writeBack.DeepCopy()
	projection.Namespace = projTestProjNS
	projection.Finalizers = nil
	projectClient := fake.NewClientBuilder().
		WithScheme(newProjectScheme()).
		WithObjects(projTestProjectNS(), projTestWorkloadDeployment(), projection).
		Build()
	projector := newTestProjector(hubClient, projectClient)
	cellReconciler := &InstanceReconciler{FederationClient: hubClient}

	_, err := cellReconciler.Finalize(ctx, writeBack)
	require.ErrorContains(t, err, "waiting for downstream write-back")
	var terminating computev1alpha.Instance
	require.NoError(t, hubClient.Get(ctx, projectorRequest().NamespacedName, &terminating))
	require.NotNil(t, terminating.DeletionTimestamp)
	var stillProjected computev1alpha.Instance
	require.NoError(t, projectClient.Get(ctx, types.NamespacedName{
		Name: projTestInstanceName, Namespace: projTestProjNS,
	}, &stillProjected), "cell finalization must wait while the project Instance remains")

	_, err = projector.Reconcile(ctx, projectorRequest())
	require.NoError(t, err)
	var removed computev1alpha.Instance
	require.True(t, apierrors.IsNotFound(projectClient.Get(ctx, types.NamespacedName{
		Name: projTestInstanceName, Namespace: projTestProjNS,
	}, &removed)))
	_, err = cellReconciler.Finalize(ctx, writeBack)
	require.NoError(t, err, "cell finalization can finish after the projection and write-back are gone")
}

func TestInstanceProjectorWaitsForProjectDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	writeBack := projTestKarmadaInstance(nil)
	writeBack.Finalizers = []string{instanceProjectionFinalizer}
	now := metav1.Now()
	writeBack.DeletionTimestamp = &now
	hubClient := newKarmadaFakeClient(writeBack)
	projection := writeBack.DeepCopy()
	projection.Namespace = projTestProjNS
	projection.DeletionTimestamp = nil
	projection.Finalizers = []string{"project-cleanup"}
	projectClient := fake.NewClientBuilder().
		WithScheme(newProjectScheme()).
		WithObjects(projTestProjectNS(), projTestWorkloadDeployment(), projection).
		Build()
	projector := newTestProjector(hubClient, projectClient)

	_, err := projector.Reconcile(ctx, projectorRequest())
	require.ErrorContains(t, err, "waiting for instance projection")
	var pending computev1alpha.Instance
	require.NoError(t, projectClient.Get(ctx, types.NamespacedName{
		Name: projTestInstanceName, Namespace: projTestProjNS,
	}, &pending))
	require.NotNil(t, pending.DeletionTimestamp)
	var retained computev1alpha.Instance
	require.NoError(t, hubClient.Get(ctx, projectorRequest().NamespacedName, &retained))
	assert.Contains(t, retained.Finalizers, instanceProjectionFinalizer)

	pending.Finalizers = nil
	require.NoError(t, projectClient.Update(ctx, &pending))
	_, err = projector.Reconcile(ctx, projectorRequest())
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(hubClient.Get(ctx, projectorRequest().NamespacedName, &retained)),
		"write-back should disappear only after the project Instance does")
}

// TestInstanceProjector_SpecCopied verifies that the Instance spec is correctly
// propagated from the Karmada instance to the projection.
func TestInstanceProjector_SpecCopied(t *testing.T) {
	t.Parallel()

	karmadaInst := projTestKarmadaInstance(nil)
	// Set a recognizable spec field we can assert against.
	karmadaInst.Spec.Controller = &computev1alpha.InstanceController{
		SchedulingGates: []computev1alpha.SchedulingGate{{Name: "test-gate"}},
	}

	projectClient := fake.NewClientBuilder().
		WithScheme(newProjectScheme()).
		WithObjects(projTestProjectNS(), projTestWorkloadDeployment()).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()
	karmadaClient := newKarmadaFakeClient(karmadaInst)

	r := newTestProjector(karmadaClient, projectClient)
	_, err := r.Reconcile(context.Background(), projectorRequest())
	require.NoError(t, err)

	var projection computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Name: projTestInstanceName, Namespace: projTestProjNS},
		&projection))

	require.NotNil(t, projection.Spec.Controller)
	require.Len(t, projection.Spec.Controller.SchedulingGates, 1)
	assert.Equal(t, "test-gate", projection.Spec.Controller.SchedulingGates[0].Name)
}

// TestInstanceProjector_NamespaceResolution verifies that the projector resolves
// the target project namespace directly from the UpstreamOwnerNamespaceLabel on
// the Karmada Instance, landing the projection in the correct namespace.
func TestInstanceProjector_NamespaceResolution(t *testing.T) {
	t.Parallel()

	karmadaInst := projTestKarmadaInstance(nil)
	projectClient := fake.NewClientBuilder().
		WithScheme(newProjectScheme()).
		WithObjects(
			projTestProjectNS(),
			projTestWorkloadDeployment(),
		).
		WithStatusSubresource(&computev1alpha.Instance{}).
		Build()
	karmadaClient := newKarmadaFakeClient(karmadaInst)

	r := newTestProjector(karmadaClient, projectClient)
	result, err := r.Reconcile(context.Background(), projectorRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Projection must land in the namespace named by the label.
	var projection computev1alpha.Instance
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Name: projTestInstanceName, Namespace: projTestProjNS},
		&projection))
}

// isNotFound returns true only when err is a Kubernetes not-found error; a nil
// error means the object exists and returns false.
// Used to distinguish "no projection created" from "projection exists but Get failed".
func isNotFound(err error) bool {
	if err == nil {
		return false // object exists — not the "not found" case
	}
	return client.IgnoreNotFound(err) == nil
}

func TestInstanceProjector_RunsOnlyOnLeader(t *testing.T) {
	t.Parallel()

	lock := &fakeLeaseLock{identity: "this-replica", holder: "other-replica"}
	federationCache, reconciled := startProjectorManager(t, lock)

	select {
	case <-federationCache.watched:
		t.Fatal("projector started watching Instances while another replica held the lease")
	case key := <-reconciled:
		t.Fatalf("projector reconciled %s while another replica held the lease", key)
	case <-time.After(2 * time.Second):
	}
	require.Positive(t, lock.gets(), "the replica never attempted to acquire the lease")

	lock.release()

	select {
	case <-federationCache.watched:
	case <-time.After(10 * time.Second):
		t.Fatal("projector did not start after this replica became leader")
	}
	federationCache.informer.Add(projTestKarmadaInstance(nil))

	select {
	case key := <-reconciled:
		assert.Equal(t, projectorRequest().NamespacedName, key)
	case <-time.After(10 * time.Second):
		t.Fatal("projector did not reconcile after this replica became leader")
	}
}

func startProjectorManager(t *testing.T, lock resourcelock.Interface) (*watchSignallingCache, <-chan types.NamespacedName) {
	t.Helper()

	scheme := newKarmadaScheme()
	leaseDuration := 2 * time.Second
	renewDeadline := time.Second
	retryPeriod := 100 * time.Millisecond

	mgr, err := manager.New(&rest.Config{Host: "https://127.0.0.1:1"}, manager.Options{
		Scheme:                              scheme,
		Metrics:                             metricsserver.Options{BindAddress: "0"},
		LeaderElection:                      true,
		LeaderElectionID:                    "instance-projector-test",
		LeaderElectionNamespace:             "default",
		LeaderElectionResourceLockInterface: lock,
		LeaseDuration:                       &leaseDuration,
		RenewDeadline:                       &renewDeadline,
		RetryPeriod:                         &retryPeriod,
		Controller:                          ctrlconfig.Controller{SkipNameValidation: new(true)},
	})
	require.NoError(t, err)

	federationCache := &watchSignallingCache{
		FakeInformers: &informertest.FakeInformers{Scheme: scheme},
		watched:       make(chan struct{}),
	}
	reconciled := make(chan types.NamespacedName, 100)
	federationClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(_ context.Context, _ client.WithWatch, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
				reconciled <- key
				return apierrors.NewNotFound(schema.GroupResource{}, key.Name)
			},
		}).
		Build()

	r := &InstanceProjector{FederationClient: federationClient}
	require.NoError(t, r.SetupWithManager(mgr, &fakeFederationCluster{cache: federationCache}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = mgr.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	return federationCache, reconciled
}

type fakeFederationCluster struct {
	cluster.Cluster
	cache cache.Cache
}

func (f *fakeFederationCluster) GetCache() cache.Cache { return f.cache }

type watchSignallingCache struct {
	*informertest.FakeInformers
	watched  chan struct{}
	once     sync.Once
	informer *controllertest.FakeInformer
}

func (c *watchSignallingCache) GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	informer, err := c.FakeInformers.GetInformer(ctx, obj, opts...)
	if err != nil {
		return nil, err
	}
	return &watchSignallingInformer{FakeInformer: informer.(*controllertest.FakeInformer), cache: c}, nil
}

type watchSignallingInformer struct {
	*controllertest.FakeInformer
	cache *watchSignallingCache
}

func (i *watchSignallingInformer) AddEventHandlerWithOptions(
	h toolscache.ResourceEventHandler, opts toolscache.HandlerOptions,
) (toolscache.ResourceEventHandlerRegistration, error) {
	reg, err := i.FakeInformer.AddEventHandlerWithOptions(h, opts)
	i.cache.once.Do(func() {
		i.cache.informer = i.FakeInformer
		close(i.cache.watched)
	})
	return reg, err
}

type fakeLeaseLock struct {
	identity string

	mu       sync.Mutex
	holder   string
	getCalls int
}

func (l *fakeLeaseLock) Get(context.Context) (*resourcelock.LeaderElectionRecord, []byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.getCalls++
	record := resourcelock.LeaderElectionRecord{
		HolderIdentity:       l.holder,
		LeaseDurationSeconds: 3600,
	}
	raw, err := json.Marshal(record)
	return &record, raw, err
}

func (l *fakeLeaseLock) Create(_ context.Context, ler resourcelock.LeaderElectionRecord) error {
	return l.Update(context.Background(), ler)
}

func (l *fakeLeaseLock) Update(_ context.Context, ler resourcelock.LeaderElectionRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holder = ler.HolderIdentity
	return nil
}

func (l *fakeLeaseLock) RecordEvent(string) {}

func (l *fakeLeaseLock) Identity() string { return l.identity }

func (l *fakeLeaseLock) Describe() string { return "fake/instance-projector-test" }

func (l *fakeLeaseLock) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holder = ""
}

func (l *fakeLeaseLock) gets() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.getCalls
}
