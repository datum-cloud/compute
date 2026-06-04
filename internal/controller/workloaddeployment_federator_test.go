// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
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
				Name: rdTestWorkloadName,
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
	encodedCluster := EncodeClusterName(testCluster)

	downstreamNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testKarmadaNSStr,
			Labels: map[string]string{
				downstreamclient.UpstreamOwnerClusterNameLabel: encodedCluster,
			},
		},
	}

	// A downstream namespace whose cluster label decodes to a project cluster the
	// manager has not engaged — used to verify the not-engaged drop path.
	unknownClusterNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testKarmadaNSStr,
			Labels: map[string]string{
				downstreamclient.UpstreamOwnerClusterNameLabel: "cluster-unregistered-project",
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
		{
			name:        "project cluster not engaged is dropped",
			karmadaObjs: []client.Object{unknownClusterNS},
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
				// Only testCluster is engaged; the not-engaged case decodes to a
				// different project name and must be dropped by the GetCluster guard.
				mgr:               newFakeMCManager(testCluster, newFakeCluster(karmadaClient)),
				FederationClient:  karmadaClient,
				FederationCluster: newFakeCluster(karmadaClient),
			}

			got := r.mapDownstreamDeploymentToRequest(context.Background(), tt.downstreamWD)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestProjectClusterNameFromLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		encoded string
		want    string
	}{
		{"cluster-datum-cloud", "datum-cloud"},
		// Org-scoped encodings decode to org/project; the provider keys on the
		// bare project name, so only the final path segment is returned.
		{"cluster-org_project", "project"},
		{"cluster-_test-project-abc", "test-project-abc"},
		{"cluster-test-project-cluster", "test-project-cluster"},
	}
	for _, tt := range tests {
		t.Run(tt.encoded, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, projectClusterNameFromLabel(tt.encoded))
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

// TestWorkloadDeploymentFederator_EmptyClusterNameDropped verifies that a
// reconcile request carrying an empty cluster name is dropped without error
// (and without touching GetCluster), so it can never fall back to the local
// host cluster and spin in a "no matches for kind" requeue loop.
func TestWorkloadDeploymentFederator_EmptyClusterNameDropped(t *testing.T) {
	t.Parallel()

	projectClient := newProjectFakeClient(testProjectNamespace(), testWorkloadDeployment())
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	req := mcreconcile.Request{
		ClusterName: "",
		Request: ctrl.Request{
			NamespacedName: types.NamespacedName{Name: testWDName, Namespace: testProjNS},
		},
	}
	result, err := r.Reconcile(context.Background(), req)
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

	// The PP must have three selectors: WorkloadDeployment (city-code), ConfigMap
	// (referenced-data), and Secret (referenced-data).
	require.Len(t, pp.Spec.ResourceSelectors, 3)

	wdSel := pp.Spec.ResourceSelectors[0]
	assert.Equal(t, computev1alpha.GroupVersion.String(), wdSel.APIVersion)
	assert.Equal(t, kindWorkloadDeployment, wdSel.Kind)
	require.NotNil(t, wdSel.LabelSelector)
	assert.Equal(t, testCityCodeLAX, wdSel.LabelSelector.MatchLabels[cityCodeLabel])

	cmSel := pp.Spec.ResourceSelectors[1]
	assert.Equal(t, "v1", cmSel.APIVersion)
	assert.Equal(t, kindConfigMap, cmSel.Kind)
	require.NotNil(t, cmSel.LabelSelector)
	assert.Equal(t, computev1alpha.ReferencedDataLabelValue, cmSel.LabelSelector.MatchLabels[computev1alpha.ReferencedDataLabel])

	secretSel := pp.Spec.ResourceSelectors[2]
	assert.Equal(t, "v1", secretSel.APIVersion)
	assert.Equal(t, kindSecret, secretSel.Kind)
	require.NotNil(t, secretSel.LabelSelector)
	assert.Equal(t, computev1alpha.ReferencedDataLabelValue, secretSel.LabelSelector.MatchLabels[computev1alpha.ReferencedDataLabel])

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
					WorkloadRef:   computev1alpha.WorkloadReference{Name: rdTestWorkloadName},
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

// TestCleanupPropagationPolicyIfUnused_EmptyCityCode verifies the guard
// against listing with an empty city-code label value, which would match the
// wrong deployment set and mis-decide PropagationPolicy cleanup.
func TestCleanupPropagationPolicyIfUnused_EmptyCityCode(t *testing.T) {
	t.Parallel()

	projectClient := newProjectFakeClient(testProjectNamespace())
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	err := r.cleanupPropagationPolicyIfUnused(context.Background(), testKarmadaNSStr, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "city code is empty")
}

// TestWorkloadDeploymentFederator_PropagationPolicyHasReferencedDataSelectors
// verifies that the PropagationPolicy always includes ConfigMap and Secret
// selectors for the referenced-data label in addition to the WorkloadDeployment
// city-code selector. This is the always-on companion co-propagation.
func TestWorkloadDeploymentFederator_PropagationPolicyHasReferencedDataSelectors(t *testing.T) {
	t.Parallel()

	wd := testWorkloadDeployment(withFinalizer)
	projectClient := newProjectFakeClient(testProjectNamespace(), wd)
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)

	ppName := propagationPolicyNameFor(testCityCodeLAX)
	var pp karmadapolicyv1alpha1.PropagationPolicy
	require.NoError(t, karmadaClient.Get(context.Background(), types.NamespacedName{
		Name:      ppName,
		Namespace: testKarmadaNSStr,
	}, &pp))

	require.Len(t, pp.Spec.ResourceSelectors, 3, "PP must have WD + ConfigMap + Secret selectors")

	kinds := make(map[string]bool)
	for _, sel := range pp.Spec.ResourceSelectors {
		kinds[sel.Kind] = true
	}
	assert.True(t, kinds[kindWorkloadDeployment], "PP must select WorkloadDeployments")
	assert.True(t, kinds[kindConfigMap], "PP must select ConfigMaps with referenced-data label")
	assert.True(t, kinds[kindSecret], "PP must select Secrets with referenced-data label")

	// Verify the ConfigMap and Secret selectors match on the referenced-data label.
	for _, sel := range pp.Spec.ResourceSelectors {
		if sel.Kind == kindConfigMap || sel.Kind == kindSecret {
			require.NotNil(t, sel.LabelSelector)
			assert.Equal(t, computev1alpha.ReferencedDataLabelValue, sel.LabelSelector.MatchLabels[computev1alpha.ReferencedDataLabel],
				"%s selector must match referenced-data=true label", sel.Kind)
		}
	}
}

// TestWorkloadDeploymentFederator_AnnotationPropagation verifies that the
// federator mirrors the expected-referenced-data annotation from the project WD
// to the downstream (Karmada hub) WD in both directions: copied while present
// so the cell can gate-clear, and deleted once the resolver removes it (the
// cell gate reads absence as "resolver hasn't run", so a stale downstream copy
// would gate new instances forever on companions that no longer exist).
func TestWorkloadDeploymentFederator_AnnotationPropagation(t *testing.T) {
	t.Parallel()

	const expectedAnno = `["ConfigMap/app-config","Secret/db-creds"]`

	wd := testWorkloadDeployment(withFinalizer, func(w *computev1alpha.WorkloadDeployment) {
		w.Annotations = map[string]string{
			computev1alpha.ExpectedReferencedDataAnnotation: expectedAnno,
		}
	})
	projectClient := newProjectFakeClient(testProjectNamespace(), wd)
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	ctx := context.Background()
	_, err := r.Reconcile(ctx, reconcileRequest())
	require.NoError(t, err)

	karmadaWDKey := types.NamespacedName{
		Name:      testWDName,
		Namespace: testKarmadaNSStr,
	}
	var karmadaWD computev1alpha.WorkloadDeployment
	require.NoError(t, karmadaClient.Get(ctx, karmadaWDKey, &karmadaWD))

	got := karmadaWD.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]
	assert.Equal(t, expectedAnno, got,
		"federator must propagate expected-referenced-data annotation to the downstream WD")

	// The resolver deletes the annotation from the project WD when the template
	// drops all references. The next upsert must delete the downstream copy too.
	var projectWD computev1alpha.WorkloadDeployment
	require.NoError(t, projectClient.Get(ctx, types.NamespacedName{
		Name:      testWDName,
		Namespace: testProjNS,
	}, &projectWD))
	delete(projectWD.Annotations, computev1alpha.ExpectedReferencedDataAnnotation)
	require.NoError(t, projectClient.Update(ctx, &projectWD))

	_, err = r.Reconcile(ctx, reconcileRequest())
	require.NoError(t, err)

	karmadaWD = computev1alpha.WorkloadDeployment{}
	require.NoError(t, karmadaClient.Get(ctx, karmadaWDKey, &karmadaWD))
	_, stale := karmadaWD.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]
	assert.False(t, stale,
		"federator must delete the downstream annotation once the project WD no longer carries it")
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
