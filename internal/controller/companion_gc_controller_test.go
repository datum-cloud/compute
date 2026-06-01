// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
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
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// ─── Test constants ────────────────────────────────────────────────────────────

const (
	gcTestCluster = "cell-cluster"

	// gcTestNamespace is the cell-side namespace (ns-{project-uid}) where
	// companion objects and the cell WorkloadDeployment reside.
	gcTestNamespace = "ns-aabbccdd-0000-1111-2222-333344445555"

	// gcTestProjectNamespace is the project-plane namespace that the
	// ReferencedDataController uses when writing referenced-by annotation keys.
	// In production keys are written as "projectNamespace/wdName", e.g.
	// "default/mount-pristine-default-dfw".
	gcTestProjectNamespace = "default"

	gcTestCMName  = "cm-pristine"
	gcTestSecName = "secret-pristine"
	gcTestWD1Name = "mount-pristine-default-dfw"
	gcTestWD2Name = "mount-pristine-default-lax"

	// gcTestDefaultWD1Key is a sample production-format referenced-by annotation
	// key used in decode and name-extraction tests ("projectNamespace/wdName").
	gcTestDefaultWD1Key = "default/wd-1"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// newCellFakeClient returns a fake client with the project scheme (corev1 + compute).
func newCellFakeClient(objs ...client.Object) client.Client {
	return newProjectFakeClient(objs...)
}

// companionCM builds a companion ConfigMap named gcTestCMName in gcTestNamespace
// with the referenced-data=true label and the given WD keys in the
// referenced-by annotation.
func companionCM(wdKeys []string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gcTestCMName,
			Namespace: gcTestNamespace,
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
			Annotations: map[string]string{
				companionRefCountAnnotation: mustEncodeRefCount(wdKeys),
			},
		},
	}
}

// companionSecret builds a companion Secret with the referenced-data=true label
// and the given WD keys in the referenced-by annotation.
func companionSecret(name, namespace string, wdKeys []string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
			Annotations: map[string]string{
				companionRefCountAnnotation: mustEncodeRefCount(wdKeys),
			},
		},
	}
}

// plainCM builds a ConfigMap WITHOUT the referenced-data label.
func plainCM(name, namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
}

// cellWD builds a WorkloadDeployment in the cell namespace (ns-{uid}).
func cellWD(name, namespace string) *computev1alpha.WorkloadDeployment {
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode:      wbTestCityCode,
			PlacementName: testDefaultPlacement,
			WorkloadRef:   computev1alpha.WorkloadReference{Name: "workload"},
			ScaleSettings: computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
		},
	}
}

// cellWDTerminating builds a WorkloadDeployment with a non-zero DeletionTimestamp.
func cellWDTerminating(name, namespace string) *computev1alpha.WorkloadDeployment {
	wd := cellWD(name, namespace)
	ts := metav1.NewTime(time.Now().Add(-5 * time.Second))
	wd.DeletionTimestamp = &ts
	wd.Finalizers = []string{"test-finalizer"} // required by fake client for terminating objects
	return wd
}

// mustEncodeRefCount serialises a slice of WD keys as a JSON array.
func mustEncodeRefCount(keys []string) string {
	if len(keys) == 0 {
		return "[]"
	}
	b, err := json.Marshal(keys)
	if err != nil {
		panic("mustEncodeRefCount: " + err.Error())
	}
	return string(b)
}

// prodRefKey returns the production-format referenced-by annotation key for a WD.
// In production the ReferencedDataController writes "projectNamespace/wdName"
// (e.g. "default/mount-pristine-default-dfw"), NOT the cell namespace.
func prodRefKey(wdName string) string {
	return gcTestProjectNamespace + "/" + wdName
}

// newGCReconciler builds a CompanionGCReconciler wired to a fakeMCManager
// backed by the given cell client.
func newGCReconciler(cellClient client.Client) *CompanionGCReconciler {
	cl := newFakeCluster(cellClient)
	mgr := newFakeMCManager(gcTestCluster, cl)
	return &CompanionGCReconciler{mgr: mgr}
}

