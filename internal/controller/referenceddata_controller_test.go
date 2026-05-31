// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/referenceddata"
)

const (
	// rdTestNamespace is the project namespace used across ReferencedData tests.
	rdTestNamespace = "my-project"

	// rdTestAppConfig is a frequently reused source ConfigMap name in tests.
	rdTestAppConfig = "app-config"
)

// rdTestScheme builds a runtime.Scheme suitable for ReferencedDataController tests.
func rdTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, computev1alpha.AddToScheme(s))
	return s
}

// newRDController creates a ReferencedDataController with the given reader wired
// into a fake multicluster manager that has one cluster identified by clusterName.
func newRDController(t *testing.T, cl client.Client, reader referenceddata.ProjectConfigSecretReader, opts ...func(*ReferencedDataControllerOptions)) (*ReferencedDataController, string) {
	t.Helper()
	clusterName := "test-cluster"

	mgr := &fakeMCManager{
		clusters: map[string]cluster.Cluster{
			clusterName: &fakeCluster{cl: cl},
		},
	}

	controllerOpts := ReferencedDataControllerOptions{
		Reader: reader,
	}
	for _, fn := range opts {
		fn(&controllerOpts)
	}

	c := &ReferencedDataController{
		mgr:  mgr,
		opts: controllerOpts,
	}
	return c, clusterName
}

// reconcileWD is a convenience wrapper that runs one reconcile for the named WD
// in namespace ns on clusterName.
func reconcileWD(t *testing.T, c *ReferencedDataController, clusterName, ns, name string) {
	t.Helper()
	cn := multicluster.ClusterName(clusterName)
	ctx := mccontext.WithCluster(context.Background(), cn)
	_, err := c.Reconcile(ctx, mcreconcile.Request{
		Request: reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: ns, Name: name},
		},
		ClusterName: cn,
	})
	require.NoError(t, err)
}

// makeWD returns a minimal WorkloadDeployment with the given template.
func makeWD(ns, name string, template computev1alpha.InstanceTemplateSpec) *computev1alpha.WorkloadDeployment {
	return &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			Template: template,
		},
	}
}

// templateWithConfigMap returns an InstanceTemplateSpec referencing the named ConfigMap
// as a volume.
func templateWithConfigMap(cmName string) computev1alpha.InstanceTemplateSpec {
	return computev1alpha.InstanceTemplateSpec{
		Spec: computev1alpha.InstanceSpec{
			Volumes: []computev1alpha.InstanceVolume{
				{
					Name: "cfg-vol",
					VolumeSource: computev1alpha.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
						},
					},
				},
			},
		},
	}
}

// templateWithSecret returns an InstanceTemplateSpec referencing the named Secret
// as a volume.
func templateWithSecret(secretName string) computev1alpha.InstanceTemplateSpec {
	return computev1alpha.InstanceTemplateSpec{
		Spec: computev1alpha.InstanceSpec{
			Volumes: []computev1alpha.InstanceVolume{
				{
					Name: "sec-vol",
					VolumeSource: computev1alpha.VolumeSource{
						Secret: &corev1.SecretVolumeSource{SecretName: secretName},
					},
				},
			},
		},
	}
}

// getWD fetches the latest WD from the fake client.
func getWD(t *testing.T, cl client.Client, key types.NamespacedName) *computev1alpha.WorkloadDeployment {
	t.Helper()
	var wd computev1alpha.WorkloadDeployment
	require.NoError(t, cl.Get(context.Background(), key, &wd))
	return &wd
}

// getCompanionCM fetches a companion ConfigMap or fails the test.
func getCompanionCM(t *testing.T, cl client.Client, ns, name string) *corev1.ConfigMap {
	t.Helper()
	var cm corev1.ConfigMap
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &cm))
	return &cm
}

// getCompanionSecret fetches a companion Secret or fails the test.
func getCompanionSecret(t *testing.T, cl client.Client, ns, name string) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &s))
	return &s
}

// decodeExpectedAnnotation parses the expected-referenced-data annotation from a WD.
func decodeExpectedAnnotation(t *testing.T, wd *computev1alpha.WorkloadDeployment) []string {
	t.Helper()
	raw, ok := wd.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]
	require.True(t, ok, "expected-referenced-data annotation must be set")
	var names []string
	require.NoError(t, json.Unmarshal([]byte(raw), &names))
	return names
}

// ─── Happy path: companion + annotation + condition ───────────────────────────

func TestReferencedData_HappyPath_ConfigMap(t *testing.T) {
	ns := rdTestNamespace
	cmName := rdTestAppConfig
	companionName := referenceddata.CompanionName("ConfigMap", cmName)

	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmName},
		Data:       map[string]string{"key": "value"},
	}
	wd := makeWD(ns, "wd-1", templateWithConfigMap(cmName))

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(srcCM, wd).
		WithStatusSubresource(wd).
		Build()

	c, clusterName := newRDController(t, cl, nil)

	// First reconcile: stamps finalizer and returns.
	reconcileWD(t, c, clusterName, ns, "wd-1")
	// Fetch updated WD (finalizer stamped).
	wd = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	require.Contains(t, wd.Finalizers, referencedDataFinalizer, "finalizer should be present after first reconcile")

	// Second reconcile: materialises companion + stamps annotation + sets condition.
	reconcileWD(t, c, clusterName, ns, "wd-1")

	// Companion ConfigMap should exist.
	companion := getCompanionCM(t, cl, ns, companionName)
	assert.Equal(t, "true", companion.Labels[computev1alpha.ReferencedDataLabel], "companion must have referenced-data label")
	assert.Equal(t, "value", companion.Data["key"], "companion must copy source Data")

	// Expected annotation should list the companion name.
	wd = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	expectedNames := decodeExpectedAnnotation(t, wd)
	assert.Equal(t, []string{companionName}, expectedNames)

	// Condition should be True/Ready.
	cond := apimeta.FindStatusCondition(wd.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonReady, cond.Reason)
}

