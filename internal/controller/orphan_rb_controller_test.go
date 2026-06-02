// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	karmadaworkv1alpha2 "github.com/karmada-io/api/work/v1alpha2"
)

// ─── Test constants ────────────────────────────────────────────────────────────

const (
	// orbTestNS is the hub namespace where companion RBs and their companions live.
	orbTestNS = "ns-efdf8ca1-9c2d-4ac8-b161-1951503a2879"

	// orbCityPPName is the PropagationPolicy name label used on companion RBs.
	orbCityPPName = "city-dfw"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// newOrphanRBReconciler creates an OrphanRBReconciler backed by a fake hub client.
func newOrphanRBReconciler(objs ...client.Object) (*OrphanRBReconciler, client.Client) {
	hubCl := fake.NewClientBuilder().
		WithScheme(newKarmadaScheme()).
		WithObjects(objs...).
		Build()
	return &OrphanRBReconciler{HubClient: hubCl}, hubCl
}

// makeCompanionRB returns a ResourceBinding that represents an in-scope
// companion RB (city-PP label + configmap/secret name suffix). The RB is
// placed in orbTestNS.
func makeCompanionRB(name string) *karmadaworkv1alpha2.ResourceBinding {
	return &karmadaworkv1alpha2.ResourceBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: orbTestNS,
			Name:      name,
			Labels:    map[string]string{ppNameLabelKey: orbCityPPName},
		},
	}
}

// reconcileRB runs Reconcile for the named ResourceBinding in orbTestNS.
func reconcileRB(t *testing.T, r *OrphanRBReconciler, name string) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: orbTestNS, Name: name},
	})
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestOrphanRB_DeletesOrphanedConfigMapRB asserts that an RB whose companion
// ConfigMap is absent is deleted.
func TestOrphanRB_DeletesOrphanedConfigMapRB(t *testing.T) {
	t.Parallel()

	rb := makeCompanionRB("cm-pristine-configmap")
	// No ConfigMap "cm-pristine" in the hub namespace — the RB is orphaned.
	r, hubCl := newOrphanRBReconciler(rb)

	_, err := reconcileRB(t, r, "cm-pristine-configmap")
	require.NoError(t, err)

	var got karmadaworkv1alpha2.ResourceBinding
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: orbTestNS, Name: "cm-pristine-configmap"}, &got)
	require.True(t, apierrors.IsNotFound(err), "orphaned companion RB must be deleted")
}

// TestOrphanRB_DeletesOrphanedSecretRB asserts the same for Secret companion RBs.
func TestOrphanRB_DeletesOrphanedSecretRB(t *testing.T) {
	t.Parallel()

	rb := makeCompanionRB("secret-pristine-secret")
	r, hubCl := newOrphanRBReconciler(rb)

	_, err := reconcileRB(t, r, "secret-pristine-secret")
	require.NoError(t, err)

	var got karmadaworkv1alpha2.ResourceBinding
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: orbTestNS, Name: "secret-pristine-secret"}, &got)
	require.True(t, apierrors.IsNotFound(err), "orphaned companion Secret RB must be deleted")
}

// TestOrphanRB_SkipsLiveCompanion asserts that an RB whose companion still
// exists (live, no deletionTimestamp) is NOT deleted.
func TestOrphanRB_SkipsLiveCompanion(t *testing.T) {
	t.Parallel()

	rb := makeCompanionRB("cm-live-configmap")
	// Companion ConfigMap exists and is live.
	liveCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: orbTestNS, Name: "cm-live"},
	}
	r, hubCl := newOrphanRBReconciler(rb, liveCM)

	_, err := reconcileRB(t, r, "cm-live-configmap")
	require.NoError(t, err)

	// RB must still exist.
	var got karmadaworkv1alpha2.ResourceBinding
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: orbTestNS, Name: "cm-live-configmap"}, &got)
	require.NoError(t, err, "RB must NOT be deleted when companion still exists")
	assert.Equal(t, "cm-live-configmap", got.Name)
}

