// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// failDeleteOf returns interceptor funcs that fail DeleteAllOf for one kind
// and pass everything else through, which is what a partial permission looks
// like from the CLI's side.
func failDeleteOf(kind string, boom error) interceptor.Funcs {
	return interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if kindOf(obj) == kind {
				return boom
			}
			return c.Delete(ctx, obj, opts...)
		},
	}
}

func objectExists(t *testing.T, c client.Client, obj client.Object) bool {
	t.Helper()
	err := c.Get(context.Background(), types.NamespacedName{Namespace: util.ResourceNamespace, Name: testWorkloadName}, obj)
	if err == nil {
		return true
	}
	if !notPublished(err) {
		t.Fatalf("reading %s back: %v", kindOf(obj), err)
	}
	return false
}

// TestUnpublishStopsWhenTheURLCannotBeRemoved: a partial delete has to be
// reported, and it has to stop. Removing the backends out from under a proxy
// that is still routing to them is the one ordering this package exists to
// prevent, so a failure on the proxy must not be followed by deleting the
// service anyway.
func TestUnpublishStopsWhenTheURLCannotBeRemoved(t *testing.T) {
	boom := errors.New("forbidden")
	c := interceptor.NewClient(newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)),
	), failDeleteOf(kindProxy, boom))

	err := Unpublish(context.Background(), c, testWorkloadName)
	if err == nil {
		t.Fatal("a URL that could not be removed must be reported")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the server's failure to survive wrapping", err)
	}
	if !strings.Contains(err.Error(), testWorkloadName) {
		t.Errorf("error = %q, want it to name the workload", err)
	}

	if !objectExists(t, c, &networkingv1alpha.NetworkService{}) {
		t.Error("the backends were deleted behind a proxy that is still routing to them")
	}
	// And the URL is still findable, so a retry has something to act on.
	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil || info == nil {
		t.Fatalf("the URL must remain visible for a retry, got info=%v err=%v", info, err)
	}
}

// The other half of a partial delete: the URL is gone and its backends are
// not. This is the one that leaves an object behind with no proxy pointing at
// it, so the error has to say which of the two failed.
func TestUnpublishReportsAFailureToRemoveTheBackends(t *testing.T) {
	boom := errors.New("forbidden")
	c := interceptor.NewClient(newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)),
	), failDeleteOf(kindService, boom))

	err := Unpublish(context.Background(), c, testWorkloadName)
	if err == nil {
		t.Fatal("backends that could not be removed must be reported")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the server's failure to survive wrapping", err)
	}
	if !strings.Contains(err.Error(), "backends") {
		t.Errorf("error = %q, want it to distinguish the backends from the URL itself", err)
	}

	if objectExists(t, c, &networkingv1alpha.HTTPProxy{}) {
		t.Error("the proxy should already be gone: it is deleted first")
	}
	if !objectExists(t, c, &networkingv1alpha.NetworkService{}) {
		t.Fatal("test is not exercising the leftover-backends case")
	}

	// The state this leaves behind: nothing that looks up a workload's URL can
	// see the leftover backends, because lookups key on the proxy.
	info, err := ForWorkload(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("ForWorkload returned error: %v", err)
	}
	if info != nil {
		t.Errorf("info = %+v, want nil — the proxy is gone", info)
	}
}

// A second Unpublish after a partial failure has to finish the job. This is
// the retry every caller advertises, and it is idempotent over the half that
// already succeeded.
func TestUnpublishRetryFinishesAPartialDelete(t *testing.T) {
	fail := true
	c := interceptor.NewClient(newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)),
	), interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if fail && kindOf(obj) == kindService {
				return errors.New("forbidden")
			}
			return cl.Delete(ctx, obj, opts...)
		},
	})

	if err := Unpublish(context.Background(), c, testWorkloadName); err == nil {
		t.Fatal("expected the first attempt to fail on the backends")
	}

	fail = false
	if err := Unpublish(context.Background(), c, testWorkloadName); err != nil {
		t.Fatalf("the retry must finish the job, got: %v", err)
	}
	if objectExists(t, c, &networkingv1alpha.NetworkService{}) {
		t.Error("the leftover backends survived the retry")
	}
}

