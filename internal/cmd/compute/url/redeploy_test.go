// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// writeCounts records what a redeploy actually sends to the API server.
type writeCounts struct {
	creates int
	updates int
}

func countingClient(t *testing.T, counts *writeCounts, objs ...client.Object) client.WithWatch {
	t.Helper()
	return interceptor.NewClient(newFakeClient(t, objs...), interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			counts.creates++
			return cl.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			counts.updates++
			return cl.Update(ctx, obj, opts...)
		},
	})
}

// TestDeclareIsIdempotent: redeploying an unchanged workload must not try to
// create a URL that already exists, and must not churn the objects either. The
// first Declare creates both; a second identical one writes nothing at all.
//
// This is the shape of every redeploy — `deploy` calls Declare on each run,
// including the runs that only change the image.
func TestDeclareIsIdempotent(t *testing.T) {
	var counts writeCounts
	w := workloadNamed(testWorkloadName)
	c := countingClient(t, &counts)

	if err := Declare(context.Background(), c, w, "http", 8080, nil); err != nil {
		t.Fatalf("first declare: %v", err)
	}
	if counts.creates != 2 {
		t.Fatalf("creates = %d, want 2 (the backends and the URL)", counts.creates)
	}
	if counts.updates != 0 {
		t.Errorf("updates = %d on a first declare, want 0", counts.updates)
	}

	counts = writeCounts{}
	if err := Declare(context.Background(), c, w, "http", 8080, nil); err != nil {
		t.Fatalf("second declare: %v", err)
	}
	if counts.creates != 0 {
		t.Errorf("creates = %d on redeploy, want 0 — a URL that exists must not be created again", counts.creates)
	}
	if counts.updates != 0 {
		t.Errorf("updates = %d on an unchanged redeploy, want 0 — nothing changed, so nothing should be written", counts.updates)
	}
}

// TestDeclareUpdatesAChangedPort: the counterpart. Idempotence must not mean
// inertness — a workload that moves to another port has to take its URL with
// it, or the URL keeps routing to a port nothing answers on.
func TestDeclareUpdatesAChangedPort(t *testing.T) {
	var counts writeCounts
	w := workloadNamed(testWorkloadName)
	c := countingClient(t, &counts)

	if err := Declare(context.Background(), c, w, "http", 8080, nil); err != nil {
		t.Fatalf("first declare: %v", err)
	}

	counts = writeCounts{}
	if err := Declare(context.Background(), c, w, "http", 9090, nil); err != nil {
		t.Fatalf("redeclare on a new port: %v", err)
	}
	if counts.creates != 0 {
		t.Errorf("creates = %d, want 0 — the objects already exist", counts.creates)
	}
	if counts.updates == 0 {
		t.Error("a changed port must be written through to the backends")
	}

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if info == nil {
		t.Fatal("the workload lost its URL on redeploy")
	}
	if info.Port != 9090 {
		t.Errorf("port = %d, want 9090", info.Port)
	}
}

// TestDeclareKeepsTheCanonicalHostnameAcrossRedeploys: the managed URL is the
// one a developer has already shared and scripted against. A redeploy that
// replaced the HTTPProxy rather than updating it would issue a new
// <uid>.datumproxy.net and silently break every existing reference.
func TestDeclareKeepsTheCanonicalHostnameAcrossRedeploys(t *testing.T) {
	w := workloadNamed(testWorkloadName)
	c := newFakeClient(t)

	if err := Declare(context.Background(), c, w, "http", 8080, nil); err != nil {
		t.Fatalf("first declare: %v", err)
	}

	// Stand in for the platform assigning the canonical hostname.
	var proxy networkingv1alpha.HTTPProxy
	key := types.NamespacedName{Namespace: util.ResourceNamespace, Name: ResourceName(testWorkloadName)}
	if err := c.Get(context.Background(), key, &proxy); err != nil {
		t.Fatalf("reading the URL back: %v", err)
	}
	proxy.Status.CanonicalHostname = testCanonical
	if err := c.Status().Update(context.Background(), &proxy); err != nil {
		t.Fatalf("seeding the canonical hostname: %v", err)
	}

	if err := Declare(context.Background(), c, w, "http", 9090, nil); err != nil {
		t.Fatalf("redeclare: %v", err)
	}

	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if info == nil || info.CanonicalHostname != testCanonical {
		t.Fatalf("canonical hostname = %q, want %q — a redeploy must not reissue the URL",
			infoCanonical(info), testCanonical)
	}
}

func infoCanonical(i *Info) string {
	if i == nil {
		return ""
	}
	return i.CanonicalHostname
}