// reconcileGC runs one GC reconcile for the named object in gcTestNamespace.
func reconcileGC(t *testing.T, r *CompanionGCReconciler, name string) (ctrl.Result, error) {
	t.Helper()
	ctx := mccontext.WithCluster(context.Background(), multicluster.ClusterName(gcTestCluster))
	return r.Reconcile(ctx, mcreconcile.Request{
		ClusterName: multicluster.ClusterName(gcTestCluster),
		Request: ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: gcTestNamespace, Name: name},
		},
	})
}

// ─── decodeCompanionRefCount unit tests ───────────────────────────────────────

func TestDecodeCompanionRefCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		annotations map[string]string
		wantKeys    []string
		wantErr     bool
	}{
		{
			name:        "absent annotation returns nil",
			annotations: nil,
			wantKeys:    nil,
		},
		{
			name:        "empty annotation returns nil",
			annotations: map[string]string{companionRefCountAnnotation: ""},
			wantKeys:    nil,
		},
		{
			name:        "single key",
			annotations: map[string]string{companionRefCountAnnotation: `["` + gcTestDefaultWD1Key + `"]`},
			wantKeys:    []string{gcTestDefaultWD1Key},
		},
		{
			name:        "multiple keys",
			annotations: map[string]string{companionRefCountAnnotation: `["` + gcTestDefaultWD1Key + `","default/wd-2"]`},
			wantKeys:    []string{gcTestDefaultWD1Key, "default/wd-2"},
		},
		{
			name:        "corrupt annotation returns error",
			annotations: map[string]string{companionRefCountAnnotation: `not-json`},
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeCompanionRefCount(tt.annotations)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKeys, got)
		})
	}
}

// ─── wdNameFromKey unit tests ─────────────────────────────────────────────────

func TestWdNameFromKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{gcTestDefaultWD1Key, "wd-1"},
		{"ns/sub/name", "name"}, // last slash wins
		{"wd-only", "wd-only"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := wdNameFromKey(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ─── hasLiveReferrer unit tests ───────────────────────────────────────────────

func TestHasLiveReferrer(t *testing.T) {
	t.Parallel()

	// Production-format key: project namespace / WD name.
	// The WD object lives in gcTestNamespace on the cell.
	wdKey := prodRefKey(gcTestWD1Name)

	tests := []struct {
		name        string
		annotations map[string]string
		cellObjs    []client.Object
		wantAlive   bool
		wantErr     bool
	}{
		{
			name:        "no annotation → no referrers → safe to delete",
			annotations: nil,
			wantAlive:   false,
		},
		{
			name:        "empty ref-count → safe to delete",
			annotations: map[string]string{companionRefCountAnnotation: "[]"},
			wantAlive:   false,
		},
		{
			// BLOCKER 1 regression: key uses project namespace ("default/…") but
			// the WD lives in the cell namespace (gcTestNamespace). hasLiveReferrer
			// must look up by name only in the companion's namespace, not by the
			// project namespace encoded in the key.
			name:        "project-namespace key — WD in cell ns — companion preserved",
			annotations: map[string]string{companionRefCountAnnotation: mustEncodeRefCount([]string{wdKey})},
			cellObjs:    []client.Object{cellWD(gcTestWD1Name, gcTestNamespace)},
			wantAlive:   true,
		},
		{
			name:        "referrer is terminating → still counts as present → keep",
			annotations: map[string]string{companionRefCountAnnotation: mustEncodeRefCount([]string{wdKey})},
			cellObjs:    []client.Object{cellWDTerminating(gcTestWD1Name, gcTestNamespace)},
			wantAlive:   true,
		},
		{
			name:        "referrer absent → delete",
			annotations: map[string]string{companionRefCountAnnotation: mustEncodeRefCount([]string{wdKey})},
			cellObjs:    nil, // WD not on this cell
			wantAlive:   false,
		},
		{
			// Multi-referrer with production-format keys: WD1 is on another cell
			// (not present locally), WD2 IS on this cell → companion kept.
			name: "multi-referrer: one absent (other cell), one present local → keep",
			annotations: map[string]string{
				companionRefCountAnnotation: mustEncodeRefCount([]string{
					prodRefKey(gcTestWD1Name),
					prodRefKey(gcTestWD2Name),
				}),
			},
			// WD2 is on this cell; WD1 is on another cell and not visible here.
			cellObjs:  []client.Object{cellWD(gcTestWD2Name, gcTestNamespace)},
			wantAlive: true,
		},
		{
			name: "multi-referrer: both absent → delete",
			annotations: map[string]string{
				companionRefCountAnnotation: mustEncodeRefCount([]string{
					prodRefKey(gcTestWD1Name),
					prodRefKey(gcTestWD2Name),
				}),
			},
			cellObjs:  nil,
			wantAlive: false,
		},
		{
			name:        "corrupt annotation → error returned",
			annotations: map[string]string{companionRefCountAnnotation: "corrupt"},
			wantErr:     true,
			wantAlive:   true, // conservative: treat as alive on error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cellClient := newCellFakeClient(tt.cellObjs...)
			r := &CompanionGCReconciler{}

			alive, err := r.hasLiveReferrer(context.Background(), cellClient, gcTestNamespace, tt.annotations)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantAlive, alive)
		})
	}
}