func TestReferencedData_HappyPath_Secret(t *testing.T) {
	ns := rdTestNamespace
	secretName := "db-creds"
	companionName := referenceddata.CompanionName("Secret", secretName)

	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: secretName},
		Data:       map[string][]byte{"password": []byte("hunter2")},
		Type:       corev1.SecretTypeOpaque,
	}
	wd := makeWD(ns, "wd-1", templateWithSecret(secretName))

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(srcSecret, wd).
		WithStatusSubresource(wd).
		Build()

	c, clusterName := newRDController(t, cl, nil)

	reconcileWD(t, c, clusterName, ns, "wd-1")
	reconcileWD(t, c, clusterName, ns, "wd-1")

	companion := getCompanionSecret(t, cl, ns, companionName)
	assert.Equal(t, "true", companion.Labels[computev1alpha.ReferencedDataLabel])
	assert.Equal(t, []byte("hunter2"), companion.Data["password"])
	assert.Equal(t, corev1.SecretTypeOpaque, companion.Type)

	wd = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	expectedNames := decodeExpectedAnnotation(t, wd)
	assert.Equal(t, []string{companionName}, expectedNames)

	cond := apimeta.FindStatusCondition(wd.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

// ─── Source-not-found sets SourceNotFound condition ──────────────────────────

func TestReferencedData_SourceNotFound(t *testing.T) {
	ns := rdTestNamespace
	cmName := "missing-config"

	// Source ConfigMap does NOT exist in the cluster.
	wd := makeWD(ns, "wd-1", templateWithConfigMap(cmName))

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(wd).
		WithStatusSubresource(wd).
		Build()

	c, clusterName := newRDController(t, cl, nil)

	reconcileWD(t, c, clusterName, ns, "wd-1")
	wd = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	require.Contains(t, wd.Finalizers, referencedDataFinalizer)

	reconcileWD(t, c, clusterName, ns, "wd-1")

	wd = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	cond := apimeta.FindStatusCondition(wd.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceNotFound, cond.Reason)

	// No annotation should be set (nothing was delivered).
	_, hasAnno := wd.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]
	assert.False(t, hasAnno)
}

// ─── Source-unauthorized sets SourceUnauthorized condition ───────────────────

func TestReferencedData_SourceUnauthorized(t *testing.T) {
	ns := rdTestNamespace
	cmName := "auth-config"

	wd := makeWD(ns, "wd-1", templateWithConfigMap(cmName))

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(wd).
		WithStatusSubresource(wd).
		Build()

	// Use a reader that always returns ErrSourceUnauthorized.
	unauthorizedReader := &stubReader{
		getCM: func(_ context.Context, _, _, _ string) (*corev1.ConfigMap, error) {
			return nil, fmt.Errorf("%w: ConfigMap %s", referenceddata.ErrSourceUnauthorized, cmName)
		},
	}

	c, clusterName := newRDController(t, cl, unauthorizedReader)

	reconcileWD(t, c, clusterName, ns, "wd-1")
	reconcileWD(t, c, clusterName, ns, "wd-1")

	wd = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	cond := apimeta.FindStatusCondition(wd.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceUnauthorized, cond.Reason)
}

// ─── Oversized source sets SourceTooLarge ────────────────────────────────────

func TestReferencedData_SourceTooLarge_PerObject(t *testing.T) {
	ns := rdTestNamespace
	cmName := "fat-config"
	companionName := referenceddata.CompanionName("ConfigMap", cmName)

	bigData := make([]byte, 300*1024) // 300 KiB > 256 KiB default
	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmName},
		BinaryData: map[string][]byte{"blob": bigData},
	}
	wd := makeWD(ns, "wd-1", templateWithConfigMap(cmName))

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(srcCM, wd).
		WithStatusSubresource(wd).
		Build()

	c, clusterName := newRDController(t, cl, nil)

	reconcileWD(t, c, clusterName, ns, "wd-1")
	reconcileWD(t, c, clusterName, ns, "wd-1")

	wd = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	cond := apimeta.FindStatusCondition(wd.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceTooLarge, cond.Reason)

	// Companion must NOT have been materialised.
	var phantom corev1.ConfigMap
	err := cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: companionName}, &phantom)
	assert.Error(t, err, "companion must not exist when source is too large")
}

