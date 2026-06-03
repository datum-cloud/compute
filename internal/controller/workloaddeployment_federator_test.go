// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/finalizer"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// ─── Shared test constants ────────────────────────────────────────────────────

const (
	testCluster      = "test-project-cluster"
	testProjNS       = "my-project"
	testProjNSUID    = types.UID("aabbccdd-0000-1111-2222-333344445555")
	testKarmadaNSStr = "ns-aabbccdd-0000-1111-2222-333344445555"
	testWDName       = "my-workload-deployment"
	testCityCodeLAX  = "LAX"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// testProjectNamespace returns a corev1.Namespace for the project cluster with a
// stable UID that matches testKarmadaNSStr.
func testProjectNamespace() *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testProjNS,
			UID:  testProjNSUID,
		},
	}
}

// testWorkloadDeployment returns a WorkloadDeployment with the given options.
func testWorkloadDeployment(opts ...func(*computev1alpha.WorkloadDeployment)) *computev1alpha.WorkloadDeployment {
	wd := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWDName,
			Namespace: testProjNS,
			UID:       "wd-uid-1111",
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode: testCityCodeLAX,
			WorkloadRef: computev1alpha.WorkloadReference{
				Name: "test-workload",
			},
			PlacementName: testDefaultPlacement,
			ScaleSettings: computev1alpha.HorizontalScaleSettings{
				MinReplicas: 1,
			},
		},
	}
	for _, opt := range opts {
		opt(wd)
	}
	return wd
}

// withFinalizer adds the federator finalizer to the WorkloadDeployment.
func withFinalizer(wd *computev1alpha.WorkloadDeployment) {
	wd.Finalizers = append(wd.Finalizers, federatorFinalizer)
}

// withDeletionTimestamp sets a non-zero DeletionTimestamp on the WorkloadDeployment.
func withDeletionTimestamp(wd *computev1alpha.WorkloadDeployment) {
	t := metav1.NewTime(time.Now().Add(-5 * time.Second))
	wd.DeletionTimestamp = &t
}

// newTestFederator constructs a WorkloadDeploymentFederator wired to the given
// project client (via a fakeMCManager) and downstream client.  The federator
// finalizer is pre-registered so reconcile can handle deletions.
func newTestFederator(projectClient client.Client, karmadaClient client.Client) *WorkloadDeploymentFederator {
	projectCluster := newFakeCluster(projectClient)
	mgr := newFakeMCManager(testCluster, projectCluster)

	r := &WorkloadDeploymentFederator{
		mgr:              mgr,
		FederationClient: karmadaClient,
	}

	feds := finalizer.NewFinalizers()
	if err := feds.Register(federatorFinalizer, r); err != nil {
		panic("failed to register test finalizer: " + err.Error())
	}
	r.finalizers = feds
	return r
}

// reconcileRequest builds an mcreconcile.Request for the test WorkloadDeployment.
func reconcileRequest() mcreconcile.Request {
	return mcreconcile.Request{
		ClusterName: testCluster,
		Request: ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      testWDName,
				Namespace: testProjNS,
			},
		},
	}
}

// ─── Unit tests ───────────────────────────────────────────────────────────────

