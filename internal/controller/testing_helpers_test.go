// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// ─── Scheme helpers ───────────────────────────────────────────────────────────

// newProjectScheme builds a runtime.Scheme with the types needed by the project
// cluster (corev1 + compute).
func newProjectScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = computev1alpha.AddToScheme(s)
	return s
}

// newKarmadaScheme builds a runtime.Scheme with the types needed by the Karmada
// API server (corev1 + compute + karmada policy).
func newKarmadaScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = computev1alpha.AddToScheme(s)
	_ = karmadapolicyv1alpha1.Install(s)
	return s
}

// newProjectFakeClient returns a fake client pre-populated with the given
// objects and the project scheme.
func newProjectFakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(newProjectScheme()).
		WithObjects(objs...).
		WithStatusSubresource(objs...).
		Build()
}

// newKarmadaFakeClient returns a fake client pre-populated with the given
// objects and the Karmada scheme.
func newKarmadaFakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(newKarmadaScheme()).
		WithObjects(objs...).
		Build()
}

// ─── Fake cluster.Cluster ─────────────────────────────────────────────────────

// fakeCluster is a minimal cluster.Cluster implementation for tests.
// Embeds the interface so only the methods we need are implemented.
type fakeCluster struct {
	cluster.Cluster // nil embed — panics if unimplemented methods are called
	cl              client.Client
}

func (f *fakeCluster) GetClient() client.Client   { return f.cl }
func (f *fakeCluster) GetScheme() *runtime.Scheme { return f.cl.Scheme() }
func (f *fakeCluster) GetAPIReader() client.Reader { return f.cl }

// newFakeCluster wraps a fake client in a fakeCluster.
func newFakeCluster(cl client.Client) *fakeCluster {
	return &fakeCluster{cl: cl}
}

// ─── Fake mcmanager.Manager ───────────────────────────────────────────────────

// fakeMCManager is a minimal mcmanager.Manager implementation that serves a
// fixed map of project clusters. Only GetCluster is implemented; all other
// Manager methods panic through the embedded nil interface.
type fakeMCManager struct {
	mcmanager.Manager // nil embed — panics if unimplemented methods are called
	clusters          map[string]cluster.Cluster
}

func (m *fakeMCManager) GetCluster(_ context.Context, name string) (cluster.Cluster, error) {
	if c, ok := m.clusters[name]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("cluster %q not found in fake manager", name)
}

// newFakeMCManager returns a fakeMCManager with a single named cluster.
func newFakeMCManager(clusterName string, cl cluster.Cluster) *fakeMCManager {
	return &fakeMCManager{
		clusters: map[string]cluster.Cluster{clusterName: cl},
	}
}