func TestReferencedData_SourceTooLarge_Aggregate(t *testing.T) {
	ns := rdTestNamespace
	cmName1 := "config-a"
	cmName2 := "config-b"

	// Each 600 KiB; aggregate 1.2 MiB > 1 MiB default.
	halfBig := make([]byte, 600*1024)
	src1 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmName1},
		BinaryData: map[string][]byte{"blob": halfBig},
	}
	src2 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmName2},
		BinaryData: map[string][]byte{"blob": halfBig},
	}

	template := computev1alpha.InstanceTemplateSpec{
		Spec: computev1alpha.InstanceSpec{
			Volumes: []computev1alpha.InstanceVolume{
				{Name: "vol1", VolumeSource: computev1alpha.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cmName1}},
				}},
				{Name: "vol2", VolumeSource: computev1alpha.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cmName2}},
				}},
			},
		},
	}
	wd := makeWD(ns, "wd-1", template)

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(src1, src2, wd).
		WithStatusSubresource(wd).
		Build()

	// Override per-object limit high enough so each passes alone, but aggregate fails.
	c, clusterName := newRDController(t, cl, nil, func(o *ReferencedDataControllerOptions) {
		o.PerObjectLimitBytes = 700 * 1024  // 700 KiB — each obj passes
		o.AggregateLimitBytes = 1000 * 1024 // 1000 KiB — aggregate fails at 1.2 MiB
	})

	reconcileWD(t, c, clusterName, ns, "wd-1")
	reconcileWD(t, c, clusterName, ns, "wd-1")

	wd = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	cond := apimeta.FindStatusCondition(wd.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, computev1alpha.ReferencedDataReasonSourceTooLarge, cond.Reason)
}

// ─── Rotation: source change → companion refreshed ───────────────────────────

func TestReferencedData_Rotation(t *testing.T) {
	ns := rdTestNamespace
	cmName := rdTestAppConfig
	companionName := referenceddata.CompanionName("ConfigMap", cmName)

	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmName},
		Data:       map[string]string{"ver": "v1"},
	}
	wd := makeWD(ns, "wd-1", templateWithConfigMap(cmName))

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(srcCM, wd).
		WithStatusSubresource(wd).
		Build()

	c, clusterName := newRDController(t, cl, nil)

	// Two passes to materialise initially.
	reconcileWD(t, c, clusterName, ns, "wd-1")
	reconcileWD(t, c, clusterName, ns, "wd-1")

	companion := getCompanionCM(t, cl, ns, companionName)
	assert.Equal(t, "v1", companion.Data["ver"])

	// Simulate a source update (rotation).
	srcCM.Data["ver"] = "v2"
	require.NoError(t, cl.Update(context.Background(), srcCM))

	// Re-reconcile (as if triggered by the source watch).
	reconcileWD(t, c, clusterName, ns, "wd-1")

	companion = getCompanionCM(t, cl, ns, companionName)
	assert.Equal(t, "v2", companion.Data["ver"], "companion must reflect rotated source")
}

// ─── Ref-count: two WDs sharing a companion ──────────────────────────────────

func TestReferencedData_RefCount_TwoWDs(t *testing.T) {
	ns := rdTestNamespace
	cmName := "shared-config"
	companionName := referenceddata.CompanionName("ConfigMap", cmName)

	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmName},
		Data:       map[string]string{"k": "v"},
	}
	wd1 := makeWD(ns, "wd-1", templateWithConfigMap(cmName))
	wd2 := makeWD(ns, "wd-2", templateWithConfigMap(cmName))

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(srcCM, wd1, wd2).
		WithStatusSubresource(wd1, wd2).
		Build()

	c, clusterName := newRDController(t, cl, nil)

	// Materialise for wd-1.
	reconcileWD(t, c, clusterName, ns, "wd-1")
	reconcileWD(t, c, clusterName, ns, "wd-1")

	companion := getCompanionCM(t, cl, ns, companionName)
	refs1, err := decodeRefCount(companion.Annotations)
	require.NoError(t, err)
	assert.Contains(t, refs1, types.NamespacedName{Namespace: ns, Name: "wd-1"}.String())

	// Materialise for wd-2.
	reconcileWD(t, c, clusterName, ns, "wd-2")
	reconcileWD(t, c, clusterName, ns, "wd-2")

	companion = getCompanionCM(t, cl, ns, companionName)
	refs2, err := decodeRefCount(companion.Annotations)
	require.NoError(t, err)
	assert.Len(t, refs2, 2, "companion must list both WDs")
	assert.Contains(t, refs2, types.NamespacedName{Namespace: ns, Name: "wd-1"}.String())
	assert.Contains(t, refs2, types.NamespacedName{Namespace: ns, Name: "wd-2"}.String())

	// Delete wd-1 (simulate deletion + finalizer processing).
	wd1 = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	require.NoError(t, cl.Delete(context.Background(), wd1))
	reconcileWD(t, c, clusterName, ns, "wd-1")

	// Companion must still exist (wd-2 still holds it).
	companion = getCompanionCM(t, cl, ns, companionName)
	refs3, err := decodeRefCount(companion.Annotations)
	require.NoError(t, err)
	assert.Len(t, refs3, 1, "wd-1 should have been removed from ref-count")
	assert.Contains(t, refs3, types.NamespacedName{Namespace: ns, Name: "wd-2"}.String())

	// Delete wd-2 too.
	wd2 = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-2"})
	require.NoError(t, cl.Delete(context.Background(), wd2))
	reconcileWD(t, c, clusterName, ns, "wd-2")

	// Companion must be gone.
	var gone corev1.ConfigMap
	getErr := cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: companionName}, &gone)
	assert.Error(t, getErr, "companion must be deleted when last WD is removed")
}

// ─── WD deletion cleans up companion ─────────────────────────────────────────