// ─── CompanionGCReconciler.Reconcile tests ────────────────────────────────────

// TestCompanionGC_ConfigMap_DeletedWhenAllReferrersAbsent verifies that a
// companion ConfigMap is deleted when the WD listed in its referenced-by
// annotation (using production "projectNS/name" format) is absent from the cell.
func TestCompanionGC_ConfigMap_DeletedWhenAllReferrersAbsent(t *testing.T) {
	t.Parallel()

	// Production-format key: project namespace, but no WD of that name on this cell.
	cm := companionCM([]string{prodRefKey(gcTestWD1Name)})
	cellClient := newCellFakeClient(cm)
	r := newGCReconciler(cellClient)

	result, err := reconcileGC(t, r, gcTestCMName)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	var got corev1.ConfigMap
	err = cellClient.Get(context.Background(), types.NamespacedName{Namespace: gcTestNamespace, Name: gcTestCMName}, &got)
	assert.True(t, apierrors.IsNotFound(err), "companion ConfigMap should be deleted when all referrers are absent")
}

// TestCompanionGC_Secret_DeletedWhenAllReferrersAbsent verifies that a companion
// Secret is deleted when the WD listed in its annotation does not exist on the cell.
func TestCompanionGC_Secret_DeletedWhenAllReferrersAbsent(t *testing.T) {
	t.Parallel()

	secret := companionSecret(gcTestSecName, gcTestNamespace, []string{prodRefKey(gcTestWD1Name)})
	cellClient := newCellFakeClient(secret)
	r := newGCReconciler(cellClient)

	result, err := reconcileGC(t, r, gcTestSecName)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	var got corev1.Secret
	err = cellClient.Get(context.Background(), types.NamespacedName{Namespace: gcTestNamespace, Name: gcTestSecName}, &got)
	assert.True(t, apierrors.IsNotFound(err), "companion Secret should be deleted when all referrers are absent")
}

// TestCompanionGC_ProjectNamespaceKey_PreservesCompanionWithLiveLocalWD is the
// Blocker 1 regression test. The referenced-by annotation key is written by the
// hub ReferencedDataController as "projectNS/wdName" (e.g.
// "default/mount-pristine-default-dfw"). The cell WD lives in ns-{uid}, NOT in
// "default". The GC reconciler must find the WD by name in the companion's own
// namespace — using the project namespace from the key would always NotFound and
// incorrectly delete a companion that is actively in use.
func TestCompanionGC_ProjectNamespaceKey_PreservesCompanionWithLiveLocalWD(t *testing.T) {
	t.Parallel()

	// Annotation uses project namespace ("default"), exactly as production writes it.
	wdKey := prodRefKey(gcTestWD1Name) // "default/mount-pristine-default-dfw"
	cm := companionCM([]string{wdKey})

	// The WD lives in the CELL namespace (ns-{uid}), not "default".
	wd := cellWD(gcTestWD1Name, gcTestNamespace)
	cellClient := newCellFakeClient(cm, wd)
	r := newGCReconciler(cellClient)

	_, err := reconcileGC(t, r, gcTestCMName)
	require.NoError(t, err)

	// Companion must NOT be deleted — the WD exists on this cell.
	var got corev1.ConfigMap
	require.NoError(t, cellClient.Get(context.Background(),
		types.NamespacedName{Namespace: gcTestNamespace, Name: gcTestCMName}, &got),
		"companion must be preserved when WD with matching name exists in cell namespace, even though annotation key uses project namespace")
}