// TestPublishFailsOnTheProxyAfterWritingTheBackends pins what a failed publish
// leaves behind, and that the error names the URL rather than the backends
// that did get written — a user reading it has to know which step failed.
func TestPublishFailsOnTheProxyAfterWritingTheBackends(t *testing.T) {
	boom := errors.New("admission webhook denied the request")
	c := interceptor.NewClient(newFakeClient(t), interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if kindOf(obj) == kindProxy {
				return boom
			}
			return cl.Create(ctx, obj, opts...)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var out bytes.Buffer
	info, err := Publish(ctx, &out, c, workloadNamed(testWorkloadName), testPortName, 8080, nil)
	if err == nil {
		t.Fatal("a proxy that could not be created must fail the publish")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the server's message to reach the user", err)
	}
	if !strings.Contains(err.Error(), "URL") {
		t.Errorf("error = %q, want it to say the URL is what failed", err)
	}
	if info != nil {
		t.Errorf("info = %+v, want nil", info)
	}
	if out.Len() != 0 {
		t.Errorf("nothing was published, so no progress may be printed:\n%s", out.String())
	}

	// A retry has to be able to succeed, so the half that was written stays.
	if !objectExists(t, c, &networkingv1alpha.NetworkService{}) {
		t.Error("the backends should remain, so a retry is an update and not a rebuild")
	}
}

// A failure to write the backends must stop before the proxy is created: a
// proxy naming a service that does not exist reports a broken backend, which
// is the spurious failure the write ordering exists to avoid.
func TestPublishDoesNotCreateAProxyWithoutBackends(t *testing.T) {
	boom := errors.New("quota exceeded")
	rec := &recorder{}
	c := interceptor.NewClient(newFakeClient(t), interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			rec.creates = append(rec.creates, kindOf(obj))
			if kindOf(obj) == kindService {
				return boom
			}
			return cl.Create(ctx, obj, opts...)
		},
	})

	if _, err := Publish(context.Background(), &bytes.Buffer{}, c, workloadNamed(testWorkloadName), testPortName, 8080, nil); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the create failure", err)
	}
	if len(rec.creates) != 1 || rec.creates[0] != kindService {
		t.Errorf("creates = %v, want the backends attempted and nothing after", rec.creates)
	}
	if objectExists(t, c, &networkingv1alpha.HTTPProxy{}) {
		t.Error("a proxy was created with no backends to point at")
	}
}

// TestPublishReportsAReadFailureRatherThanOverwriting: an existing object that
// cannot be read is not an object that can safely be replaced. Publishing has
// to stop, not fall through to a blind create or an update built on nothing.
func TestPublishReportsAReadFailureRatherThanOverwriting(t *testing.T) {
	boom := errors.New("connection reset")
	rec := &recorder{}
	c := interceptor.NewClient(newFakeClient(t), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return boom
		},
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			rec.creates = append(rec.creates, kindOf(obj))
			return cl.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			rec.updates = append(rec.updates, kindOf(obj))
			return cl.Update(ctx, obj, opts...)
		},
	})

	if _, err := Publish(context.Background(), &bytes.Buffer{}, c, workloadNamed(testWorkloadName), testPortName, 8080, nil); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the read failure", err)
	}
	if len(rec.creates) != 0 || len(rec.updates) != 0 {
		t.Errorf("creates = %v, updates = %v, want nothing written on an unreadable control plane", rec.creates, rec.updates)
	}
}

// TestPublishStopsWhenTheURLCanNeverBeRead: publishing treats a read failure
// as transient and polls again, which is right for the blip it was written
// for. Nothing escalates, though, so a failure that is not transient — a
// missing list permission is the everyday one — never becomes anything.
//
// The context here is unbounded on purpose: that is the one a deploy passes,
// and the only reason the rest of this package's tests do not hang on this is
// that they all pass a deadline.
func TestPublishStopsWhenTheURLCanNeverBeRead(t *testing.T) {
	c := interceptor.NewClient(newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)),
	), interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("httpproxies.networking.datumapis.com is forbidden")
		},
	})

	type result struct {
		info *Info
		err  error
		out  string
	}
	done := make(chan result, 1)
	go func() {
		var out bytes.Buffer
		info, err := Publish(context.Background(), &out, c, workloadNamed(testWorkloadName), testPortName, 8080, nil)
		done <- result{info, err, out.String()}
	}()

	select {
	case got := <-done:
		if got.err == nil && !strings.Contains(got.out, "forbidden") {
			t.Errorf("publishing gave up silently; output:\n%s", got.out)
		}
		if got.err != nil && !strings.Contains(got.err.Error(), "forbidden") {
			t.Errorf("error = %v, want the server's own words for why it gave up", got.err)
		}
		if got.info != nil {
			t.Errorf("info = %+v, want nil — the URL was never read", got.info)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Publish never returned: a deploy against a control plane it cannot read hangs indefinitely")
	}
}