func TestReferencedData_WDDeletion_CleansUpCompanion(t *testing.T) {
	ns := rdTestNamespace
	cmName := "solo-config"
	companionName := referenceddata.CompanionName("ConfigMap", cmName)

	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmName},
		Data:       map[string]string{"a": "b"},
	}
	wd := makeWD(ns, "wd-1", templateWithConfigMap(cmName))

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(srcCM, wd).
		WithStatusSubresource(wd).
		Build()

	c, clusterName := newRDController(t, cl, nil)

	reconcileWD(t, c, clusterName, ns, "wd-1")
	reconcileWD(t, c, clusterName, ns, "wd-1")

	// Companion must exist.
	getCompanionCM(t, cl, ns, companionName)

	// Delete the WD.
	wd = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	require.NoError(t, cl.Delete(context.Background(), wd))

	// Reconcile handles the deletion + finalizer removal.
	// After the controller removes the finalizer, the fake client may GC the WD,
	// so we capture the state before reconcile.
	reconcileWD(t, c, clusterName, ns, "wd-1")

	// Companion must be gone.
	var gone corev1.ConfigMap
	err := cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: companionName}, &gone)
	assert.Error(t, err, "companion must be deleted when the only referencing WD is deleted")
}

// ─── Empty ref set → no companion, no finalizer ──────────────────────────────

func TestReferencedData_EmptyRefs_NoCompanionNoFinalizer(t *testing.T) {
	ns := rdTestNamespace

	// WD template has no ConfigMap/Secret references.
	wd := makeWD(ns, "wd-1", computev1alpha.InstanceTemplateSpec{})

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(wd).
		WithStatusSubresource(wd).
		Build()

	c, clusterName := newRDController(t, cl, nil)
	reconcileWD(t, c, clusterName, ns, "wd-1")

	wd = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	assert.NotContains(t, wd.Finalizers, referencedDataFinalizer, "no finalizer for empty refs")
	_, hasAnno := wd.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]
	assert.False(t, hasAnno, "no annotation for empty refs")
}

// ─── Companion namespace invariant ───────────────────────────────────────────

// TestReferencedData_CompanionNamespaceInvariant asserts that the companion
// lands in the same namespace as the WorkloadDeployment. This is the namespace
// invariant from the plan: "the WorkloadDeployment, its Instances, and the
// companions all live in the same ns-{project-uid} namespace."
func TestReferencedData_CompanionNamespaceInvariant(t *testing.T) {
	ns := "ns-project-uid-123"
	cmName := rdTestAppConfig
	companionName := referenceddata.CompanionName("ConfigMap", cmName)

	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmName},
		Data:       map[string]string{"x": "y"},
	}
	wd := makeWD(ns, "wd-1", templateWithConfigMap(cmName))

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(srcCM, wd).
		WithStatusSubresource(wd).
		Build()

	c, clusterName := newRDController(t, cl, nil)
	reconcileWD(t, c, clusterName, ns, "wd-1")
	reconcileWD(t, c, clusterName, ns, "wd-1")

	companion := getCompanionCM(t, cl, ns, companionName)
	assert.Equal(t, ns, companion.Namespace, "companion must be in WD's namespace")
}

// ─── Phase 1b: federated companion writer ────────────────────────────────────

// newRDControllerFederated creates a ReferencedDataController wired with a
// FederationClient (fake hub client). The project cluster holds the WDs and
// source ConfigMaps/Secrets; the hub client is the destination for companions.
func newRDControllerFederated(
	t *testing.T,
	projectCl client.Client,
	hubCl client.Client,
	reader referenceddata.ProjectConfigSecretReader,
) (*ReferencedDataController, string) {
	t.Helper()
	clusterName := "test-cluster"

	mgr := &fakeMCManager{
		clusters: map[string]cluster.Cluster{
			clusterName: &fakeCluster{cl: projectCl},
		},
	}

	c := &ReferencedDataController{
		mgr: mgr,
		opts: ReferencedDataControllerOptions{
			Reader:           reader,
			FederationClient: hubCl,
		},
	}
	return c, clusterName
}

