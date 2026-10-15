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
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

// ─── Shared test constants ────────────────────────────────────────────────────

const (
	testCluster           = "test-project-cluster"
	testProjNS            = "my-project"
	testProjNSUID         = types.UID("aabbccdd-0000-1111-2222-333344445555")
	testKarmadaNSStr      = "ns-aabbccdd-0000-1111-2222-333344445555"
	testWDName            = "my-workload-deployment"
	testFederatorLocation = "us-west-2"
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
			LocationRef: locationsv1alpha1.LocationReference{Name: testFederatorLocation},
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
		name         string
		location     string
		runtimeClass string
		want         string
	}{
		{"LAX", "LAX", "", "location-lax"},
		{"lax", "lax", "", "location-lax"},
		{"New York", "New York", "", "location-new-york"},
		{"LOS ANGELES", "LOS ANGELES", "", "location-los-angeles"},
		{"SEA", "SEA", "", "location-sea"},
		{"us-west-2", testFederatorLocation, "", "location-us-west-2"},
		{"us-west-2 with a class", testFederatorLocation, testClassAzurite, "location-us-west-2-class-azurite"},
		{"us-west-2 with another class", testFederatorLocation, testClassBasalt, "location-us-west-2-class-basalt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := propagationPolicyNameFor(tt.location, tt.runtimeClass)
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
// the Karmada namespace, WorkloadDeployment (with location label), and
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

	// Karmada WorkloadDeployment must exist with the location label.
	var karmadaWD computev1alpha.WorkloadDeployment
	err = karmadaClient.Get(ctx, types.NamespacedName{
		Name:      testWDName,
		Namespace: testKarmadaNSStr,
	}, &karmadaWD)
	require.NoError(t, err, "Karmada WorkloadDeployment should exist")
	assert.Equal(t, testFederatorLocation, karmadaWD.Labels[locationLabel],
		"location label should be set on Karmada WD")
	assert.Equal(t, testFederatorLocation, karmadaWD.Spec.LocationRef.Name,
		"spec.locationRef should be copied from project WD")

	// PropagationPolicy for the location must exist.
	ppName := propagationPolicyNameFor(testFederatorLocation, "")
	var pp karmadapolicyv1alpha1.PropagationPolicy
	err = karmadaClient.Get(ctx, types.NamespacedName{
		Name:      ppName,
		Namespace: testKarmadaNSStr,
	}, &pp)
	require.NoError(t, err, "PropagationPolicy %q should exist", ppName)

	// The PP selects the WorkloadDeployment and nothing else. Companions travel
	// as its dependencies.
	require.Len(t, pp.Spec.ResourceSelectors, 1)
	assert.True(t, pp.Spec.PropagateDeps, "the PP must ask the engine to carry the deployment's dependencies")

	wdSel := pp.Spec.ResourceSelectors[0]
	assert.Equal(t, computev1alpha.GroupVersion.String(), wdSel.APIVersion)
	assert.Equal(t, kindWorkloadDeployment, wdSel.Kind)
	require.NotNil(t, wdSel.LabelSelector)
	assert.Equal(t, testFederatorLocation, wdSel.LabelSelector.MatchLabels[locationLabel])

	// The PP cluster affinity must target clusters carrying the same location.
	require.NotNil(t, pp.Spec.Placement.ClusterAffinity)
	require.NotNil(t, pp.Spec.Placement.ClusterAffinity.LabelSelector)
	assert.Equal(t, testFederatorLocation,
		pp.Spec.Placement.ClusterAffinity.LabelSelector.MatchLabels[locationLabel])
}

// TestWorkloadDeploymentFederator_Finalization covers the deletion scenarios:
// cleanup of Karmada resources and conditional PropagationPolicy removal.
func TestWorkloadDeploymentFederator_Finalization(t *testing.T) {
	t.Parallel()

	ppName := propagationPolicyNameFor(testFederatorLocation, "")

	tests := []struct {
		name string
		// karmadaExtra holds additional Karmada objects beyond the "own" WD and PP.
		karmadaExtra []client.Object
		wantPPGone   bool
	}{
		{
			name:         "last WD for location — PropagationPolicy removed",
			karmadaExtra: nil,
			wantPPGone:   true,
		},
		{
			name: "other WD for same location remains — PropagationPolicy kept",
			karmadaExtra: []client.Object{
				// A sibling WD in the same Karmada namespace with the same location.
				&computev1alpha.WorkloadDeployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "other-deployment",
						Namespace: testKarmadaNSStr,
						Labels:    map[string]string{locationLabel: testFederatorLocation},
					},
					Spec: computev1alpha.WorkloadDeploymentSpec{
						LocationRef:   locationsv1alpha1.LocationReference{Name: testFederatorLocation},
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
					Labels:    map[string]string{locationLabel: testFederatorLocation},
				},
				Spec: computev1alpha.WorkloadDeploymentSpec{
					LocationRef:   locationsv1alpha1.LocationReference{Name: testFederatorLocation},
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

// TestCleanupPropagationPolicyIfUnused_EmptyLocation verifies the guard
// against listing with an empty location label value, which would match the
// wrong deployment set and mis-decide PropagationPolicy cleanup.
func TestCleanupPropagationPolicyIfUnused_EmptyLocation(t *testing.T) {
	t.Parallel()

	projectClient := newProjectFakeClient(testProjectNamespace())
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	err := r.cleanupPropagationPolicyIfUnused(context.Background(), testKarmadaNSStr, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "location name is empty")
}

// TestWorkloadDeploymentFederator_PropagationPolicyDelegatesCompanionDelivery
// verifies the PropagationPolicy names no ConfigMap or Secret and instead sets
// PropagateDeps.
//
// A companion is shared by every deployment in the namespace that references
// the same source, so naming it on each location's policy hands it to whichever
// policy claims it first and starves every other location. Delegating to the
// engine unions the destinations instead.
func TestWorkloadDeploymentFederator_PropagationPolicyDelegatesCompanionDelivery(t *testing.T) {
	t.Parallel()

	wd := testWorkloadDeployment(withFinalizer)
	projectClient := newProjectFakeClient(testProjectNamespace(), wd)
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)

	ppName := propagationPolicyNameFor(testFederatorLocation, "")
	var pp karmadapolicyv1alpha1.PropagationPolicy
	require.NoError(t, karmadaClient.Get(context.Background(), types.NamespacedName{
		Name:      ppName,
		Namespace: testKarmadaNSStr,
	}, &pp))

	assert.True(t, pp.Spec.PropagateDeps,
		"without PropagateDeps the engine never reads the deployment's dependencies and no companion is delivered")

	// An attached dependency binding inherits the parent binding's conflict
	// resolution, so this is the only place companion delivery can be told to
	// take over a stray same-named object on a cell. The default, Abort, would
	// strand delivery as AppliedFailed on a hub object the customer cannot read.
	assert.Equal(t, karmadapolicyv1alpha1.ConflictOverwrite, pp.Spec.ConflictResolution,
		"compute owns what this policy delivers and the namespace it lands in, so a name collision must not stop propagation")

	require.Len(t, pp.Spec.ResourceSelectors, 1, "PP must select the WorkloadDeployment only")
	assert.Equal(t, kindWorkloadDeployment, pp.Spec.ResourceSelectors[0].Kind)

	for _, sel := range pp.Spec.ResourceSelectors {
		assert.NotEqual(t, kindConfigMap, sel.Kind,
			"a ConfigMap selector reintroduces exclusive claiming of shared companions")
		assert.NotEqual(t, kindSecret, sel.Kind,
			"a Secret selector reintroduces exclusive claiming of shared companions")
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

// TestWorkloadDeploymentFederator_RecordsFederationNamespace asserts the hub
// namespace is stamped on the project object during federation. That record is
// what makes finalization self-contained: the object being finalized carries
// everything needed to remove its hub copy.
func TestWorkloadDeploymentFederator_RecordsFederationNamespace(t *testing.T) {
	t.Parallel()

	wd := testWorkloadDeployment(withFinalizer)
	projectClient := newProjectFakeClient(testProjectNamespace(), wd)
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)

	var federated computev1alpha.WorkloadDeployment
	require.NoError(t, projectClient.Get(context.Background(), types.NamespacedName{
		Namespace: testProjNS, Name: testWDName,
	}, &federated))
	assert.Equal(t, testKarmadaNSStr, federated.Annotations[computev1alpha.FederationNamespaceAnnotation])
}

// TestWorkloadDeploymentFederator_FinalizeIsSelfContained asserts finalization
// removes the hub deployment using only the record on the object itself.
//
// Nothing on the hub owns the hub deployment, so this finalizer is the only
// thing that can remove it. The project namespace is left out of the fixture to
// prove the path no longer depends on it.
func TestWorkloadDeploymentFederator_FinalizeIsSelfContained(t *testing.T) {
	t.Parallel()

	wd := testWorkloadDeployment(withFinalizer, withDeletionTimestamp)
	wd.Annotations = map[string]string{
		computev1alpha.FederationNamespaceAnnotation: testKarmadaNSStr,
	}

	hubWD := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWDName,
			Namespace: testKarmadaNSStr,
			Labels:    map[string]string{locationLabel: testFederatorLocation},
		},
		Spec: wd.Spec,
	}

	// The project namespace is deliberately absent: live resolution would fail.
	projectClient := newProjectFakeClient(wd)
	karmadaClient := newKarmadaFakeClient(hubWD)
	r := newTestFederator(projectClient, karmadaClient)

	ctx := mccontext.WithCluster(context.Background(), testCluster)
	result, err := r.Finalize(ctx, wd)
	require.NoError(t, err, "finalization must not depend on the project namespace")
	assert.False(t, result.Updated)

	var gone computev1alpha.WorkloadDeployment
	err = karmadaClient.Get(ctx, types.NamespacedName{Namespace: testKarmadaNSStr, Name: testWDName}, &gone)
	assert.True(t, apierrors.IsNotFound(err), "the hub deployment must be removed")
}

// TestWorkloadDeploymentFederator_FinalizeHoldsWhenUnresolvable asserts the
// finalizer is held, not released, when neither the record nor live resolution
// can name the hub namespace. Releasing here would strand the hub deployment.
func TestWorkloadDeploymentFederator_FinalizeHoldsWhenUnresolvable(t *testing.T) {
	t.Parallel()

	wd := testWorkloadDeployment(withFinalizer, withDeletionTimestamp)
	projectClient := newProjectFakeClient(wd)
	karmadaClient := newKarmadaFakeClient()
	r := newTestFederator(projectClient, karmadaClient)

	ctx := mccontext.WithCluster(context.Background(), testCluster)
	_, err := r.Finalize(ctx, wd)
	require.Error(t, err, "an unresolvable hub namespace must hold the finalizer")
}

// TestWorkloadDeploymentFederator_FinalizesLegacyDeployment covers deleting a
// deployment stored before placement moved to locations. It has no location
// to key a PropagationPolicy by; the city it was routed by is read off its
// hub copy, and the city-keyed policies from that era are removed once no hub
// deployment routed by the city remains. Policies keyed by location are not
// touched.
func TestWorkloadDeploymentFederator_FinalizesLegacyDeployment(t *testing.T) {
	t.Parallel()

	const legacyCity = "LAX"
	withoutLocation := func(wd *computev1alpha.WorkloadDeployment) {
		wd.Spec.LocationRef = locationsv1alpha1.LocationReference{}
	}

	tests := []struct {
		name          string
		siblingLabels map[string]string
		wantCityPPs   bool
	}{
		{
			name:        "last legacy deployment for the city removes its policies",
			wantCityPPs: false,
		},
		{
			name:          "another legacy deployment routed by the city keeps them",
			siblingLabels: map[string]string{networkingv1alpha.TopologyCityCodeKey: legacyCity},
			wantCityPPs:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wd := testWorkloadDeployment(withFinalizer, withDeletionTimestamp, withoutLocation)
			projectClient := newProjectFakeClient(testProjectNamespace(), wd)

			hubCopy := &computev1alpha.WorkloadDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testWDName,
					Namespace: testKarmadaNSStr,
					Labels:    map[string]string{networkingv1alpha.TopologyCityCodeKey: legacyCity},
				},
			}
			policy := func(name string) *karmadapolicyv1alpha1.PropagationPolicy {
				return &karmadapolicyv1alpha1.PropagationPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testKarmadaNSStr}}
			}
			karmadaObjs := []client.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testKarmadaNSStr}},
				hubCopy,
				policy("city-lax"),
				policy("city-lax-class-basalt"),
				policy("city-sea"),
				policy(propagationPolicyNameFor(testFederatorLocation, "")),
			}
			if tt.siblingLabels != nil {
				karmadaObjs = append(karmadaObjs, &computev1alpha.WorkloadDeployment{
					ObjectMeta: metav1.ObjectMeta{Name: "legacy-sibling", Namespace: testKarmadaNSStr, Labels: tt.siblingLabels},
				})
			}
			karmadaClient := newKarmadaFakeClient(karmadaObjs...)

			r := newTestFederator(projectClient, karmadaClient)
			_, err := r.Reconcile(context.Background(), reconcileRequest())
			require.NoError(t, err, "a deployment without a location must still finalize")

			ctx := context.Background()
			var gone computev1alpha.WorkloadDeployment
			err = karmadaClient.Get(ctx, types.NamespacedName{Name: testWDName, Namespace: testKarmadaNSStr}, &gone)
			assert.True(t, apierrors.IsNotFound(err), "the hub copy is deleted")

			exists := func(name string) bool {
				var pp karmadapolicyv1alpha1.PropagationPolicy
				return karmadaClient.Get(ctx, types.NamespacedName{Name: name, Namespace: testKarmadaNSStr}, &pp) == nil
			}
			assert.Equal(t, tt.wantCityPPs, exists("city-lax"))
			assert.Equal(t, tt.wantCityPPs, exists("city-lax-class-basalt"))
			assert.True(t, exists("city-sea"), "another city's policies are not touched")
			assert.True(t, exists(propagationPolicyNameFor(testFederatorLocation, "")), "location-keyed policies are not touched")
		})
	}
}
