// SPDX-License-Identifier: AGPL-3.0-only

package destroy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// clientFailingToDeleteBackends is the half of a partial delete that the
// existing tests do not cover: the URL comes down, the backends behind it do
// not. url.Unpublish deletes the proxy first, so this is the ordering a
// permission problem on one kind actually produces.
func clientFailingToDeleteBackends(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*networkingv1alpha.NetworkService); ok {
					return errors.New("forbidden")
				}
				return cl.Delete(ctx, obj, opts...)
			},
		}).
		Build()
}

// TestDestroyFailsWhenOnlyTheBackendsAreLeft: the workload and its URL are
// gone, the backends are not. The destroy reports the workload deleted, names
// the retry — and still fails, because something it was asked to remove is
// still there.
func TestDestroyFailsWhenOnlyTheBackendsAreLeft(t *testing.T) {
	c := clientFailingToDeleteBackends(t, testWorkloadObject(), publishedProxy(testCustom), publishedService())

	var out, errOut bytes.Buffer
	err := destroyWorkload(context.Background(), &out, &errOut, c, testProject, testWorkload, true)
	if err == nil {
		t.Fatal("leftover backends must fail the destroy")
	}

	if !strings.Contains(out.String(), "workload/api deleted.") {
		t.Errorf("stdout should still report the workload deleted:\n%s", out.String())
	}
	if exists(t, c, &networkingv1alpha.HTTPProxy{}) {
		t.Error("the URL should have been deleted before the backends were attempted")
	}
	if !exists(t, c, &networkingv1alpha.NetworkService{}) {
		t.Fatal("test is not exercising the leftover-backends case")
	}
	if !strings.Contains(errOut.String(), "datumctl compute destroy "+testWorkload) {
		t.Errorf("the message must name the command that finishes the job:\n%s", errOut.String())
	}
}

// TestDestroyRetryRemovesLeftoverBackends is the promise the message above
// makes, taken at its word: run destroy again and what was left behind is
// removed. The workload is gone and so is the proxy the URL lookup keys on, so
// the only trace left is the backends — and destroy still has to find and
// remove them, because no other command can.
func TestDestroyRetryRemovesLeftoverBackends(t *testing.T) {
	// The state the first run left: no workload, no proxy, backends still
	// there.
	c := newFakeClient(t, publishedService())

	var out, errOut bytes.Buffer
	if err := destroyWorkload(context.Background(), &out, &errOut, c, testProject, testWorkload, true); err != nil {
		t.Fatalf("the retry the message advertises must work, got: %v", err)
	}
	if exists(t, c, &networkingv1alpha.NetworkService{}) {
		t.Error("the leftover backends survived the retry, with no other command able to remove them")
	}
	if !strings.Contains(out.String(), "already deleted") {
		t.Errorf("output should say the workload was already gone:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Leftover URL backends for api deleted.") {
		t.Errorf("output should report exactly what it removed:\n%s", out.String())
	}
}

// TestDestroyRetryReportsWhatIsLeftBehind: the confirmation for a cleanup-only
// run has to describe what it is about to remove. When the proxy is gone there
// is no hostname left to name, so the summary says what remains in the user's
// vocabulary rather than printing nothing at all.
func TestDestroyRetryReportsWhatIsLeftBehind(t *testing.T) {
	c := newFakeClient(t, publishedService())

	var out, errOut bytes.Buffer
	if err := destroyWorkload(context.Background(), &out, &errOut, c, testProject, testWorkload, true); err != nil {
		t.Fatalf("destroyWorkload: %v", err)
	}
	if !strings.Contains(out.String(), "Leftovers:") {
		t.Errorf("the summary must name what is still there:\n%s", out.String())
	}
	for _, machinery := range []string{"NetworkService", "HTTPProxy"} {
		if strings.Contains(out.String(), machinery) {
			t.Errorf("output names the machinery %q:\n%s", machinery, out.String())
		}
	}
}

// The retry does work when it is the proxy that was left behind, because that
// is what the lookup keys on. Pinned so a fix for the case above is not read
// as a regression here.
func TestDestroyRetryRemovesALeftoverProxy(t *testing.T) {
	c := newFakeClient(t, publishedProxy(testCustom))

	var out, errOut bytes.Buffer
	if err := destroyWorkload(context.Background(), &out, &errOut, c, testProject, testWorkload, true); err != nil {
		t.Fatalf("destroyWorkload: %v", err)
	}
	if exists(t, c, &networkingv1alpha.HTTPProxy{}) {
		t.Error("the leftover URL survived the retry")
	}
	if !strings.Contains(out.String(), "URLs for api deleted.") {
		t.Errorf("output should report the URLs deleted:\n%s", out.String())
	}
}

// TestDestroyReportsAFailedWorkloadDelete: the workload itself failing to
// delete is a failed destroy, and nothing may be unpublished after it — a URL
// removed from under a workload that still exists takes a live service down
// for no reason.
func TestDestroyReportsAFailedWorkloadDelete(t *testing.T) {
	boom := errors.New("forbidden")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testWorkloadObject(), publishedProxy(testCustom), publishedService()).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*computev1alpha.Workload); ok {
					return boom
				}
				return cl.Delete(ctx, obj, opts...)
			},
		}).
		Build()

	var out, errOut bytes.Buffer
	err := destroyWorkload(context.Background(), &out, &errOut, c, testProject, testWorkload, true)
	if err == nil {
		t.Fatal("a workload that could not be deleted must fail the command")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the server's failure", err)
	}
	if !exists(t, c, &networkingv1alpha.HTTPProxy{}) {
		t.Error("the URL was taken down for a workload that is still running")
	}
	if !exists(t, c, &networkingv1alpha.NetworkService{}) {
		t.Error("the backends were taken down for a workload that is still running")
	}
}

// TestDestroySummaryWarnsWhenURLsCannotBeRead: a destroy whose URL lookup
// fails still deletes the workload, but the user has to be told the summary is
// incomplete rather than reading a missing URLs line as "there were none".
func TestDestroySummaryWarnsWhenURLsCannotBeRead(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testWorkloadObject(), publishedProxy(testCustom), publishedService()).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*networkingv1alpha.HTTPProxyList); ok {
					return errors.New("forbidden")
				}
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()

	var out, errOut bytes.Buffer
	if err := destroyWorkload(context.Background(), &out, &errOut, c, testProject, testWorkload, true); err != nil {
		t.Fatalf("an unreadable URL must not fail the destroy: %v", err)
	}

	if !strings.Contains(errOut.String(), "could not read URLs") {
		t.Errorf("stderr must say the summary is incomplete:\n%s", errOut.String())
	}
	if strings.Contains(out.String(), "URLs:") {
		t.Errorf("no URL is known, so none may be claimed:\n%s", out.String())
	}
	if exists(t, c, &computev1alpha.Workload{}) {
		t.Error("workload survived destroy")
	}
	// The delete itself does not depend on the lookup: the URL still goes.
	if exists(t, c, &networkingv1alpha.HTTPProxy{}) {
		t.Error("URL survived destroy")
	}
}