// TestReferencedData_Federated_CompanionWrittenToHub asserts that, when a
// FederationClient is configured, companions are materialised into the
// downstream ns-{project-uid} namespace on the hub rather than the project
// namespace. The expected-set annotation must also be set on the project WD.
func TestReferencedData_Federated_CompanionWrittenToHub(t *testing.T) {
	// Use the same UID as the shared test constants so the downstream namespace
	// name is deterministic and matches testKarmadaNSStr.
	projNS := testProjNS
	projNSUID := testProjNSUID
	cmName := rdTestAppConfig
	companionName := referenceddata.CompanionName("ConfigMap", cmName)

	// Project cluster objects: namespace (with UID), source ConfigMap, WD.
	projNSObj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: projNS, UID: projNSUID},
	}
	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: projNS, Name: cmName},
		Data:       map[string]string{"key": "federated-value"},
	}
	wd := makeWD(projNS, "wd-fed-1", templateWithConfigMap(cmName))

	s := rdTestScheme(t)
	require.NoError(t, corev1.AddToScheme(s)) // Namespace type

	projectCl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(projNSObj, srcCM, wd).
		WithStatusSubresource(wd).
		Build()

	// Hub client: empty federation control plane.
	hubScheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(hubScheme))
	require.NoError(t, computev1alpha.AddToScheme(hubScheme))
	hubCl := fake.NewClientBuilder().WithScheme(hubScheme).Build()

	c, clusterName := newRDControllerFederated(t, projectCl, hubCl, nil)

	// First reconcile: stamps finalizer.
	reconcileWD(t, c, clusterName, projNS, "wd-fed-1")
	// Fetch updated WD.
	wd = getWD(t, projectCl, types.NamespacedName{Namespace: projNS, Name: "wd-fed-1"})
	require.Contains(t, wd.Finalizers, referencedDataFinalizer)

	// Second reconcile: materialises companion on the hub.
	reconcileWD(t, c, clusterName, projNS, "wd-fed-1")

	// Companion must exist on the HUB in the downstream namespace, NOT in the project namespace.
	downstreamNS := testKarmadaNSStr // "ns-aabbccdd-0000-1111-2222-333344445555"
	var hubCM corev1.ConfigMap
	require.NoError(t, hubCl.Get(context.Background(),
		types.NamespacedName{Namespace: downstreamNS, Name: companionName}, &hubCM),
		"companion ConfigMap must exist on the hub in the downstream namespace")
	assert.Equal(t, "federated-value", hubCM.Data["key"], "hub companion must copy source Data")
	assert.Equal(t, "true", hubCM.Labels[computev1alpha.ReferencedDataLabel],
		"hub companion must carry referenced-data label")

	// Companion must NOT exist in the project namespace.
	var projCM corev1.ConfigMap
	err := projectCl.Get(context.Background(),
		types.NamespacedName{Namespace: projNS, Name: companionName}, &projCM)
	assert.Error(t, err, "companion must NOT be written to the project namespace in federated mode")

	// Expected-set annotation must be set on the project WD.
	wd = getWD(t, projectCl, types.NamespacedName{Namespace: projNS, Name: "wd-fed-1"})
	expectedNames := decodeExpectedAnnotation(t, wd)
	assert.Equal(t, []string{companionName}, expectedNames)
}

// TestReferencedData_WriterSelection asserts that writerFor returns a
// localCompanionWriter when FederationClient is nil, and a
// downstreamCompanionWriter when it is set.
func TestReferencedData_WriterSelection(t *testing.T) {
	t.Parallel()

	projNS := testProjNS
	projNSUID := testProjNSUID

	projNSObj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: projNS, UID: projNSUID},
	}
	wd := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: projNS, Name: "wd-sel"},
	}

	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, computev1alpha.AddToScheme(s))

	projectCl := fake.NewClientBuilder().WithScheme(s).WithObjects(projNSObj, wd).Build()

	t.Run("no federation client returns localCompanionWriter", func(t *testing.T) {
		t.Parallel()
		c := &ReferencedDataController{opts: ReferencedDataControllerOptions{}}
		w, err := c.writerFor(context.Background(), "test-cluster", projectCl, wd)
		require.NoError(t, err)
		_, ok := w.(*localCompanionWriter)
		assert.True(t, ok, "expected *localCompanionWriter when FederationClient is nil")
	})

	t.Run("with federation client returns downstreamCompanionWriter", func(t *testing.T) {
		t.Parallel()

		hubScheme := runtime.NewScheme()
		require.NoError(t, corev1.AddToScheme(hubScheme))
		hubCl := fake.NewClientBuilder().WithScheme(hubScheme).Build()

		c := &ReferencedDataController{opts: ReferencedDataControllerOptions{FederationClient: hubCl}}
		w, err := c.writerFor(context.Background(), "test-cluster", projectCl, wd)
		require.NoError(t, err)
		dsw, ok := w.(*downstreamCompanionWriter)
		require.True(t, ok, "expected *downstreamCompanionWriter when FederationClient is set")
		assert.Equal(t, testKarmadaNSStr, dsw.downstreamNamespace,
			"downstream namespace must be ns-{project-uid}")
	})
}

// ─── Conflict-tolerant finalizer ─────────────────────────────────────────────

// TestReferencedData_AddFinalizer_ConflictRetried asserts that the finalizer
// add path survives a single optimistic-lock conflict (as would occur when the
// WorkloadDeploymentFederator concurrently adds its own finalizer) and still
// stamps the finalizer on the object after retrying.
func TestReferencedData_AddFinalizer_ConflictRetried(t *testing.T) {
	ns := rdTestNamespace
	cmName := rdTestAppConfig

	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmName},
		Data:       map[string]string{"key": "value"},
	}
	wd := makeWD(ns, "wd-conflict", templateWithConfigMap(cmName))

	s := rdTestScheme(t)
	require.NoError(t, corev1.AddToScheme(s))

	realCl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(srcCM, wd).
		WithStatusSubresource(wd).
		Build()

	// Intercept the first Update call and return a conflict; let subsequent
	// calls pass through to the real fake client so the retry succeeds.
	var updateCalls atomic.Int32
	wdGR := schema.GroupResource{Group: "compute.datumapis.com", Resource: "workloaddeployments"}
	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(srcCM, wd).
		WithStatusSubresource(wd).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*computev1alpha.WorkloadDeployment); ok {
					if updateCalls.Add(1) == 1 {
						// Simulate the conflict the federator would cause.
						return apierrors.NewConflict(wdGR, obj.GetName(),
							fmt.Errorf("the object has been modified; please apply your changes to the latest version and try again"))
					}
					// Subsequent calls pass through to the real client.
					return realCl.Update(ctx, obj, opts...)
				}
				return c.Update(ctx, obj, opts...)
			},
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				// Always read from the real client so retries see the latest state.
				return realCl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	clusterName := "test-cluster"
	mgr := &fakeMCManager{
		clusters: map[string]cluster.Cluster{
			clusterName: &fakeCluster{cl: cl},
		},
	}
	c := &ReferencedDataController{
		mgr:  mgr,
		opts: ReferencedDataControllerOptions{},
	}

	// First reconcile: should add the finalizer despite the initial conflict.
	cn := multicluster.ClusterName(clusterName)
	ctx := mccontext.WithCluster(context.Background(), cn)
	_, err := c.Reconcile(ctx, mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "wd-conflict"}},
		ClusterName: cn,
	})
	require.NoError(t, err, "reconcile must succeed even when the first Update conflicts")

	// Verify the finalizer was added to the real object.
	updated := getWD(t, realCl, types.NamespacedName{Namespace: ns, Name: "wd-conflict"})
	assert.Contains(t, updated.Finalizers, referencedDataFinalizer,
		"finalizer must be present after conflict-retried Update")

	// Update was called at least twice (once for the conflict, once for the retry).
	assert.GreaterOrEqual(t, int(updateCalls.Load()), 2,
		"Update must have been called at least twice (conflict + retry)")
}

