// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
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
	projTestInstanceName = "inst-abc"

	// projTestWDUID is the UID of the owning WorkloadDeployment.
	projTestWDUID = types.UID("wd-uid-9999-aaaa-bbbb-cccc")

	// projTestWDName is the name of the owning WorkloadDeployment.
	projTestWDName = "my-wd"
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
			PlacementName: "default",
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
		computev1alpha.WorkloadDeploymentUIDLabel:      string(projTestWDUID),
	}
	for k, v := range labelOverrides {
		labels[k] = v
	}
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
		UpstreamClient: karmadaClient,
		MCManager:      mgr,
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

		// wantRequeue controls whether the reconcile result should request a requeue.
		wantRequeue bool

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
			name: "projection created without owner ref when WD UID label absent",
			karmadaInstance: projTestKarmadaInstance(map[string]string{
				// Override: remove the WD UID label.
				computev1alpha.WorkloadDeploymentUIDLabel: "",
			}),
			projectObjs: []client.Object{
				projTestProjectNS(),
				// No WorkloadDeployment in project cluster.
			},
			wantProjection: true,
			wantOwnerRef:   false,
		},
		{
			name: "missing upstream-cluster-name label — skipped, no projection",
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
			projectObjs:    []client.Object{projTestProjectNS()},
			wantProjection: false,
		},
		{
			name: "missing upstream-namespace label — requeue",
			karmadaInstance: projTestKarmadaInstance(map[string]string{
				// Override: remove the upstream namespace label.
				downstreamclient.UpstreamOwnerNamespaceLabel: "",
			}),
			projectObjs:    []client.Object{projTestProjectNS()},
			wantProjection: false,
			wantRequeue:    true,
		},
		{
			name:            "karmada instance not found — no-op",
			karmadaInstance: nil, // causes Get to return NotFound
			projectObjs:     []client.Object{projTestProjectNS()},
			wantProjection:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Build Karmada client.
			var karmadaObjs []client.Object
			if tt.karmadaInstance != nil {
				karmadaObjs = append(karmadaObjs, tt.karmadaInstance)
			}
			karmadaClient := newKarmadaFakeClient(karmadaObjs...)

			// Build project client.
			projectClient := fake.NewClientBuilder().
				WithScheme(newProjectScheme()).
				WithObjects(tt.projectObjs...).
				WithStatusSubresource(&computev1alpha.Instance{}).
				Build()

			r := newTestProjector(karmadaClient, projectClient)

			result, err := r.Reconcile(context.Background(), projectorRequest())

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tt.wantRequeue {
				assert.NotZero(t, result.RequeueAfter, "expected RequeueAfter to be set")
			} else {
				assert.Equal(t, ctrl.Result{}, result)
			}

			ctx := context.Background()

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

			// Owner reference check.
			if tt.wantOwnerRef {
				require.NotEmpty(t, projection.OwnerReferences,
					"projected instance should have an owner reference to the WorkloadDeployment")
				ownerRef := projection.OwnerReferences[0]
				assert.Equal(t, string(projTestWDUID), string(ownerRef.UID),
					"owner reference UID should match the WorkloadDeployment UID")
				assert.Equal(t, projTestWDName, ownerRef.Name,
					"owner reference name should match the WorkloadDeployment name")
			} else {
				assert.Empty(t, projection.OwnerReferences,
					"projected instance should have no owner reference when WD not found")
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

// isNotFound returns true when err is a Kubernetes not-found error or is nil
// (object not found means Get returned NotFound, not that err is nil).
// Used to distinguish "no projection created" from "projection exists but Get failed".
func isNotFound(err error) bool {
	if err == nil {
		return false // object exists — not the "not found" case
	}
	// Import apierrors to check — we already have it via the fake client package.
	return client.IgnoreNotFound(err) == nil
}