// TestCompanionGC_PreservedWhenLiveReferrerExists verifies that a companion is
// NOT deleted when a live (non-terminating) WD is present on the cell.
func TestCompanionGC_PreservedWhenLiveReferrerExists(t *testing.T) {
	t.Parallel()

	cm := companionCM([]string{prodRefKey(gcTestWD1Name)})
	wd := cellWD(gcTestWD1Name, gcTestNamespace)
	cellClient := newCellFakeClient(cm, wd)
	r := newGCReconciler(cellClient)

	_, err := reconcileGC(t, r, gcTestCMName)
	require.NoError(t, err)

	var got corev1.ConfigMap
	require.NoError(t, cellClient.Get(context.Background(),
		types.NamespacedName{Namespace: gcTestNamespace, Name: gcTestCMName}, &got))
}

// TestCompanionGC_PreservedWhenTerminatingReferrerExists verifies that a companion
// is NOT deleted when the only referrer on this cell is terminating. Terminating
// WDs still count as present — we are conservative to avoid premature deletion
// during the WD teardown window.
func TestCompanionGC_PreservedWhenTerminatingReferrerExists(t *testing.T) {
	t.Parallel()

	cm := companionCM([]string{prodRefKey(gcTestWD1Name)})
	wd := cellWDTerminating(gcTestWD1Name, gcTestNamespace)
	cellClient := newCellFakeClient(cm, wd)
	r := newGCReconciler(cellClient)

	_, err := reconcileGC(t, r, gcTestCMName)
	require.NoError(t, err)

	var got corev1.ConfigMap
	require.NoError(t, cellClient.Get(context.Background(),
		types.NamespacedName{Namespace: gcTestNamespace, Name: gcTestCMName}, &got))
}

// TestCompanionGC_MultiReferrer_OneDifferentCell_OtherLiveLocal verifies the
// core per-cell multi-referrer safety property. The annotation (written by the
// hub) lists both WDs with project-namespace keys. WD1 is on another cell (not
// present locally). WD2 IS on this cell. Companion must be preserved.
func TestCompanionGC_MultiReferrer_OneDifferentCell_OtherLiveLocal(t *testing.T) {
	t.Parallel()

	// Both keys use production format (project namespace prefix).
	// WD1 is NOT present on this cell (simulating it running on another cell).
	// WD2 IS present on this cell.
	cm := companionCM([]string{
		prodRefKey(gcTestWD1Name),
		prodRefKey(gcTestWD2Name),
	})
	wd2 := cellWD(gcTestWD2Name, gcTestNamespace)
	cellClient := newCellFakeClient(cm, wd2)
	r := newGCReconciler(cellClient)

	_, err := reconcileGC(t, r, gcTestCMName)
	require.NoError(t, err)

	// Companion must NOT have been deleted because WD2 is still alive on this cell.
	var got corev1.ConfigMap
	require.NoError(t, cellClient.Get(context.Background(),
		types.NamespacedName{Namespace: gcTestNamespace, Name: gcTestCMName}, &got),
		"companion must survive when at least one local referrer is present")
}