// TestReferencedData_RemoveFinalizer_ConflictRetried asserts that the finalizer
// removal path (on WD deletion) survives an optimistic-lock conflict.
func TestReferencedData_RemoveFinalizer_ConflictRetried(t *testing.T) {
	ns := rdTestNamespace
	cmName := rdTestAppConfig
	companionName := referenceddata.CompanionName("ConfigMap", cmName)

	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmName},
		Data:       map[string]string{"key": "value"},
	}
	wd := makeWD(ns, "wd-del-conflict", templateWithConfigMap(cmName))

	s := rdTestScheme(t)
	require.NoError(t, corev1.AddToScheme(s))

	// Build the WD to a state where the finalizer and companion are already present.
	realCl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(srcCM, wd).
		WithStatusSubresource(wd).
		Build()

	{
		cn := multicluster.ClusterName("setup")
		ctx := mccontext.WithCluster(context.Background(), cn)
		setupMgr := &fakeMCManager{
			clusters: map[string]cluster.Cluster{
				"setup": &fakeCluster{cl: realCl},
			},
		}
		setupC := &ReferencedDataController{mgr: setupMgr, opts: ReferencedDataControllerOptions{}}
		req := mcreconcile.Request{
			Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "wd-del-conflict"}},
			ClusterName: cn,
		}
		// Reconcile twice: first stamps finalizer, second materialises companion.
		_, err := setupC.Reconcile(ctx, req)
		require.NoError(t, err)
		_, err = setupC.Reconcile(ctx, req)
		require.NoError(t, err)
	}

	// Verify setup: finalizer present, companion exists.
	wdObj := getWD(t, realCl, types.NamespacedName{Namespace: ns, Name: "wd-del-conflict"})
	require.Contains(t, wdObj.Finalizers, referencedDataFinalizer)
	getCompanionCM(t, realCl, ns, companionName)

	// Now delete the WD.
	require.NoError(t, realCl.Delete(context.Background(), wdObj))

	// Intercept the first Update during deletion and return a conflict.
	var updateCalls atomic.Int32
	wdGR := schema.GroupResource{Group: "compute.datumapis.com", Resource: "workloaddeployments"}
	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*computev1alpha.WorkloadDeployment); ok {
					if updateCalls.Add(1) == 1 {
						return apierrors.NewConflict(wdGR, obj.GetName(),
							fmt.Errorf("the object has been modified; please apply your changes to the latest version and try again"))
					}
					return realCl.Update(ctx, obj, opts...)
				}
				return c.Update(ctx, obj, opts...)
			},
			Get: func(ctx context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				return realCl.Get(ctx, key, obj, opts...)
			},
			Delete: func(ctx context.Context, _ client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				return realCl.Delete(ctx, obj, opts...)
			},
		}).
		Build()

	cn := multicluster.ClusterName("del-cluster")
	mgr := &fakeMCManager{
		clusters: map[string]cluster.Cluster{
			"del-cluster": &fakeCluster{cl: cl},
		},
	}
	c := &ReferencedDataController{mgr: mgr, opts: ReferencedDataControllerOptions{}}
	ctx := mccontext.WithCluster(context.Background(), cn)
	req := mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "wd-del-conflict"}},
		ClusterName: cn,
	}

	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err, "reconcile must succeed even when the first Update conflicts during deletion")

	// When all finalizers are removed the fake client GCs the object immediately,
	// so we expect either: (a) the object is gone, or (b) it exists without our
	// finalizer. Both outcomes confirm the finalizer was removed correctly.
	var finalObj computev1alpha.WorkloadDeployment
	getErr := realCl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "wd-del-conflict"}, &finalObj)
	if getErr == nil {
		assert.NotContains(t, finalObj.Finalizers, referencedDataFinalizer,
			"finalizer must be removed after conflict-retried Update on deletion")
	} else {
		require.True(t, apierrors.IsNotFound(getErr),
			"expected object to be gone or exist without finalizer, got: %v", getErr)
	}

	assert.GreaterOrEqual(t, int(updateCalls.Load()), 2,
		"Update must have been called at least twice during deletion (conflict + retry)")
}