// TestMapDownstreamDeploymentToRequest verifies the downstream-WD → project-WD
// mapping used by the cross-plane status watch: the request name equals the
// downstream WD name, the namespace comes from the WD's upstream-namespace label,
// and the cluster name is decoded from the downstream namespace's
// upstream-cluster-name label. Events lacking correlation metadata are dropped.
func TestMapDownstreamDeploymentToRequest(t *testing.T) {
	t.Parallel()

	// The encoded cluster name on the downstream namespace decodes to testCluster.
	encodedCluster := "cluster-" + strings.ReplaceAll(testCluster, "/", "_")

	downstreamNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testKarmadaNSStr,
			Labels: map[string]string{
				downstreamclient.UpstreamOwnerClusterNameLabel: encodedCluster,
			},
		},
	}

	newDownstreamWD := func(labels map[string]string) *computev1alpha.WorkloadDeployment {
		return &computev1alpha.WorkloadDeployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testWDName,
				Namespace: testKarmadaNSStr,
				Labels:    labels,
			},
		}
	}

	tests := []struct {
		name         string
		karmadaObjs  []client.Object
		downstreamWD *computev1alpha.WorkloadDeployment
		want         []mcreconcile.Request
	}{
		{
			name:        "maps to project WD request",
			karmadaObjs: []client.Object{downstreamNS},
			downstreamWD: newDownstreamWD(map[string]string{
				downstreamclient.UpstreamOwnerNamespaceLabel: testProjNS,
			}),
			want: []mcreconcile.Request{
				{
					ClusterName: testCluster,
					Request: ctrl.Request{
						NamespacedName: types.NamespacedName{
							Namespace: testProjNS,
							Name:      testWDName,
						},
					},
				},
			},
		},
		{
			name:         "missing upstream-namespace label is dropped",
			karmadaObjs:  []client.Object{downstreamNS},
			downstreamWD: newDownstreamWD(nil),
			want:         nil,
		},
		{
			name:        "missing downstream namespace is dropped",
			karmadaObjs: nil, // namespace not present in federation cluster
			downstreamWD: newDownstreamWD(map[string]string{
				downstreamclient.UpstreamOwnerNamespaceLabel: testProjNS,
			}),
			want: nil,
		},
		{
			name: "namespace without cluster label is dropped",
			karmadaObjs: []client.Object{&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testKarmadaNSStr},
			}},
			downstreamWD: newDownstreamWD(map[string]string{
				downstreamclient.UpstreamOwnerNamespaceLabel: testProjNS,
			}),
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			karmadaClient := newKarmadaFakeClient(tt.karmadaObjs...)
			r := &WorkloadDeploymentFederator{
				FederationClient:  karmadaClient,
				FederationCluster: newFakeCluster(karmadaClient),
			}

			got := r.mapDownstreamDeploymentToRequest(context.Background(), tt.downstreamWD)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeUpstreamClusterName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		encoded string
		want    string
	}{
		{"cluster-datum-cloud", "datum-cloud"},
		{"cluster-org_project", "org/project"},
		{"cluster-test-project-cluster", "test-project-cluster"},
	}
	for _, tt := range tests {
		t.Run(tt.encoded, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, decodeUpstreamClusterName(tt.encoded))
		})
	}
}

func TestPropagationPolicyNameFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		cityCode string
		want     string
	}{
		{"LAX", "city-lax"},
		{"lax", "city-lax"},
		{"New York", "city-new-york"},
		{"LOS ANGELES", "city-los-angeles"},
		{"SEA", "city-sea"},
	}

	for _, tt := range tests {
		t.Run(tt.cityCode, func(t *testing.T) {
			t.Parallel()
			got := propagationPolicyNameFor(tt.cityCode)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestWorkloadDeploymentFederator_NoFederationClient verifies that the reconciler
// is a no-op when FederationClient is nil.
func TestWorkloadDeploymentFederator_NoFederationClient(t *testing.T) {
	t.Parallel()

	projectClient := newProjectFakeClient(testProjectNamespace(), testWorkloadDeployment())
	r := newTestFederator(projectClient, nil)
	r.FederationClient = nil // explicitly nil

	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// TestWorkloadDeploymentFederator_AddsFinalizerOnFirstSeen verifies that the
// first reconcile of a brand-new WorkloadDeployment adds the finalizer and
// returns without federating (the finalizer update triggers a re-queue).
func TestWorkloadDeploymentFederator_AddsFinalizerOnFirstSeen(t *testing.T) {
	t.Parallel()

	wd := testWorkloadDeployment() // no finalizer yet
	projectClient := newProjectFakeClient(testProjectNamespace(), wd)
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// The project WD should now have the finalizer persisted.
	var updated computev1alpha.WorkloadDeployment
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Name: testWDName, Namespace: testProjNS}, &updated))
	assert.Contains(t, updated.Finalizers, federatorFinalizer)

	// Karmada should be untouched – federation happens on the next reconcile.
	var wdList computev1alpha.WorkloadDeploymentList
	require.NoError(t, karmadaClient.List(context.Background(), &wdList))
	assert.Empty(t, wdList.Items, "no Karmada WD should be created on first-seen reconcile")
}

// TestWorkloadDeploymentFederator_FederatesToKarmada verifies that a
// WorkloadDeployment with the finalizer already set is fully federated:
// the Karmada namespace, WorkloadDeployment (with city-code label), and
// PropagationPolicy are all created.
func TestWorkloadDeploymentFederator_FederatesToKarmada(t *testing.T) {
	t.Parallel()

	wd := testWorkloadDeployment(withFinalizer)
	projectClient := newProjectFakeClient(testProjectNamespace(), wd)
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	ctx := context.Background()

	// Karmada namespace must exist.
	var karmadaNS corev1.Namespace
	err = karmadaClient.Get(ctx, types.NamespacedName{Name: testKarmadaNSStr}, &karmadaNS)
	require.NoError(t, err, "Karmada namespace %q should exist", testKarmadaNSStr)

	// Karmada WorkloadDeployment must exist with the city-code label.
	var karmadaWD computev1alpha.WorkloadDeployment
	err = karmadaClient.Get(ctx, types.NamespacedName{
		Name:      testWDName,
		Namespace: testKarmadaNSStr,
	}, &karmadaWD)
	require.NoError(t, err, "Karmada WorkloadDeployment should exist")
	assert.Equal(t, testCityCodeLAX, karmadaWD.Labels[cityCodeLabel],
		"city-code label should be set on Karmada WD")
	assert.Equal(t, testCityCodeLAX, karmadaWD.Spec.CityCode,
		"spec.cityCode should be copied from project WD")

	// PropagationPolicy for the city code must exist.
	ppName := propagationPolicyNameFor(testCityCodeLAX)
	var pp karmadapolicyv1alpha1.PropagationPolicy
	err = karmadaClient.Get(ctx, types.NamespacedName{
		Name:      ppName,
		Namespace: testKarmadaNSStr,
	}, &pp)
	require.NoError(t, err, "PropagationPolicy %q should exist", ppName)

	// The PP must select WorkloadDeployments by the city-code label.
	require.Len(t, pp.Spec.ResourceSelectors, 1)
	sel := pp.Spec.ResourceSelectors[0]
	assert.Equal(t, computev1alpha.GroupVersion.String(), sel.APIVersion)
	assert.Equal(t, "WorkloadDeployment", sel.Kind)
	require.NotNil(t, sel.LabelSelector)
	assert.Equal(t, testCityCodeLAX, sel.LabelSelector.MatchLabels[cityCodeLabel])

	// The PP cluster affinity must target clusters carrying the same city-code.
	require.NotNil(t, pp.Spec.Placement.ClusterAffinity)
	require.NotNil(t, pp.Spec.Placement.ClusterAffinity.LabelSelector)
	assert.Equal(t, testCityCodeLAX,
		pp.Spec.Placement.ClusterAffinity.LabelSelector.MatchLabels[cityCodeLabel])
}

// TestWorkloadDeploymentFederator_Finalization covers the deletion scenarios:
// cleanup of Karmada resources and conditional PropagationPolicy removal.
func TestWorkloadDeploymentFederator_Finalization(t *testing.T) {
	t.Parallel()

	ppName := propagationPolicyNameFor(testCityCodeLAX)

	tests := []struct {
		name string
		// karmadaExtra holds additional Karmada objects beyond the "own" WD and PP.
		karmadaExtra []client.Object
		wantPPGone   bool
	}{
		{
			name:         "last WD for city — PropagationPolicy removed",
			karmadaExtra: nil,
			wantPPGone:   true,
		},
		{
			name: "other WD for same city remains — PropagationPolicy kept",
			karmadaExtra: []client.Object{
				// A sibling WD in the same Karmada namespace with the same city-code.
				&computev1alpha.WorkloadDeployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "other-deployment",
						Namespace: testKarmadaNSStr,
						Labels:    map[string]string{cityCodeLabel: testCityCodeLAX},
					},
					Spec: computev1alpha.WorkloadDeploymentSpec{
						CityCode:      testCityCodeLAX,
						PlacementName: "other",
						WorkloadRef:   computev1alpha.WorkloadReference{Name: "other"},
						ScaleSettings: computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
					},
				},
			},
			wantPPGone: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Project cluster: namespace + WD with finalizer and deletion timestamp.
			wd := testWorkloadDeployment(withFinalizer, withDeletionTimestamp)
			projectClient := newProjectFakeClient(testProjectNamespace(), wd)

			// Karmada cluster: the mirrored WD + its PropagationPolicy + any extras.
			karmadaWD := &computev1alpha.WorkloadDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testWDName,
					Namespace: testKarmadaNSStr,
					Labels:    map[string]string{cityCodeLabel: testCityCodeLAX},
				},
				Spec: computev1alpha.WorkloadDeploymentSpec{
					CityCode:      testCityCodeLAX,
					PlacementName: testDefaultPlacement,
					WorkloadRef:   computev1alpha.WorkloadReference{Name: "test-workload"},
					ScaleSettings: computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
				},
			}
			karmadaPP := &karmadapolicyv1alpha1.PropagationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ppName,
					Namespace: testKarmadaNSStr,
				},
			}
			karmadaObjs := []client.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testKarmadaNSStr}},
				karmadaWD,
				karmadaPP,
			}
			karmadaObjs = append(karmadaObjs, tt.karmadaExtra...)
			karmadaClient := newKarmadaFakeClient(karmadaObjs...)

			r := newTestFederator(projectClient, karmadaClient)

			result, err := r.Reconcile(context.Background(), reconcileRequest())
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)

			ctx := context.Background()

			// The Karmada-side WD must be gone.
			var remainingWD computev1alpha.WorkloadDeployment
			err = karmadaClient.Get(ctx, types.NamespacedName{
				Name:      testWDName,
				Namespace: testKarmadaNSStr,
			}, &remainingWD)
			assert.True(t, apierrors.IsNotFound(err),
				"Karmada WD %q should be deleted after finalization", testWDName)

			// PropagationPolicy presence depends on whether siblings remain.
			var remainingPP karmadapolicyv1alpha1.PropagationPolicy
			err = karmadaClient.Get(ctx, types.NamespacedName{
				Name:      ppName,
				Namespace: testKarmadaNSStr,
			}, &remainingPP)
			if tt.wantPPGone {
				assert.True(t, apierrors.IsNotFound(err),
					"PropagationPolicy should be deleted when no city siblings remain")
			} else {
				assert.NoError(t, err,
					"PropagationPolicy should be kept when other city siblings remain")
			}

			// The project WD should be gone: once the federator finalizer is removed
			// from an object that already has a DeletionTimestamp, the API server
			// (and the fake client) garbage-collects the object.
			var updatedWD computev1alpha.WorkloadDeployment
			err = projectClient.Get(ctx,
				types.NamespacedName{Name: testWDName, Namespace: testProjNS}, &updatedWD)
			assert.True(t, apierrors.IsNotFound(err),
				"project WD should be gone after finalizer removal (DeletionTimestamp + empty Finalizers = GC)")
		})
	}
}

// TestWorkloadDeploymentFederator_NotFound verifies that a missing
// WorkloadDeployment is handled gracefully (no error, no action).
func TestWorkloadDeploymentFederator_NotFound(t *testing.T) {
	t.Parallel()

	projectClient := newProjectFakeClient(testProjectNamespace()) // WD missing
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// TestWorkloadDeploymentFederator_Finalize_DirectCall exercises the Finalize
// method directly, ensuring the cluster name is required in context.
func TestWorkloadDeploymentFederator_Finalize_DirectCall(t *testing.T) {
	t.Parallel()

	projectClient := newProjectFakeClient(testProjectNamespace())
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	wd := testWorkloadDeployment(withFinalizer)

	// Without cluster in context → must return an error.
	_, err := r.Finalize(context.Background(), wd)
	require.Error(t, err, "Finalize without cluster context should fail")
	assert.Contains(t, err.Error(), "cluster name not found")

	// With cluster in context → must succeed (karmada client returns not-found, which is OK).
	ctx := mccontext.WithCluster(context.Background(), testCluster)
	result, err := r.Finalize(ctx, wd)
	require.NoError(t, err)
	assert.False(t, result.Updated)
}
