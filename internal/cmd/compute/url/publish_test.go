// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// recorder records the order of writes, which is the part of publishing that
// has to be right: the service exists before anything references it.
type recorder struct {
	creates []string
	updates []string
	deletes []string
}

func (r *recorder) funcs() interceptor.Funcs {
	return interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			r.creates = append(r.creates, kindOf(obj))
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			r.updates = append(r.updates, kindOf(obj))
			return c.Update(ctx, obj, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			r.deletes = append(r.deletes, kindOf(obj))
			return c.Delete(ctx, obj, opts...)
		},
	}
}

func kindOf(obj client.Object) string {
	switch obj.(type) {
	case *networkingv1alpha.NetworkService:
		return kindService
	case *networkingv1alpha.HTTPProxy:
		return kindProxy
	default:
		return "other"
	}
}

func TestPublishCreatesBackendsBeforeTheProxy(t *testing.T) {
	rec := &recorder{}
	c := interceptor.NewClient(newFakeClient(t), rec.funcs())

	// Nothing ever reports the URL live here, so the wait runs until the
	// context is cancelled — the Ctrl-C path.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	var out bytes.Buffer
	info, err := Publish(ctx, &out, c, workloadNamed(testWorkloadName), testPortName, 8080, nil)
	if err != nil {
		t.Fatalf("detaching is not an error, got: %v", err)
	}
	if info != nil {
		t.Errorf("info = %+v, want nil when we stopped watching", info)
	}

	want := []string{kindService, kindProxy}
	if len(rec.creates) != 2 || rec.creates[0] != want[0] || rec.creates[1] != want[1] {
		t.Fatalf("creates = %v, want %v — a proxy naming a missing service reports a broken backend", rec.creates, want)
	}

	if !strings.Contains(out.String(), "Detached") {
		t.Errorf("output = %q, want a detach note", out.String())
	}
	if !strings.Contains(out.String(), "datumctl compute workloads describe api") {
		t.Errorf("output = %q, want a pointer to `datumctl compute workloads describe`", out.String())
	}

	var svc networkingv1alpha.NetworkService
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: util.ResourceNamespace, Name: testWorkloadName}, &svc); err != nil {
		t.Fatalf("network service was not created: %v", err)
	}
	if svc.Spec.Ports[0].Port != 8080 {
		t.Errorf("port = %d, want 8080", svc.Spec.Ports[0].Port)
	}

	var proxy networkingv1alpha.HTTPProxy
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: util.ResourceNamespace, Name: testWorkloadName}, &proxy); err != nil {
		t.Fatalf("proxy was not created: %v", err)
	}
	if proxy.Spec.Rules[0].Backends[0].NetworkService.Name != testWorkloadName {
		t.Errorf("backend = %+v, want a reference to the service", proxy.Spec.Rules[0].Backends[0])
	}
}

func TestPublishReturnsWhenTheURLIsLive(t *testing.T) {
	rec := &recorder{}
	c := interceptor.NewClient(newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 2, 2, true), location("IAD", 2, 2, true)),
	), rec.funcs())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var out bytes.Buffer
	info, err := Publish(ctx, &out, c, workloadNamed(testWorkloadName), testPortName, 8080, nil)
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if info == nil || info.URL != testCanonicalURL {
		t.Fatalf("info = %+v, want the live URL", info)
	}

	// Already published and unchanged: no writes at all.
	if len(rec.creates) != 0 || len(rec.updates) != 0 {
		t.Errorf("creates = %v, updates = %v, want none for an unchanged workload", rec.creates, rec.updates)
	}

	got := out.String()
	for _, want := range []string{
		"Backends     4 healthy across DFW, IAD",
		"Edge         programmed",
		"Certificate  issued",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	// The URL is the caller's line to print, not this package's.
	if strings.Contains(got, "https://") {
		t.Errorf("Publish should not print the URL itself:\n%s", got)
	}
}

func TestPublishUpdatesAChangedPort(t *testing.T) {
	rec := &recorder{}
	c := interceptor.NewClient(newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)),
	), rec.funcs())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := Publish(ctx, &bytes.Buffer{}, c, workloadNamed(testWorkloadName), testPortName, 9090, nil); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	var svc networkingv1alpha.NetworkService
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: util.ResourceNamespace, Name: testWorkloadName}, &svc); err != nil {
		t.Fatalf("getting service: %v", err)
	}
	if svc.Spec.Ports[0].Port != 9090 {
		t.Errorf("port = %d, want the new port 9090", svc.Spec.Ports[0].Port)
	}
	if len(rec.updates) != 1 || rec.updates[0] != kindService {
		t.Errorf("updates = %v, want the service alone", rec.updates)
	}
}

func TestPublishAdoptsAnExistingUnlabelledObject(t *testing.T) {
	// An object written before the labels existed must be brought in line, or
	// lookups would never find it again.
	svc := BuildNetworkService(workloadNamed(testWorkloadName), testPortName, 8080)
	svc.Labels = nil
	svc.OwnerReferences = nil
	proxy := BuildHTTPProxy(workloadNamed(testWorkloadName), testPortName, nil)
	proxy.Labels = nil
	proxy.OwnerReferences = nil

	c := newFakeClient(t, svc, proxy)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := Publish(ctx, &bytes.Buffer{}, c, workloadNamed(testWorkloadName), testPortName, 8080, nil); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	var got networkingv1alpha.NetworkService
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: util.ResourceNamespace, Name: testWorkloadName}, &got); err != nil {
		t.Fatalf("getting service: %v", err)
	}
	assertPublishedLabels(t, got.Labels, workloadNamed(testWorkloadName))
	assertOwnerRef(t, got.OwnerReferences, workloadNamed(testWorkloadName))
}

func TestUnpublishRemovesTheProxyFirst(t *testing.T) {
	rec := &recorder{}
	c := interceptor.NewClient(newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)),
		publishedProxy("web", "e5f6a7b8.datumproxy.net"),
		publishedService("web", 3000, location("DFW", 1, 1, true)),
	), rec.funcs())

	if err := Unpublish(context.Background(), c, testWorkloadName); err != nil {
		t.Fatalf("Unpublish returned error: %v", err)
	}

	want := []string{kindProxy, kindService}
	if len(rec.deletes) != 2 || rec.deletes[0] != want[0] || rec.deletes[1] != want[1] {
		t.Fatalf("deletes = %v, want %v — removing the backends first leaves the proxy reporting a missing backend", rec.deletes, want)
	}

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}
	if info != nil {
		t.Errorf("api still published: %+v", info)
	}

	// Another workload's URL is untouched.
	other, err := ForWorkload(context.Background(), c, "web")
	if err != nil || other == nil {
		t.Fatalf("web should be untouched, got info=%+v err=%v", other, err)
	}
}

// A no-op means no delete, not a delete the server happens to forgive: the
// read is what keeps a destroy with no URL from needing delete permission it
// never had to use.
func TestUnpublishIsANoOpWhenNothingIsPublished(t *testing.T) {
	c := interceptor.NewClient(newFakeClient(t), interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.DeleteOption) error {
			t.Errorf("deleted %s for a workload that published nothing", kindOf(obj))
			return nil
		},
	})

	if err := Unpublish(context.Background(), c, testWorkloadName); err != nil {
		t.Fatalf("unpublishing something that was never published is not an error, got: %v", err)
	}
}