// ─── Regression: two WDs sharing a source, interleaved reconciles ─────────────

// TestReferencedData_RefCount_ConcurrentInterleaved verifies that when two WDs
// share the same source ConfigMap and their reconciles are interleaved, both
// ref-count entries are preserved and the companion is not orphaned or deleted.
//
// This is the regression test for the ref-count race (fix 2).
func TestReferencedData_RefCount_ConcurrentInterleaved(t *testing.T) {
	ns := rdTestNamespace
	cmName := "shared-concurrent-config"
	companionName := referenceddata.CompanionName("ConfigMap", cmName)

	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmName},
		Data:       map[string]string{"k": "v"},
	}
	wd1 := makeWD(ns, "wd-c1", templateWithConfigMap(cmName))
	wd2 := makeWD(ns, "wd-c2", templateWithConfigMap(cmName))

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(srcCM, wd1, wd2).
		WithStatusSubresource(wd1, wd2).
		Build()

	c, clusterName := newRDController(t, cl, nil)

	// Stamp finalizers for both WDs.
	reconcileWD(t, c, clusterName, ns, "wd-c1")
	reconcileWD(t, c, clusterName, ns, "wd-c2")

	// Interleave: wd-c1 writes companion (with its ref), then wd-c2 should
	// read the companion fresh and add its own ref — not start from scratch.
	reconcileWD(t, c, clusterName, ns, "wd-c1")
	reconcileWD(t, c, clusterName, ns, "wd-c2")

	companion := getCompanionCM(t, cl, ns, companionName)
	refs, err := decodeRefCount(companion.Annotations)
	require.NoError(t, err)
	assert.Len(t, refs, 2, "both WD ref-count entries must be present after interleaved reconciles")
	assert.Contains(t, refs, types.NamespacedName{Namespace: ns, Name: "wd-c1"}.String())
	assert.Contains(t, refs, types.NamespacedName{Namespace: ns, Name: "wd-c2"}.String())

	// Delete wd-c1; companion must survive with only wd-c2's entry.
	wd1 = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-c1"})
	require.NoError(t, cl.Delete(context.Background(), wd1))
	reconcileWD(t, c, clusterName, ns, "wd-c1")

	companion = getCompanionCM(t, cl, ns, companionName)
	refs2, err := decodeRefCount(companion.Annotations)
	require.NoError(t, err)
	assert.Len(t, refs2, 1, "companion must survive with wd-c2 still referencing it")
	assert.Contains(t, refs2, types.NamespacedName{Namespace: ns, Name: "wd-c2"}.String(),
		"wd-c2 entry must remain in ref-count after wd-c1 deletion")
}

// ─── Regression: optional missing source → WD not failed ──────────────────────

// TestReferencedData_OptionalMissingSource_Skipped verifies that a source
// marked optional=true that does not exist is silently skipped: the WD is NOT
// set to Failed/SourceNotFound, and the companion for the optional source is
// not expected.
//
// This is the regression test for the optional source escape hatch (fix 3).
func TestReferencedData_OptionalMissingSource_Skipped(t *testing.T) {
	ns := rdTestNamespace

	// A required ConfigMap that exists.
	cmRequired := "required-config"
	cmOptional := "optional-config"
	companionRequired := referenceddata.CompanionName("ConfigMap", cmRequired)

	srcRequired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: cmRequired},
		Data:       map[string]string{"key": "value"},
	}

	// Template references both: required (no optional flag) and optional.
	optionalTrue := true
	_ = optionalTrue
	template := computev1alpha.InstanceTemplateSpec{
		Spec: computev1alpha.InstanceSpec{
			Volumes: []computev1alpha.InstanceVolume{
				{
					Name: "req-vol",
					VolumeSource: computev1alpha.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: cmRequired},
						},
					},
				},
				{
					Name: "opt-vol",
					VolumeSource: computev1alpha.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: cmOptional},
							Optional:             &[]bool{true}[0],
						},
					},
				},
			},
		},
	}
	wd := makeWD(ns, "wd-opt", template)

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(srcRequired, wd).
		WithStatusSubresource(wd).
		Build()

	c, clusterName := newRDController(t, cl, nil)

	// Two passes: first stamps finalizer, second resolves.
	reconcileWD(t, c, clusterName, ns, "wd-opt")
	reconcileWD(t, c, clusterName, ns, "wd-opt")

	wd = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-opt"})

	// The WD must NOT have a False/SourceNotFound condition.
	cond := apimeta.FindStatusCondition(wd.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond, "ReferencedDataReady condition must be set")
	assert.Equal(t, metav1.ConditionTrue, cond.Status,
		"WD must be Ready when only optional sources are missing")
	assert.Equal(t, computev1alpha.ReferencedDataReasonReady, cond.Reason)

	// The required companion must exist.
	_ = getCompanionCM(t, cl, ns, companionRequired)

	// The optional companion must NOT exist.
	var phantom corev1.ConfigMap
	err := cl.Get(context.Background(),
		types.NamespacedName{Namespace: ns, Name: referenceddata.CompanionName("ConfigMap", cmOptional)}, &phantom)
	assert.Error(t, err, "companion for optional missing source must not be created")

	// The expected-set annotation must only list the required companion.
	expectedNames := decodeExpectedAnnotation(t, wd)
	assert.Equal(t, []string{companionRequired}, expectedNames)
}

