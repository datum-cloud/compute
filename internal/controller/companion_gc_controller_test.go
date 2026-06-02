// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
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
	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// ─── Test constants ────────────────────────────────────────────────────────────

const (
	// gcHubNS is the hub namespace (ns-{project-uid}) used across companion GC tests.
	gcHubNS = "ns-efdf8ca1-9c2d-4ac8-b161-1951503a2879"

	// gcProjectNS is the project-plane namespace whose WDs are recorded in the
	// referenced-by annotation. The annotation key format is "projectNS/wdName".
	gcProjectNS = "default"

	// gcWD1Name is the WD name used in single-referrer tests.
	gcWD1Name = "mount-pristine"

	// gcWD2Name is the WD name used in multi-referrer tests.
	gcWD2Name = "mount-alternate"

	// gcCMPristineRBName is the Karmada ResourceBinding name for the cm-pristine
	// companion — used in both companion-GC and orphan-RB test assertions.
	gcCMPristineRBName = "cm-pristine-configmap"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// gcRefKey builds the "projectNS/wdName" annotation entry used in production.
// Using a real-looking key exercises the annotation-parsing path correctly.
func gcRefKey(wdName string) string {
	return gcProjectNS + "/" + wdName
}

// gcEncodeRefs serialises WD keys to the JSON annotation value that
// ReferencedDataController writes on production companions.
func gcEncodeRefs(keys ...string) string {
	b, _ := json.Marshal(keys)
	return string(b)
}

// newGCReconciler creates a CompanionGCReconciler backed by a fake hub client
// containing the supplied objects.
func newGCReconciler(objs ...client.Object) (*CompanionGCReconciler, client.Client) {
	hubCl := fake.NewClientBuilder().
		WithScheme(newKarmadaScheme()).
		WithObjects(objs...).
		Build()
	return &CompanionGCReconciler{HubClient: hubCl}, hubCl
}

// gcMakeCompanionCM builds a ConfigMap that mirrors a production hub companion:
// carries the referenced-data label and a referenced-by annotation whose value
// is the JSON-encoded list of WD keys.
func gcMakeCompanionCM(name string, wdKeys ...string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: gcHubNS,
			Name:      name,
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
			Annotations: map[string]string{
				companionRefCountAnnotation: gcEncodeRefs(wdKeys...),
			},
		},
		Data: map[string]string{"key": "value"},
	}
}

// gcMakeCompanionSecret builds a Secret that mirrors a production hub companion.
func gcMakeCompanionSecret(name string, wdKeys ...string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: gcHubNS,
			Name:      name,
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
			Annotations: map[string]string{
				companionRefCountAnnotation: gcEncodeRefs(wdKeys...),
			},
		},
		Data: map[string][]byte{"key": []byte("value")},
	}
}

// gcMakeHubWD builds a WorkloadDeployment that mirrors a production hub WD:
// lives in the hub namespace (ns-{project-uid}), not the project namespace.
func gcMakeHubWD(name string) *computev1alpha.WorkloadDeployment {
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: gcHubNS,
			Name:      name,
		},
	}
}

// gcMakeTerminatingHubWD builds a hub WD that is terminating (deletionTimestamp set).
func gcMakeTerminatingHubWD(name string) *computev1alpha.WorkloadDeployment {
	now := metav1.Now()
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         gcHubNS,
			Name:              name,
			DeletionTimestamp: &now,
			Finalizers:        []string{"compute.datumapis.com/test"},
		},
	}
}

// gcMakeCompanionRB builds a ResourceBinding for a companion — used to assert
// the RB teardown path. The naming follows Karmada's convention: {name}-{kind}.
func gcMakeCompanionRB(companionName, kindSuffix string) *karmadaworkv1alpha2.ResourceBinding {
	return &karmadaworkv1alpha2.ResourceBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: gcHubNS,
			Name:      companionName + "-" + kindSuffix,
			Annotations: map[string]string{
				ppNameAnnotationKey: "city-dfw",
			},
		},
	}
}

