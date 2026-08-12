// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/federation"
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
			CityCode:      "LAX",
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

		// wantQuarantineReason is the terminal reason the object must be
		// quarantined with. Empty means the object must not be quarantined.
		wantQuarantineReason string
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
			// A write-back copy that cannot identify its WorkloadDeployment can
			// never be projected. The label is stamped when the copy is created, so
			// no retry supplies it later. Report it once and quarantine it.
			name: "WD name label absent — quarantined, no projection",
			karmadaInstance: projTestKarmadaInstance(map[string]string{
				computev1alpha.WorkloadDeploymentNameLabel: "",
			}),
			projectObjs:          []client.Object{projTestProjectNS()},
			wantProjection:       false,
			wantQuarantineReason: federation.QuarantineReasonMissingDeploymentName,
		},
		{
			// Federation-plane Instances are exclusively write-back copies and the
			// write-back stamps both upstream-owner labels atomically, so a missing
			// cluster label is a stamping-invariant violation, not a foreign object.
			name: "missing upstream-cluster-name label — quarantined as unidentifiable",
			karmadaInstance: &computev1alpha.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      projTestInstanceName,
					Namespace: projTestKarmadaNS,
					// Intentionally no UpstreamOwnerClusterNameLabel.
					Labels: map[string]string{
						"some-other-label": "value",
					},
				},
			},
			projectObjs:          []client.Object{projTestProjectNS()},
			wantProjection:       false,
			wantQuarantineReason: federation.QuarantineReasonUnidentifiable,
		},
		{
			// The write-back stamps both upstream-owner labels together, so a
			// cluster label without a namespace label is the same invariant
			// violation.
			name: "missing upstream-namespace label — quarantined",
			karmadaInstance: projTestKarmadaInstance(map[string]string{
				// Override: remove the upstream namespace label.
				downstreamclient.UpstreamOwnerNamespaceLabel: "",
			}),
			projectObjs:          []client.Object{projTestProjectNS()},
			wantProjection:       false,
			wantQuarantineReason: federation.QuarantineReasonMissingNamespaceLabel,
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

			if tt.wantQuarantineReason != "" {
				var quarantined computev1alpha.Instance
				require.NoError(t, karmadaClient.Get(ctx, req.NamespacedName, &quarantined))
				assert.Equal(t, tt.wantQuarantineReason,
					quarantined.Annotations[computev1alpha.QuarantineReasonAnnotation],
					"hub object must record the terminal reason")
				assert.NotEmpty(t, quarantined.Annotations[computev1alpha.QuarantineFingerprintAnnotation],
					"quarantine must record the fingerprint that invalidates it")
			}
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