// TestCompanionGC_MultiReferrer_AllAbsent_Deleted verifies that when BOTH WD
// names from the annotation are absent from this cell the companion is deleted.
func TestCompanionGC_MultiReferrer_AllAbsent_Deleted(t *testing.T) {
	t.Parallel()

	cm := companionCM([]string{
		prodRefKey(gcTestWD1Name),
		prodRefKey(gcTestWD2Name),
	})
	// Neither WD is on this cell.
	cellClient := newCellFakeClient(cm)
	r := newGCReconciler(cellClient)

	_, err := reconcileGC(t, r, gcTestCMName)
	require.NoError(t, err)

	var got corev1.ConfigMap
	err = cellClient.Get(context.Background(), types.NamespacedName{Namespace: gcTestNamespace, Name: gcTestCMName}, &got)
	assert.True(t, apierrors.IsNotFound(err), "companion must be deleted when all referrers are absent from this cell")
}

// TestCompanionGC_EmptyRefCount_Deleted verifies that a companion with an empty
// (or missing) ref-count annotation is deleted — zero referrers means safe to remove.
func TestCompanionGC_EmptyRefCount_Deleted(t *testing.T) {
	t.Parallel()

	cm := companionCM(nil) // empty ref-count
	cellClient := newCellFakeClient(cm)
	r := newGCReconciler(cellClient)

	_, err := reconcileGC(t, r, gcTestCMName)
	require.NoError(t, err)

	var got corev1.ConfigMap
	err = cellClient.Get(context.Background(), types.NamespacedName{Namespace: gcTestNamespace, Name: gcTestCMName}, &got)
	assert.True(t, apierrors.IsNotFound(err), "companion with no referrers should be deleted")
}

// TestCompanionGC_NonCompanionNotTouched verifies that a ConfigMap without the
// referenced-data=true label is not deleted by the GC reconciler.
func TestCompanionGC_NonCompanionNotTouched(t *testing.T) {
	t.Parallel()

	cm := plainCM(gcTestCMName, gcTestNamespace)
	cellClient := newCellFakeClient(cm)
	r := newGCReconciler(cellClient)

	_, err := reconcileGC(t, r, gcTestCMName)
	require.NoError(t, err)

	var got corev1.ConfigMap
	require.NoError(t, cellClient.Get(context.Background(),
		types.NamespacedName{Namespace: gcTestNamespace, Name: gcTestCMName}, &got),
		"ConfigMap without referenced-data label must not be deleted")
}

// TestCompanionGC_ObjectAlreadyGone verifies that reconciling a missing object
// is a no-op (no error).
func TestCompanionGC_ObjectAlreadyGone(t *testing.T) {
	t.Parallel()

	cellClient := newCellFakeClient() // nothing pre-loaded
	r := newGCReconciler(cellClient)

	_, err := reconcileGC(t, r, gcTestCMName)
	require.NoError(t, err)
}

// ─── Federator ordering guard tests ───────────────────────────────────────────

// TestFederator_Finalize_RequeuesWhenCompanionsPresent verifies that the
// Finalize method returns a RequeueAfter result (not an error) when companion
// ConfigMaps or Secrets still exist in the downstream namespace. The
// PropagationPolicy must not be deleted until all companions are gone.
func TestFederator_Finalize_RequeuesWhenCompanionsPresent(t *testing.T) {
	t.Parallel()

	ppName := propagationPolicyNameFor(testCityCodeLAX)

	tests := []struct {
		name     string
		extraObj client.Object // the companion that blocks PP deletion
	}{
		{
			name: "ConfigMap companion blocks PP deletion",
			extraObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cm-companion",
					Namespace: testKarmadaNSStr,
					Labels: map[string]string{
						computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
					},
				},
			},
		},
		{
			name: "Secret companion blocks PP deletion",
			extraObj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "secret-companion",
					Namespace: testKarmadaNSStr,
					Labels: map[string]string{
						computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wd := testWorkloadDeployment(withFinalizer, withDeletionTimestamp)
			projectClient := newProjectFakeClient(testProjectNamespace(), wd)

			karmadaWD := &computev1alpha.WorkloadDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testWDName,
					Namespace: testKarmadaNSStr,
				},
			}
			karmadaPP := &karmadapolicyv1alpha1.PropagationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ppName,
					Namespace: testKarmadaNSStr,
				},
			}
			karmadaClient := newKarmadaFakeClient(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testKarmadaNSStr}},
				karmadaWD,
				karmadaPP,
				tt.extraObj,
			)

			r := newTestFederator(projectClient, karmadaClient)
			result, err := r.Reconcile(context.Background(), reconcileRequest())

			// With a companion still present, Finalize must requeue gracefully —
			// no error (which would inflate error metrics), just a RequeueAfter.
			require.NoError(t, err, "Finalize should NOT return an error when companions are present; use RequeueAfter instead")
			assert.Equal(t, companionGuardRequeueAfter, result.RequeueAfter,
				"result should carry the companion-guard RequeueAfter delay")

			// PropagationPolicy must still be alive.
			var remainingPP karmadapolicyv1alpha1.PropagationPolicy
			require.NoError(t, karmadaClient.Get(context.Background(),
				types.NamespacedName{Name: ppName, Namespace: testKarmadaNSStr},
				&remainingPP),
				"PropagationPolicy must not be deleted while companions are still present")
		})
	}
}

