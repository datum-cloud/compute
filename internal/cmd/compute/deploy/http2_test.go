// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

	t.Run("the flag wins over what the URL uses", func(t *testing.T) {
		got, err := resolveHTTP2(ctx, http2Client(t), testWorkload, 8080, true, false, false)
		if err != nil || got {
			t.Errorf("resolveHTTP2 = %v, %v; want false, nil — --http2=false turns it off", got, err)
		}
	})

	t.Run("without the flag a redeploy keeps HTTP/2", func(t *testing.T) {
		got, err := resolveHTTP2(ctx, http2Client(t), testWorkload, 8080, false, false, false)
		if err != nil || !got {
			t.Errorf("resolveHTTP2 = %v, %v; want true, nil — an image bump must not switch gRPC off", got, err)
		}
	})

	t.Run("without the flag a redeploy keeps HTTP/1.1", func(t *testing.T) {
		got, err := resolveHTTP2(ctx, publishedClient(t), testWorkload, 8080, false, false, false)
		if err != nil || got {
			t.Errorf("resolveHTTP2 = %v, %v; want false, nil", got, err)
		}
	})

	t.Run("a new workload does not inherit a leftover URL", func(t *testing.T) {
		got, err := resolveHTTP2(ctx, http2Client(t), testWorkload, 8080, false, false, true)
		if err != nil || got {
			t.Errorf("resolveHTTP2 = %v, %v; want false, nil", got, err)
		}
	})

	t.Run("http2 without an HTTP service is refused", func(t *testing.T) {
		_, err := resolveHTTP2(ctx, newFakeClient(t), testWorkload, 0, true, true, false)
		if !errors.Is(err, errHTTP2NeedsPort) {
			t.Errorf("error = %v, want errHTTP2NeedsPort", err)
		}
	})

	t.Run("turning it off without an HTTP service is not an error", func(t *testing.T) {
		got, err := resolveHTTP2(ctx, newFakeClient(t), testWorkload, 0, true, false, false)
		if err != nil || got {
			t.Errorf("resolveHTTP2 = %v, %v; want false, nil", got, err)
		}
	})

	t.Run("an unreadable control plane fails closed", func(t *testing.T) {
		boom := errors.New("connection refused")
		c := interceptor.NewClient(http2Client(t), interceptor.Funcs{
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return boom
			},
		})
		_, err := resolveHTTP2(ctx, c, testWorkload, 8080, false, false, false)
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
	planHTTPService(context.Background(), &out, newFakeClient(t), testWorkload, 9090, true, &options{httpPort: 9090}, true)

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