// reconcileGC runs Reconcile for the named object in gcHubNS.
func reconcileGC(t *testing.T, r *CompanionGCReconciler, name string) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: gcHubNS, Name: name},
	})
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestCompanionGC_DeletesOrphanedConfigMap asserts that a ConfigMap companion
// whose sole referrer WD is absent from the hub is deleted along with its RB.
func TestCompanionGC_DeletesOrphanedConfigMap(t *testing.T) {
	t.Parallel()

	cm := gcMakeCompanionCM("cm-pristine", gcRefKey(gcWD1Name))
	rb := gcMakeCompanionRB("cm-pristine", "configmap")
	// No hub WD "mount-pristine" — the referrer is absent.
	r, hubCl := newGCReconciler(cm, rb)

	_, err := reconcileGC(t, r, "cm-pristine")
	require.NoError(t, err)

	// Companion ConfigMap must be deleted.
	var gotCM corev1.ConfigMap
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "cm-pristine"}, &gotCM)
	require.True(t, apierrors.IsNotFound(err), "orphaned ConfigMap companion must be deleted")

	// Associated ResourceBinding must be deleted.
	var gotRB karmadaworkv1alpha2.ResourceBinding
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: gcCMPristineRBName}, &gotRB)
	require.True(t, apierrors.IsNotFound(err), "orphaned ConfigMap RB must be deleted")
}

// TestCompanionGC_DeletesOrphanedSecret asserts the same for Secret companions.
func TestCompanionGC_DeletesOrphanedSecret(t *testing.T) {
	t.Parallel()

	secret := gcMakeCompanionSecret("secret-pristine", gcRefKey(gcWD1Name))
	rb := gcMakeCompanionRB("secret-pristine", "secret")
	r, hubCl := newGCReconciler(secret, rb)

	_, err := reconcileGC(t, r, "secret-pristine")
	require.NoError(t, err)

	var gotSecret corev1.Secret
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "secret-pristine"}, &gotSecret)
	require.True(t, apierrors.IsNotFound(err), "orphaned Secret companion must be deleted")

	var gotRB karmadaworkv1alpha2.ResourceBinding
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "secret-pristine-secret"}, &gotRB)
	require.True(t, apierrors.IsNotFound(err), "orphaned Secret RB must be deleted")
}

// TestCompanionGC_PreservesCompanionWithLiveReferrer asserts that a companion
// whose referrer WD still exists in the hub is NOT deleted.
func TestCompanionGC_PreservesCompanionWithLiveReferrer(t *testing.T) {
	t.Parallel()

	cm := gcMakeCompanionCM("cm-live", gcRefKey(gcWD1Name))
	liveWD := gcMakeHubWD(gcWD1Name) // WD exists in hub namespace
	r, hubCl := newGCReconciler(cm, liveWD)

	_, err := reconcileGC(t, r, "cm-live")
	require.NoError(t, err)

	// Companion must still exist.
	var got corev1.ConfigMap
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "cm-live"}, &got)
	require.NoError(t, err, "companion with live referrer must NOT be deleted")
}

// TestCompanionGC_TerminatingReferrerCountsAsPresent asserts that a companion
// whose referrer WD is terminating (DeletionTimestamp set) is preserved.
// The ReferencedDataController finalizer on the terminating WD may still complete
// teardown; the GC must not race with it.
func TestCompanionGC_TerminatingReferrerCountsAsPresent(t *testing.T) {
	t.Parallel()

	cm := gcMakeCompanionCM("cm-term", gcRefKey(gcWD1Name))
	terminatingWD := gcMakeTerminatingHubWD(gcWD1Name)
	r, hubCl := newGCReconciler(cm, terminatingWD)

	_, err := reconcileGC(t, r, "cm-term")
	require.NoError(t, err)

	// Companion must still exist.
	var got corev1.ConfigMap
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "cm-term"}, &got)
	require.NoError(t, err, "companion with terminating referrer must NOT be deleted")
}

