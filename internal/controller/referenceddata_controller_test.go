// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
	refs1 := decodeRefCount(companion.Annotations)
	assert.Contains(t, refs1, types.NamespacedName{Namespace: ns, Name: "wd-1"}.String())

	// Materialise for wd-2.
	reconcileWD(t, c, clusterName, ns, "wd-2")
	reconcileWD(t, c, clusterName, ns, "wd-2")

	companion = getCompanionCM(t, cl, ns, companionName)
	refs2 := decodeRefCount(companion.Annotations)
	assert.Len(t, refs2, 2, "companion must list both WDs")
	assert.Contains(t, refs2, types.NamespacedName{Namespace: ns, Name: "wd-1"}.String())
	assert.Contains(t, refs2, types.NamespacedName{Namespace: ns, Name: "wd-2"}.String())

	// Delete wd-1 (simulate deletion + finalizer processing).
	wd1 = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-1"})
	require.NoError(t, cl.Delete(context.Background(), wd1))
	reconcileWD(t, c, clusterName, ns, "wd-1")

	// Companion must still exist (wd-2 still holds it).
	companion = getCompanionCM(t, cl, ns, companionName)
	refs3 := decodeRefCount(companion.Annotations)
	assert.Len(t, refs3, 1, "wd-1 should have been removed from ref-count")
	assert.Contains(t, refs3, types.NamespacedName{Namespace: ns, Name: "wd-2"}.String())

	// Delete wd-2 too.
	wd2 = getWD(t, cl, types.NamespacedName{Namespace: ns, Name: "wd-2"})
	require.NoError(t, cl.Delete(context.Background(), wd2))
	reconcileWD(t, c, clusterName, ns, "wd-2")

	// Companion must be gone.
	var gone corev1.ConfigMap
	err := cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: companionName}, &gone)
	assert.Error(t, err, "companion must be deleted when last WD is removed")
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