// TestPublishRidesOutATransientReadFailure is the other half of giving up: a
// read that fails once must still be a blip. The objects were written a moment
// ago, and failing a deploy on the first hiccup would be worse than the hang
// this bound exists to stop.
func TestPublishRidesOutATransientReadFailure(t *testing.T) {
	var lists int
	c := interceptor.NewClient(newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)),
	), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			lists++
			if lists == 1 {
				return errors.New("etcdserver: request timed out")
			}
			return cl.List(ctx, list, opts...)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var out bytes.Buffer
	info, err := Publish(ctx, &out, c, workloadNamed(testWorkloadName), testPortName, 8080, nil)
	if err != nil {
		t.Fatalf("one failed read must not fail a publish, got: %v", err)
	}
	if info == nil || info.URL != testCanonicalURL {
		t.Fatalf("info = %+v, want the live URL on the next tick", info)
	}
	if strings.Contains(out.String(), "timed out") {
		t.Errorf("a blip was reported to the user:\n%s", out.String())
	}
}

// TestPublishDetachesWhenInterruptedMidWrite: Ctrl-C is a detach wherever it
// lands, including during the writes that precede the wait. Returning the
// cancelled context as an error exits 1 on a keystroke the command's own help
// says is safe.
func TestPublishDetachesWhenInterruptedMidWrite(t *testing.T) {
	for _, tc := range []struct{ name, kind string }{
		{"during the backends", kindService},
		{"during the URL", kindProxy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// The interrupt arrives while this write is in flight, so the write
			// fails with the cancelled context — exactly as a real client does.
			c := interceptor.NewClient(newFakeClient(t), interceptor.Funcs{
				Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if kindOf(obj) == tc.kind {
						cancel()
						return context.Canceled
					}
					return cl.Create(ctx, obj, opts...)
				},
			})

			var out bytes.Buffer
			info, err := Publish(ctx, &out, c, workloadNamed(testWorkloadName), testPortName, 8080, nil)
			if err != nil {
				t.Fatalf("detaching is not an error, got: %v", err)
			}
			if info != nil {
				t.Errorf("info = %+v, want nil when we stopped watching", info)
			}
			if !strings.Contains(out.String(), "Detached") {
				t.Errorf("output = %q, want the same detach note the wait prints", out.String())
			}
			if !strings.Contains(out.String(), "datumctl compute workloads describe "+testWorkloadName) {
				t.Errorf("output = %q, want a pointer to how to pick it up again", out.String())
			}
		})
	}
}

// TestUnpublishNeverDeletesACollection: nothing here deletes by selector.
// Deleting a collection is a permission of its own that project roles do not
// grant, so the call is refused for a user who can perfectly well delete each
// object — and it was refused on every destroy, including the destroy of a
// workload that never published a URL at all. A collection delete reaching the
// server is the bug, so this fails the moment one is attempted.
func TestUnpublishNeverDeletesACollection(t *testing.T) {
	noCollectionDeletes := func(t *testing.T) interceptor.Funcs {
		return interceptor.Funcs{
			DeleteAllOf: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.DeleteAllOfOption) error {
				t.Errorf("deleted a collection of %s: the server refuses deletecollection for a project role", kindOf(obj))
				return errors.New("deletecollection is forbidden")
			},
		}
	}

	t.Run("nothing published", func(t *testing.T) {
		c := interceptor.NewClient(newFakeClient(t), noCollectionDeletes(t))

		if err := Unpublish(context.Background(), c, testWorkloadName); err != nil {
			t.Fatalf("a workload with no URL has nothing to delete, got: %v", err)
		}
	})

	t.Run("a published URL", func(t *testing.T) {
		c := interceptor.NewClient(newFakeClient(t,
			publishedProxy(testWorkloadName, testCanonical),
			publishedService(testWorkloadName, 8080, location("DFW", 1, 1, true)),
		), noCollectionDeletes(t))

		if err := Unpublish(context.Background(), c, testWorkloadName); err != nil {
			t.Fatalf("Unpublish: %v", err)
		}
		if objectExists(t, c, &networkingv1alpha.HTTPProxy{}) {
			t.Error("the URL survived unpublishing")
		}
		if objectExists(t, c, &networkingv1alpha.NetworkService{}) {
			t.Error("the URL backends survived unpublishing")
		}
	})
}