// TestFederator_Finalize_DeletesPPWhenCompanionsGone verifies that once all
// companions are removed from the downstream namespace the PP is deleted as
// normal (ordering guard does not block a clean namespace).
func TestFederator_Finalize_DeletesPPWhenCompanionsGone(t *testing.T) {
	t.Parallel()

	ppName := propagationPolicyNameFor(testCityCodeLAX)
	wd := testWorkloadDeployment(withFinalizer, withDeletionTimestamp)
	projectClient := newProjectFakeClient(testProjectNamespace(), wd)

	karmadaWD := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWDName,
			Namespace: testKarmadaNSStr,
		},
	}
	karmadaPP := &karmadapolicyv1alpha1.PropagationPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ppName,
			Namespace: testKarmadaNSStr,
		},
	}
	// No companions in the namespace — ordering guard should pass.
	karmadaClient := newKarmadaFakeClient(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testKarmadaNSStr}},
		karmadaWD,
		karmadaPP,
	)

	r := newTestFederator(projectClient, karmadaClient)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// PP must be gone — no companions blocked the cleanup.
	var remainingPP karmadapolicyv1alpha1.PropagationPolicy
	err = karmadaClient.Get(context.Background(),
		types.NamespacedName{Name: ppName, Namespace: testKarmadaNSStr},
		&remainingPP)
	assert.True(t, apierrors.IsNotFound(err), "PropagationPolicy should be deleted when no companions remain")
}