// TestOrphanRB_SkipsTerminatingCompanion asserts that an RB whose companion
// has a deletionTimestamp is NOT deleted — the deletion cascade may still fire.
func TestOrphanRB_SkipsTerminatingCompanion(t *testing.T) {
	t.Parallel()

	rb := makeCompanionRB("cm-terminating-configmap")
	now := metav1.Now()
	// Companion exists with a deletionTimestamp — deletion in progress.
	terminatingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         orbTestNS,
			Name:              "cm-terminating",
			DeletionTimestamp: &now,
			Finalizers:        []string{"karmada.io/fake"},
		},
	}
	r, hubCl := newOrphanRBReconciler(rb, terminatingCM)

	_, err := reconcileRB(t, r, "cm-terminating-configmap")
	require.NoError(t, err)

	// RB must still exist.
	var got karmadaworkv1alpha2.ResourceBinding
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: orbTestNS, Name: "cm-terminating-configmap"}, &got)
	require.NoError(t, err, "RB must NOT be deleted when companion is terminating")
}

// TestOrphanRB_IgnoresNonCityPPRB asserts that an RB without the "city-"
// PropagationPolicy label is never touched, even if its companion is absent.
func TestOrphanRB_IgnoresNonCityPPRB(t *testing.T) {
	t.Parallel()

	// RB has a non-city PP label (e.g. a WD's PP or an unrelated tenant's PP).
	nonCityRB := &karmadaworkv1alpha2.ResourceBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: orbTestNS,
			Name:      "cm-other-configmap",
			Labels:    map[string]string{ppNameLabelKey: "other-pp"},
		},
	}
	r, hubCl := newOrphanRBReconciler(nonCityRB)

	_, err := reconcileRB(t, r, "cm-other-configmap")
	require.NoError(t, err)

	// RB must still exist — it is outside the companion scope.
	var got karmadaworkv1alpha2.ResourceBinding
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: orbTestNS, Name: "cm-other-configmap"}, &got)
	require.NoError(t, err, "non-city-PP RB must NOT be touched")
}

// TestOrphanRB_IgnoresWorkloadDeploymentRB asserts that a WD ResourceBinding
// (suffix "-workloaddeployment") is never deleted even when its companion is
// absent and it has a city- PP label. WD RBs are out of scope.
func TestOrphanRB_IgnoresWorkloadDeploymentRB(t *testing.T) {
	t.Parallel()

	// WD RB: correct city- PP label but wrong kind suffix.
	wdRB := &karmadaworkv1alpha2.ResourceBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: orbTestNS,
			Name:      "my-workload-workloaddeployment",
			Labels:    map[string]string{ppNameLabelKey: "city-dfw"},
		},
	}
	r, hubCl := newOrphanRBReconciler(wdRB)

	_, err := reconcileRB(t, r, "my-workload-workloaddeployment")
	require.NoError(t, err)

	var got karmadaworkv1alpha2.ResourceBinding
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: orbTestNS, Name: "my-workload-workloaddeployment"}, &got)
	require.NoError(t, err, "WorkloadDeployment RB must NEVER be deleted by the orphan sweep")
}

// TestOrphanRB_ToleratesRBAlreadyGone asserts that the reconciler does not
// error when the RB is already gone at reconcile time (deleted by Component 3).
func TestOrphanRB_ToleratesRBAlreadyGone(t *testing.T) {
	t.Parallel()

	// Do not seed the RB — simulate it being deleted before Reconcile runs.
	r, _ := newOrphanRBReconciler()

	_, err := reconcileRB(t, r, "cm-already-gone-configmap")
	require.NoError(t, err, "reconciler must tolerate NotFound on the RB itself")
}

// TestOrphanRB_CompanionFromRBName_Patterns verifies companionFromRBName
// correctly extracts companion names/kinds and rejects non-companion patterns.
func TestOrphanRB_CompanionFromRBName_Patterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rbName   string
		wantOK   bool
		wantName string
		wantKind string
	}{
		{"cm-pristine-configmap", true, "cm-pristine", kindConfigMap},
		{"secret-foo-secret", true, "secret-foo", kindSecret},
		{"a-b-c-secret", true, "a-b-c", kindSecret},
		{"wd-foo-workloaddeployment", false, "", ""},
		{"just-a-name", false, "", ""},
		{"", false, "", ""},
		{"-configmap", true, "", kindConfigMap}, // degenerate — name is empty
		{"notsuffix", false, "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.rbName, func(t *testing.T) {
			name, kind, ok := companionFromRBName(tc.rbName)
			assert.Equal(t, tc.wantOK, ok, "ok mismatch for %q", tc.rbName)
			assert.Equal(t, tc.wantName, name, "name mismatch for %q", tc.rbName)
			assert.Equal(t, tc.wantKind, kind, "kind mismatch for %q", tc.rbName)
		})
	}
}
