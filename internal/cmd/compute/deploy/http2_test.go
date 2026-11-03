// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/url"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// http2Client is a published workload whose URL reaches it over HTTP/2.
func http2Client(t *testing.T) client.WithWatch {
	t.Helper()
	w := workload()
	return newFakeClient(t, liveProxy(w), url.BuildNetworkService(w, "http", 8080, true))
}

func TestResolveHTTP2(t *testing.T) {
	ctx := context.Background()
	declaring := func() *computev1alpha.Workload {
		w := workloadWithPorts(computev1alpha.NamedPort{Name: httpPortName, Port: 8080})
		return &w
	}
	resolve := func(t *testing.T, c client.Client, w *computev1alpha.Workload, creating bool, opts *options, set bool) httpPlan {
		t.Helper()
		got, err := resolveHTTP(ctx, c, w, creating, opts, set)
		if err != nil {
			t.Fatalf("resolveHTTP: %v", err)
		}
		return got
	}

	t.Run("the flag wins over what the URL uses", func(t *testing.T) {
		if got := resolve(t, http2Client(t), declaring(), false, &options{http2: false}, true); got.http2 || !got.publish {
			t.Errorf("resolveHTTP = %+v; want published over HTTP/1.1 — --http2=false turns it off", got)
		}
	})

	t.Run("without the flag a redeploy keeps HTTP/2", func(t *testing.T) {
		if got := resolve(t, http2Client(t), declaring(), false, &options{}, false); !got.http2 || !got.publish {
			t.Errorf("resolveHTTP = %+v; want published over HTTP/2 — an image bump must not switch gRPC off", got)
		}
	})

	t.Run("without the flag a redeploy keeps HTTP/1.1", func(t *testing.T) {
		if got := resolve(t, publishedClient(t), declaring(), false, &options{}, false); got.http2 {
			t.Errorf("resolveHTTP = %+v; want HTTP/1.1", got)
		}
	})

	t.Run("a new workload does not inherit a leftover URL", func(t *testing.T) {
		if got := resolve(t, http2Client(t), workload(), true, &options{httpPort: 8080}, false); got.http2 {
			t.Errorf("resolveHTTP = %+v; want HTTP/1.1", got)
		}
	})

	// --http2 asks for an HTTP service as plainly as --http-port does. On a
	// workload with a port and no URL, ignoring it would leave the user
	// believing gRPC is being served.
	t.Run("--http2 publishes a workload that has no URL yet", func(t *testing.T) {
		got := resolve(t, newFakeClient(t), declaring(), false, &options{http2: true}, true)
		if want := (httpPlan{port: 8080, http2: true, publish: true}); !reflect.DeepEqual(got, want) {
			t.Errorf("resolveHTTP = %+v, want %+v", got, want)
		}
	})

	t.Run("--http2=false is not a request to publish", func(t *testing.T) {
		if got := resolve(t, newFakeClient(t), declaring(), false, &options{}, true); got.publish {
			t.Errorf("resolveHTTP = %+v; turning HTTP/2 off must not publish a URL", got)
		}
	})

	t.Run("http2 without an HTTP service is refused", func(t *testing.T) {
		_, err := resolveHTTP(ctx, newFakeClient(t), workload(), false, &options{http2: true}, true)
		if !errors.Is(err, errHTTP2NeedsPort) {
			t.Errorf("error = %v, want errHTTP2NeedsPort", err)
		}
	})

	t.Run("turning it off without an HTTP service is not an error", func(t *testing.T) {
		if got := resolve(t, newFakeClient(t), workload(), false, &options{}, true); got.http2 || got.publish {
			t.Errorf("resolveHTTP = %+v; want nothing", got)
		}
	})

	t.Run("an unreadable control plane fails closed", func(t *testing.T) {
		boom := errors.New("connection refused")
		c := interceptor.NewClient(http2Client(t), interceptor.Funcs{
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return boom
			},
		})
		_, err := resolveHTTP(ctx, c, declaring(), false, &options{}, false)
		if !errors.Is(err, boom) {
			t.Errorf("error = %v, want the server's failure rather than a silent switch to HTTP/1.1", err)
		}
	})
}

func TestDeclareURLWritesHTTP2(t *testing.T) {
	c := newFakeClient(t)
	if err := declareURL(context.Background(), c, workload(), "http", 9090, true); err != nil {
		t.Fatalf("declareURL: %v", err)
	}

	var svc networkingv1alpha.NetworkService
	key := client.ObjectKey{Namespace: util.ResourceNamespace, Name: url.ResourceName(testWorkload)}
	if err := c.Get(context.Background(), key, &svc); err != nil {
		t.Fatalf("reading the service back: %v", err)
	}
	got := svc.Spec.Ports[0].AppProtocol
	if got == nil || *got != networkingv1alpha.NetworkServiceAppProtocolH2C {
		t.Errorf("appProtocol = %v, want %q", got, networkingv1alpha.NetworkServiceAppProtocolH2C)
	}
}

func TestPlanNamesHTTP2(t *testing.T) {
	var out bytes.Buffer
	planHTTPService(context.Background(), &out, newFakeClient(t), testWorkload, httpPlan{port: 9090, http2: true, publish: true}, &options{httpPort: 9090}, true)

	got := out.String()
	for _, want := range []string{
		"port 9090, HTTP/2 → Datum-managed URL",
		"Datum terminates TLS; serve plain HTTP/2 on this port.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan is missing %q:\n%s", want, got)
		}
	}
}