// TestCompanionGC_MultiReferrer_AllAbsent asserts that a companion shared by
// two WDs is deleted when BOTH referrers are absent from the hub.
func TestCompanionGC_MultiReferrer_AllAbsent(t *testing.T) {
	t.Parallel()

	cm := gcMakeCompanionCM("cm-shared",
		gcRefKey(gcWD1Name),
		gcRefKey(gcWD2Name),
	)
	// Neither WD exists on the hub.
	r, hubCl := newGCReconciler(cm)

	_, err := reconcileGC(t, r, "cm-shared")
	require.NoError(t, err)

	var got corev1.ConfigMap
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "cm-shared"}, &got)
	require.True(t, apierrors.IsNotFound(err), "companion with all referrers absent must be deleted")
}

// TestCompanionGC_MultiReferrer_OneRemains asserts that a companion shared by
// two WDs is preserved when one referrer is still present on the hub.
func TestCompanionGC_MultiReferrer_OneRemains(t *testing.T) {
	t.Parallel()

	cm := gcMakeCompanionCM("cm-partial",
		gcRefKey(gcWD1Name),
		gcRefKey(gcWD2Name),
	)
	// Only one of the two WDs is gone; gcWD2Name still exists.
	liveWD := gcMakeHubWD(gcWD2Name)
	r, hubCl := newGCReconciler(cm, liveWD)

	_, err := reconcileGC(t, r, "cm-partial")
	require.NoError(t, err)

	var got corev1.ConfigMap
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "cm-partial"}, &got)
	require.NoError(t, err, "companion with at least one live referrer must NOT be deleted")
}

// TestCompanionGC_CorruptAnnotationPreservesCompanion asserts that a companion
// with a corrupt (unparseable) referenced-by annotation is NOT deleted.
// Corrupt state is treated conservatively: other referrers may exist that cannot
// be parsed; we must not silently drop companions.
func TestCompanionGC_CorruptAnnotationPreservesCompanion(t *testing.T) {
	t.Parallel()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: gcHubNS,
			Name:      "cm-corrupt",
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
			Annotations: map[string]string{
				// Not valid JSON — simulates corruption (e.g. truncated write).
				companionRefCountAnnotation: `["default/wd-1","default/wd-2`,
			},
		},
	}
	r, hubCl := newGCReconciler(cm)

	_, err := reconcileGC(t, r, "cm-corrupt")
	require.NoError(t, err, "corrupt annotation must not cause an error (only skip)")

	var got corev1.ConfigMap
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "cm-corrupt"}, &got)
	require.NoError(t, err, "companion with corrupt annotation must NOT be deleted")
}

// TestCompanionGC_EmptyAnnotationPreservesCompanion asserts that a companion
// with no referenced-by annotation is not deleted. Such companions were not
// created by the ReferencedDataController (or predate the annotation).
func TestCompanionGC_EmptyAnnotationPreservesCompanion(t *testing.T) {
	t.Parallel()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: gcHubNS,
			Name:      "cm-no-anno",
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
			// No referenced-by annotation at all.
		},
	}
	r, hubCl := newGCReconciler(cm)

	_, err := reconcileGC(t, r, "cm-no-anno")
	require.NoError(t, err)

	var got corev1.ConfigMap
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "cm-no-anno"}, &got)
	require.NoError(t, err, "companion without referenced-by annotation must NOT be deleted")
}

// TestCompanionGC_UnlabeledObjectUnaffected asserts that an unlabeled ConfigMap
// (not a companion) is never touched even if its name matches a companion pattern.
func TestCompanionGC_UnlabeledObjectUnaffected(t *testing.T) {
	t.Parallel()

	// ConfigMap without the referenced-data label — not a companion.
	unlabeled := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: gcHubNS,
			Name:      "not-a-companion",
			Annotations: map[string]string{
				// Even if it has the annotation, the GC must not touch it without the label.
				companionRefCountAnnotation: "[]",
			},
		},
	}
	r, hubCl := newGCReconciler(unlabeled)

	// The GC is triggered via the predicate; force reconcile directly.
	_, err := reconcileGC(t, r, "not-a-companion")
	require.NoError(t, err)

	// Object must still exist — the GC skips non-companions.
	var got corev1.ConfigMap
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "not-a-companion"}, &got)
	require.NoError(t, err, "non-companion object must NOT be touched by GC")
}

