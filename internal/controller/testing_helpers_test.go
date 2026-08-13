// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"sync"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// ─── Scheme helpers ───────────────────────────────────────────────────────────

// newProjectScheme builds a runtime.Scheme with the types needed by the project
// cluster (corev1 + compute).
func newProjectScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = autoscalingv2.AddToScheme(s)
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

func (f *fakeCluster) GetClient() client.Client    { return f.cl }
func (f *fakeCluster) GetScheme() *runtime.Scheme  { return f.cl.Scheme() }
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

func (m *fakeMCManager) GetCluster(_ context.Context, name multicluster.ClusterName) (cluster.Cluster, error) {
	if c, ok := m.clusters[string(name)]; ok {
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

// ─── Capturing events.EventRecorder ───────────────────────────────────────────

// recordedEvent captures every argument the reconciler passes to Eventf so
// tests can assert on fields the stock events.FakeRecorder discards.
type recordedEvent struct {
	Regarding, Related              runtime.Object
	EventType, Reason, Action, Note string
}

// capturingEventRecorder is a test double for events.EventRecorder. It emits the
// byte-identical "Type Reason Note" string on Events (so channel-based
// assertions match both the old record.FakeRecorder and the new
// events.FakeRecorder) while also recording the structured fields. The stock
// events.FakeRecorder drops action/related/regarding, yet the apiserver rejects
// an events.k8s.io/v1 event with an empty action — so without structured
// capture an empty-action regression would pass CI and be dropped in production.
type capturingEventRecorder struct {
	mu      sync.Mutex
	Events  chan string
	records []recordedEvent
}

var _ events.EventRecorder = (*capturingEventRecorder)(nil)

// newCapturingEventRecorder returns a recorder whose Events channel is buffered
// to buf entries.
func newCapturingEventRecorder(buf int) *capturingEventRecorder {
	return &capturingEventRecorder{Events: make(chan string, buf)}
}

func (c *capturingEventRecorder) Eventf(regarding, related runtime.Object, eventtype, reason, action, noteFmt string, args ...any) {
	note := fmt.Sprintf(noteFmt, args...)
	c.mu.Lock()
	c.records = append(c.records, recordedEvent{
		Regarding: regarding,
		Related:   related,
		EventType: eventtype,
		Reason:    reason,
		Action:    action,
		Note:      note,
	})
	c.mu.Unlock()
	if c.Events != nil {
		c.Events <- eventtype + " " + reason + " " + note
	}
}

// Recorded returns a copy of the events captured so far.
func (c *capturingEventRecorder) Recorded() []recordedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]recordedEvent, len(c.records))
	copy(out, c.records)
	return out
}

// LastRecorded returns the most recently captured event, or nil if none.
func (c *capturingEventRecorder) LastRecorded() *recordedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.records) == 0 {
		return nil
	}
	last := c.records[len(c.records)-1]
	return &last
}