// TestFederator_Finalize_DoesNotBlockOnSiblingWDCompanion is the Blocker 2
// regression test. The downstream namespace (ns-{uid}) is shared by ALL WDs in
// a project. When WD-A is deleted while WD-B's companion still exists in the
// same namespace, the ordering guard must NOT block WD-A's finalizer. Blocking
// would permanently deadlock WD-A's deletion because WD-B's companion is never
// going to be cleaned up by WD-A's referenced-data controller.
//
// The guard must only fire when the deleting WD is the LAST one for its city
// code (i.e. when the PP is actually about to be deleted).
func TestFederator_Finalize_DoesNotBlockOnSiblingWDCompanion(t *testing.T) {
	t.Parallel()

	ppName := propagationPolicyNameFor(testCityCodeLAX)

	// WD-A is the one being deleted (has finalizer + deletionTimestamp).
	wdA := testWorkloadDeployment(withFinalizer, withDeletionTimestamp)
	projectClient := newProjectFakeClient(testProjectNamespace(), wdA)

	// In the downstream namespace:
	//   - WD-A's mirrored object (just deleted above, but not yet GC'd from Karmada)
	//   - WD-B: a sibling WD with the SAME city code, still alive
	//   - A companion belonging to WD-B
	//   - The shared PropagationPolicy
	karmadaWDA := &computev1alpha.WorkloadDeployment{
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
	karmadaWDB := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wd-b",
			Namespace: testKarmadaNSStr,
			Labels:    map[string]string{cityCodeLabel: testCityCodeLAX},
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			CityCode:      testCityCodeLAX,
			PlacementName: testDefaultPlacement,
			WorkloadRef:   computev1alpha.WorkloadReference{Name: "workload-b"},
			ScaleSettings: computev1alpha.HorizontalScaleSettings{MinReplicas: 1},
		},
	}
	// WD-B's companion still lives in the shared namespace.
	wdBCompanion := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cm-b-companion",
			Namespace: testKarmadaNSStr,
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
		},
	}
	karmadaPP := &karmadapolicyv1alpha1.PropagationPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ppName,
			Namespace: testKarmadaNSStr,
		},
	}
	karmadaClient := newKarmadaFakeClient(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testKarmadaNSStr}},
		karmadaWDA,
		karmadaWDB,
		wdBCompanion,
		karmadaPP,
	)

	r := newTestFederator(projectClient, karmadaClient)
	result, err := r.Reconcile(context.Background(), reconcileRequest())

	// WD-A's finalizer must complete without error even though WD-B's companion
	// is still in the namespace. WD-B is still alive so the PP is kept anyway
	// (cleanupPropagationPolicyIfUnused no-ops). The companion guard must NOT
	// fire here.
	require.NoError(t, err, "WD-A finalization must not be blocked by WD-B's companion")
	assert.Equal(t, ctrl.Result{}, result)

	// The PropagationPolicy must still be alive (WD-B keeps it alive, not the guard).
	var remainingPP karmadapolicyv1alpha1.PropagationPolicy
	require.NoError(t, karmadaClient.Get(context.Background(),
		types.NamespacedName{Name: ppName, Namespace: testKarmadaNSStr},
		&remainingPP),
		"PropagationPolicy should be kept because WD-B still references the city code")
}

// TestFederator_Finalize_GuardBypassesAfterTimeout verifies that the companion
// ordering guard stops blocking when the WD has been terminating for longer than
// companionGuardTimeout. This prevents a permanently wedged referenced-data
// controller from deadlocking WD deletion.
func TestFederator_Finalize_GuardBypassesAfterTimeout(t *testing.T) {
	t.Parallel()

	ppName := propagationPolicyNameFor(testCityCodeLAX)

	// Set the DeletionTimestamp far enough in the past to exceed the guard timeout.
	pastDeletion := metav1.NewTime(time.Now().Add(-(companionGuardTimeout + time.Minute)))
	wd := testWorkloadDeployment(withFinalizer, func(w *computev1alpha.WorkloadDeployment) {
		w.DeletionTimestamp = &pastDeletion
	})
	projectClient := newProjectFakeClient(testProjectNamespace(), wd)

	karmadaWD := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: testWDName, Namespace: testKarmadaNSStr},
	}
	karmadaPP := &karmadapolicyv1alpha1.PropagationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: ppName, Namespace: testKarmadaNSStr},
	}
	// A companion is still present — but the timeout should let us proceed anyway.
	stuckCompanion := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stuck-companion",
			Namespace: testKarmadaNSStr,
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
		},
	}
	karmadaClient := newKarmadaFakeClient(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testKarmadaNSStr}},
		karmadaWD,
		karmadaPP,
		stuckCompanion,
	)

	r := newTestFederator(projectClient, karmadaClient)
	result, err := r.Reconcile(context.Background(), reconcileRequest())

	// After the timeout the guard is bypassed — finalization must complete.
	require.NoError(t, err, "guard must bypass after timeout even with companion still present")
	assert.Equal(t, ctrl.Result{}, result)

	// The PP must be deleted (last WD for the city, guard bypassed).
	var remainingPP karmadapolicyv1alpha1.PropagationPolicy
	err = karmadaClient.Get(context.Background(),
		types.NamespacedName{Name: ppName, Namespace: testKarmadaNSStr},
		&remainingPP)
	assert.True(t, apierrors.IsNotFound(err), "PropagationPolicy should be deleted after guard timeout bypass")
}