// TestCompanionGC_ObjectAlreadyGone asserts that the reconciler does not error
// when the companion is already gone at reconcile time.
func TestCompanionGC_ObjectAlreadyGone(t *testing.T) {
	t.Parallel()

	// Do not seed any object.
	r, _ := newGCReconciler()

	_, err := reconcileGC(t, r, "already-gone")
	require.NoError(t, err, "reconciler must tolerate NotFound on the companion itself")
}

// TestCompanionGC_RBAlreadyGoneIsToleratedOnOrphanDelete asserts that if the
// companion is orphaned but its RB is already gone (Karmada cascade beat us),
// the GC does not error.
func TestCompanionGC_RBAlreadyGoneIsToleratedOnOrphanDelete(t *testing.T) {
	t.Parallel()

	cm := gcMakeCompanionCM("cm-norb", gcRefKey(gcWD1Name))
	// No RB seeded — simulates Karmada cascade already cleaned it up.
	r, hubCl := newGCReconciler(cm)

	_, err := reconcileGC(t, r, "cm-norb")
	require.NoError(t, err, "missing RB must not cause an error — IgnoreNotFound applied")

	// Companion itself must be deleted (the IgnoreNotFound RB delete must not abort).
	var got corev1.ConfigMap
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "cm-norb"}, &got)
	require.True(t, apierrors.IsNotFound(err), "companion must be deleted even when RB was already absent")
}

// TestCompanionGC_WdNameFromRefKey_Patterns verifies the WD name extraction
// from various key formats including edge cases.
func TestCompanionGC_WdNameFromRefKey_Patterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key      string
		wantName string
		wantErr  bool
	}{
		{"default/mount-pristine", "mount-pristine", false},
		{"my-project/my-wd", "my-wd", false},
		{"namespace/wd-with-dashes", "wd-with-dashes", false},
		{"missing-separator", "", true},
		{"ns/", "", true},
		{"", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			name, err := wdNameFromRefKey(tc.key)
			if tc.wantErr {
				assert.Error(t, err, "expected error for key %q", tc.key)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.wantName, name, "name mismatch for key %q", tc.key)
			}
		})
	}
}

// TestCompanionGC_PeriodicSweepReconcilesCandidates is a white-box test that
// calls sweep() directly and verifies it drives reconciliation for stranded
// companions.
func TestCompanionGC_PeriodicSweepReconcilesCandidates(t *testing.T) {
	t.Parallel()

	// Stranded companion: labeled, referenced-by points at an absent WD.
	strangedCM := gcMakeCompanionCM("stranded-cm", gcRefKey("dead-wd"))
	// Live companion: labeled, referenced-by points at an existing WD.
	liveCM := gcMakeCompanionCM("live-cm", gcRefKey("live-wd"))
	liveWD := gcMakeHubWD("live-wd")

	r, hubCl := newGCReconciler(strangedCM, liveCM, liveWD)

	sweep := &companionGCPeriodicSweep{
		hubClient:  hubCl,
		reconciler: r,
		interval:   companionGCSweepInterval, // unused in this test
	}
	sweep.sweep(context.Background())

	// Stranded companion must be deleted.
	var gotStranded corev1.ConfigMap
	err := hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "stranded-cm"}, &gotStranded)
	require.True(t, apierrors.IsNotFound(err), "stranded companion must be deleted by sweep")

	// Live companion must survive.
	var gotLive corev1.ConfigMap
	err = hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: gcHubNS, Name: "live-cm"}, &gotLive)
	require.NoError(t, err, "live companion must survive the sweep")
}