// ─── Regression: unparseable ref-count annotation → companion NOT deleted ─────

// TestReferencedData_CorruptRefCount_NotDeleted verifies that when the
// ref-count annotation on a companion is unparseable, the release path returns
// an error (transient) and does NOT delete the companion. This guards against
// data loss when the annotation is corrupt but other WDs may still reference
// the companion.
//
// This is the regression test for fix 4.
func TestReferencedData_CorruptRefCount_NotDeleted(t *testing.T) {
	ns := rdTestNamespace
	cmName := "shared-config-corrupt"
	companionName := referenceddata.CompanionName("ConfigMap", cmName)
	wdKey := types.NamespacedName{Namespace: ns, Name: "wd-corrupt"}.String()

	// Companion already exists with a corrupt ref-count annotation.
	companion := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      companionName,
			Labels:    map[string]string{computev1alpha.ReferencedDataLabel: "true"},
			Annotations: map[string]string{
				companionRefCountAnnotation: `{not-valid-json}`,
			},
		},
		Data: map[string]string{"k": "v"},
	}

	cl := fake.NewClientBuilder().
		WithScheme(rdTestScheme(t)).
		WithObjects(companion).
		Build()

	// Use a localCompanionWriter backed by the fake client.
	writer := &localCompanionWriter{cl: cl}

	ctrl := &ReferencedDataController{}
	err := ctrl.releaseOneCompanion(context.Background(), nil, writer, ns, companionName, wdKey)
	assert.Error(t, err, "corrupt ref-count annotation must cause releaseOneCompanion to return an error")
	assert.Contains(t, err.Error(), "corrupt ref-count",
		"error message must mention corrupt ref-count")

	// Companion must NOT have been deleted.
	var still corev1.ConfigMap
	getErr := cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: companionName}, &still)
	assert.NoError(t, getErr, "companion must NOT be deleted when ref-count annotation is corrupt")
}

// ─── Regression: federator status sync preserves ReferencedDataReady ──────────

// TestFederator_StatusSync_PreservesReferencedDataReadyCondition verifies that
// syncStatusFromDownstream does NOT overwrite the resolver-owned
// ReferencedDataReady condition on the project WD with the downstream WD's
// (empty or stale) copy.
//
// This is the regression test for fix 1.
func TestFederator_StatusSync_PreservesReferencedDataReadyCondition(t *testing.T) {
	t.Parallel()

	// Project WD has the resolver's ReferencedDataReady=True condition.
	resolverCond := metav1.Condition{
		Type:    computev1alpha.ReferencedDataReady,
		Status:  metav1.ConditionTrue,
		Reason:  computev1alpha.ReferencedDataReasonReady,
		Message: "All 1 referenced companion(s) are materialised",
	}

	wd := testWorkloadDeployment(withFinalizer, func(w *computev1alpha.WorkloadDeployment) {
		w.Status.Conditions = []metav1.Condition{resolverCond}
	})
	projectClient := newProjectFakeClient(testProjectNamespace(), wd)

	// Downstream WD has NO ReferencedDataReady condition (as would be the case
	// when the cell hasn't set it, or when it was never populated downstream).
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
		Status: computev1alpha.WorkloadDeploymentStatus{
			// Downstream status has replica counts but NO ReferencedDataReady condition.
		},
	}

	karmadaClient := newKarmadaFakeClient(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testKarmadaNSStr}},
		karmadaWD,
	)

	r := newTestFederator(projectClient, karmadaClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)

	// After reconcile, the project WD must still have ReferencedDataReady=True.
	var updatedWD computev1alpha.WorkloadDeployment
	require.NoError(t, projectClient.Get(context.Background(),
		types.NamespacedName{Name: testWDName, Namespace: testProjNS}, &updatedWD))

	cond := apimeta.FindStatusCondition(updatedWD.Status.Conditions, computev1alpha.ReferencedDataReady)
	require.NotNil(t, cond, "ReferencedDataReady condition must still be present after federator status sync")
	assert.Equal(t, metav1.ConditionTrue, cond.Status,
		"resolver's ReferencedDataReady=True must be preserved by federator status sync")
	assert.Equal(t, computev1alpha.ReferencedDataReasonReady, cond.Reason,
		"resolver's Ready reason must be preserved by federator status sync")
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// stubReader allows individual test cases to inject custom reader behaviour.
type stubReader struct {
	getCM     func(ctx context.Context, projectID, namespace, name string) (*corev1.ConfigMap, error)
	getSecret func(ctx context.Context, projectID, namespace, name string) (*corev1.Secret, error)
}

func (s *stubReader) GetConfigMap(ctx context.Context, projectID, namespace, name string) (*corev1.ConfigMap, error) {
	if s.getCM != nil {
		return s.getCM(ctx, projectID, namespace, name)
	}
	return nil, fmt.Errorf("%w: ConfigMap %s", referenceddata.ErrSourceNotFound, name)
}

func (s *stubReader) GetSecret(ctx context.Context, projectID, namespace, name string) (*corev1.Secret, error) {
	if s.getSecret != nil {
		return s.getSecret(ctx, projectID, namespace, name)
	}
	return nil, fmt.Errorf("%w: Secret %s", referenceddata.ErrSourceNotFound, name)
}
