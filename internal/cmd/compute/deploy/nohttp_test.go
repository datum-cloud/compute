// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/url"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const testCustom = "api.example.com"

func publishedClient(t *testing.T, hostnames ...string) client.WithWatch {
	t.Helper()
	w := workload()
	proxy := liveProxy(w)
	for _, h := range hostnames {
		proxy.Spec.Hostnames = append(proxy.Spec.Hostnames, gatewayv1.Hostname(h))
		// A custom hostname only serves on the strength of its own entry here.
		// Without one the platform has not checked it yet, and a hostname
		// nobody has verified is not the URL to name in a plan.
		proxy.Status.HostnameStatuses = append(proxy.Status.HostnameStatuses,
			networkingv1alpha.HostnameStatus{
				Hostname: h,
				Conditions: []metav1.Condition{
					{Type: networkingv1alpha.HostnameConditionVerified, Status: metav1.ConditionTrue, Reason: "Verified"},
					{Type: networkingv1alpha.HostnameConditionDNSRecordProgrammed, Status: metav1.ConditionTrue, Reason: "RecordCreated"},
					{Type: networkingv1alpha.HostnameConditionCertificateReady, Status: metav1.ConditionTrue, Reason: "CertificateIssued"},
				},
			})
	}
	return newFakeClient(t, proxy, url.BuildNetworkService(w, "http", 8080))
}

func published(t *testing.T, c client.Client, obj client.Object) bool {
	t.Helper()
	return c.Get(context.Background(), client.ObjectKey{
		Namespace: util.ResourceNamespace, Name: url.ResourceName(testWorkload),
	}, obj) == nil
}

// TestPlanHTTPServiceStatesTheConsequence: --no-http takes a URL down, and the
// plan summary printed before the Apply prompt has to say which URL, by name.
// A user cannot consent to something the prompt does not mention.
func TestPlanHTTPServiceStatesTheConsequence(t *testing.T) {
	tests := []struct {
		name         string
		client       func(t *testing.T) client.WithWatch
		httpPort     int32
		opts         *options
		creating     bool
		wantLine     string
		wantMissing  string
		wantRemoving string
	}{
		{
			name:     "publishing names the port and says a URL is coming",
			client:   func(t *testing.T) client.WithWatch { return newFakeClient(t) },
			httpPort: 8080,
			opts:     &options{httpPort: 8080},
			wantLine: "HTTP service:        port 8080 → Datum-managed URL",
		},
		{
			name:         "removing names the URL that stops answering",
			client:       func(t *testing.T) client.WithWatch { return publishedClient(t) },
			opts:         &options{noHTTP: true},
			wantLine:     "HTTP service:        removed — https://" + testCanonical + " will stop responding",
			wantRemoving: "https://" + testCanonical,
		},
		{
			name:         "the custom hostname is the one named, since it is the one people use",
			client:       func(t *testing.T) client.WithWatch { return publishedClient(t, testCustom) },
			opts:         &options{noHTTP: true},
			wantLine:     "https://" + testCustom + " will stop responding",
			wantRemoving: "https://" + testCustom,
		},
		{
			name:     "removing something that was never published says so plainly",
			client:   func(t *testing.T) client.WithWatch { return newFakeClient(t) },
			opts:     &options{noHTTP: true},
			wantLine: "HTTP service:        removed",
		},
		{
			name:        "a workload being created has no URL to lose",
			client:      func(t *testing.T) client.WithWatch { return newFakeClient(t) },
			opts:        &options{noHTTP: true},
			creating:    true,
			wantMissing: "HTTP service",
		},
		{
			name:        "no HTTP flags at all is not an HTTP plan",
			client:      func(t *testing.T) client.WithWatch { return publishedClient(t) },
			opts:        &options{},
			wantMissing: "HTTP service",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got := planHTTPService(context.Background(), &out, tc.client(t), testWorkload, tc.httpPort, tc.opts, tc.creating)

			if tc.wantLine != "" && !strings.Contains(out.String(), tc.wantLine) {
				t.Errorf("plan missing %q:\n%s", tc.wantLine, out.String())
			}
			if tc.wantMissing != "" && strings.Contains(out.String(), tc.wantMissing) {
				t.Errorf("plan should not mention %q:\n%s", tc.wantMissing, out.String())
			}
			if got != tc.wantRemoving {
				t.Errorf("removed URL = %q, want %q", got, tc.wantRemoving)
			}
		})
	}
}

// A control plane that cannot be read must not stop a deploy at the plan
// stage: the line loses the hostname it would have named and nothing else.
func TestPlanHTTPServiceSurvivesAnUnreadableControlPlane(t *testing.T) {
	c := interceptor.NewClient(publishedClient(t), interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("connection refused")
		},
	})

	var out bytes.Buffer
	got := planHTTPService(context.Background(), &out, c, testWorkload, 0, &options{noHTTP: true}, false)

	if got != "" {
		t.Errorf("removed URL = %q, want none: nothing was read", got)
	}
	if !strings.Contains(out.String(), "HTTP service:        removed") {
		t.Errorf("the plan must still state the removal:\n%s", out.String())
	}
	if strings.Contains(out.String(), "will stop responding") {
		t.Errorf("no URL is known, so none may be named:\n%s", out.String())
	}
}

// TestRemoveHTTPServiceTakesBothObjectsDown is the whole point of --no-http:
// the workload stops declaring the port, and what answered for it goes with
// it. Leaving either object behind leaves a URL routing to a workload that no
// longer serves it.
func TestRemoveHTTPServiceTakesBothObjectsDown(t *testing.T) {
	c := publishedClient(t, testCustom)

	var out bytes.Buffer
	removedURL := "https://" + testCustom
	if err := removeHTTPService(context.Background(), &out, c, testWorkload, removedURL, false); err != nil {
		t.Fatalf("removeHTTPService: %v", err)
	}

	if published(t, c, &networkingv1alpha.HTTPProxy{}) {
		t.Error("the URL survived --no-http")
	}
	if published(t, c, &networkingv1alpha.NetworkService{}) {
		t.Error("the URL backends survived --no-http")
	}
	if want := "HTTP service removed — " + removedURL + " no longer responds"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

// Removing an HTTP service that was never there is a no-op with nothing to
// report — but only on a create. On an existing workload the user asked for
// something, so they are told it happened.
func TestRemoveHTTPServiceSaysNothingOnACreate(t *testing.T) {
	c := newFakeClient(t)

	var creating bytes.Buffer
	if err := removeHTTPService(context.Background(), &creating, c, testWorkload, "", true); err != nil {
		t.Fatalf("removeHTTPService: %v", err)
	}
	if creating.Len() != 0 {
		t.Errorf("a new workload never had an HTTP service, so nothing may be reported:\n%s", creating.String())
	}

	var existing bytes.Buffer
	if err := removeHTTPService(context.Background(), &existing, c, testWorkload, "", false); err != nil {
		t.Fatalf("removeHTTPService: %v", err)
	}
	if !strings.Contains(existing.String(), "HTTP service removed") {
		t.Errorf("an existing workload's removal must be reported:\n%s", existing.String())
	}
}

// A URL that could not be taken down is a failed deploy, not a warning: the
// workload has already been updated to stop serving, so a URL still routing to
// it is a live inconsistency the user has to know about.
func TestRemoveHTTPServiceReportsAFailure(t *testing.T) {
	boom := errors.New("forbidden")
	c := interceptor.NewClient(publishedClient(t), interceptor.Funcs{
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			return boom
		},
	})

	var out bytes.Buffer
	err := removeHTTPService(context.Background(), &out, c, testWorkload, "https://"+testCanonical, false)
	if err == nil {
		t.Fatal("a URL that could not be removed must fail the deploy")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the server's failure", err)
	}
	if strings.Contains(out.String(), "no longer responds") {
		t.Errorf("nothing was removed, so nothing may claim it was:\n%s", out.String())
	}
}

// TestPublishKeepsCustomHostnamesWhenTheSpecChanges is the carry-forward test
// with the short circuit removed: changing the port forces the apply through
// the update path that rewrites the proxy spec, which is where a dropped
// hostname would actually be lost. Republishing an unchanged workload writes
// nothing at all, so it cannot prove this on its own.
func TestPublishKeepsCustomHostnamesWhenTheSpecChanges(t *testing.T) {
	c := publishedClient(t, testCustom, "www.example.com")

	if err := declareURL(context.Background(), c, workload(), "http", 9090); err != nil {
		t.Fatalf("declareURL: %v", err)
	}

	var proxy networkingv1alpha.HTTPProxy
	key := client.ObjectKey{Namespace: util.ResourceNamespace, Name: url.ResourceName(testWorkload)}
	if err := c.Get(context.Background(), key, &proxy); err != nil {
		t.Fatalf("reading the proxy back: %v", err)
	}

	got := make([]string, 0, len(proxy.Spec.Hostnames))
	for _, h := range proxy.Spec.Hostnames {
		got = append(got, string(h))
	}
	if want := testCustom + "," + "www.example.com"; strings.Join(got, ",") != want {
		t.Errorf("hostnames after a port change = %v, want %q — a redeploy must not detach a custom domain", got, want)
	}

	// And the port change did land, so the test is exercising the update path.
	var svc networkingv1alpha.NetworkService
	if err := c.Get(context.Background(), key, &svc); err != nil {
		t.Fatalf("reading the backends back: %v", err)
	}
	if svc.Spec.Ports[0].Port != 9090 {
		t.Fatalf("port = %d, want the new port — the update path was not exercised", svc.Spec.Ports[0].Port)
	}
}

// TestExistingHostnames pins what the carry-forward reads, including the two
// cases where it deliberately reports none.
func TestExistingHostnames(t *testing.T) {
	t.Run("attached hostnames are carried forward in declared order", func(t *testing.T) {
		c := publishedClient(t, testCustom, "www.example.com")
		got, err := existingHostnames(context.Background(), c, testWorkload)
		if err != nil {
			t.Fatalf("existingHostnames: %v", err)
		}
		if want := testCustom + ",www.example.com"; strings.Join(got, ",") != want {
			t.Errorf("existingHostnames = %v, want %q", got, want)
		}
	})

	t.Run("a workload that was never published has none", func(t *testing.T) {
		got, err := existingHostnames(context.Background(), newFakeClient(t), testWorkload)
		if err != nil {
			t.Fatalf("a workload with no URL is not an error: %v", err)
		}
		if got != nil {
			t.Errorf("existingHostnames = %v, want nil", got)
		}
	})

	t.Run("an unreadable control plane fails closed", func(t *testing.T) {
		boom := errors.New("connection refused")
		c := interceptor.NewClient(publishedClient(t, testCustom), interceptor.Funcs{
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return boom
			},
		})
		got, err := existingHostnames(context.Background(), c, testWorkload)
		if err == nil {
			t.Fatal("a control plane that cannot be read must not report \"no custom domains\"")
		}
		if !errors.Is(err, boom) {
			t.Errorf("error = %v, want the server's failure", err)
		}
		if got != nil {
			t.Errorf("existingHostnames = %v, want nil alongside the error", got)
		}
	})
}

// TestDeclareURLDoesNotDetachHostnamesWhenTheLookupFails is the hole left in
// the carry-forward: existingHostnames reported none when it could not read the
// URL, and declaring then rewrote the proxy spec with none. The two calls use
// different verbs on the same object — a List for the carry-forward, a Get for
// the apply — so a control plane that answers one and refuses the other would
// detach every custom domain on the next deploy, while the deploy reported
// success.
//
// A missing list permission on httpproxies is the everyday shape of that.
func TestDeclareURLDoesNotDetachHostnamesWhenTheLookupFails(t *testing.T) {
	boom := errors.New("httpproxies.networking.datumapis.com is forbidden")
	c := interceptor.NewClient(publishedClient(t, testCustom), interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return boom
		},
	})

	err := declareURL(context.Background(), c, workload(), "http", 8080)
	if err == nil {
		t.Fatal("a deploy that could not read the attached domains must fail before it rewrites them")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the server's failure", err)
	}

	var proxy networkingv1alpha.HTTPProxy
	key := client.ObjectKey{Namespace: util.ResourceNamespace, Name: url.ResourceName(testWorkload)}
	if err := c.Get(context.Background(), key, &proxy); err != nil {
		t.Fatalf("reading the proxy back: %v", err)
	}
	if len(proxy.Spec.Hostnames) != 1 || string(proxy.Spec.Hostnames[0]) != testCustom {
		t.Errorf("hostnames = %v, want %q kept — a deploy that could not read the URL must not detach a domain",
			proxy.Spec.Hostnames, testCustom)
	}
}

// And the failure reaches the user as a failure to publish, after the rollout
// table, rather than as a silent success with a detached domain.
func TestPublishReportsACarryForwardFailure(t *testing.T) {
	boom := errors.New("httpproxies.networking.datumapis.com is forbidden")
	c := interceptor.NewClient(publishedClient(t, testCustom), interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return boom
		},
	})

	declareErr := declareURL(context.Background(), c, workload(), "http", 8080)

	var out bytes.Buffer
	err := publish(context.Background(), &out, c, workload(), 8080, &options{image: testImage}, declareErr)
	if err == nil {
		t.Fatal("a URL that was never declared must not be reported as published")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the server's failure", err)
	}
	if !strings.Contains(out.String(), "The rollout succeeded") {
		t.Errorf("a running workload must not read as a failed deploy:\n%s", out.String())
	}
}

// TestPublishSaysNothingMoreAfterNoHTTP: --no-http has just told the user, by
// name, that their URL is gone. Following that with "this workload is not
// reachable from the internet — to publish it..." answers a question nobody
// asked, and reads as though the removal were a mistake.
func TestPublishSaysNothingMoreAfterNoHTTP(t *testing.T) {
	var out bytes.Buffer
	if err := publish(context.Background(), &out, nil, workload(), 0, &options{noHTTP: true}, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("--no-http was already reported; nothing more may be said:\n%s", out.String())
	}
}

// TestManifestDeployReportsTheDeadEnd: the spec guarantees the not-reachable
// note for any workload with no HTTP port, and a manifest deploy is no
// exception. Printing it only on the flag path leaves a -f user to conclude
// the URL is somewhere they have not looked.
func TestManifestDeployReportsTheDeadEnd(t *testing.T) {
	t.Run("no port declared", func(t *testing.T) {
		w := workloadWithPorts()

		var out bytes.Buffer
		reportManifestReachability(&out, &w)

		for _, want := range []string{
			"No HTTP port declared — this workload is not reachable from the internet.",
			"To publish it:  datumctl compute deploy api --http-port 8080",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output missing %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("a declared http port is not a dead end", func(t *testing.T) {
		w := workloadWithPorts(computev1alpha.NamedPort{Name: httpPortName, Port: 8080})

		var out bytes.Buffer
		reportManifestReachability(&out, &w)

		if out.Len() != 0 {
			t.Errorf("a workload that declares an HTTP port is not unreachable:\n%s", out.String())
		}
	})
}

// TestPlanWarnsThatTheEdgeSpeaksPlaintext: the edge reaches instances over
// plaintext inside the network, so a container terminating TLS itself answers
// nothing. The only symptom is a URL that does not work, which is why this is
// said before the Apply prompt rather than left to be discovered.
func TestPlanWarnsThatTheEdgeSpeaksPlaintext(t *testing.T) {
	var out bytes.Buffer
	planHTTPService(context.Background(), &out, newFakeClient(t), testWorkload, 8080, &options{httpPort: 8080}, true)

	got := out.String()
	if !strings.Contains(got, "Datum terminates TLS; serve plain HTTP on this port.") {
		t.Errorf("the plan must say the edge reaches the container over plaintext:\n%s", got)
	}
	for _, machinery := range []string{"NetworkService", "HTTPProxy"} {
		if strings.Contains(got, machinery) {
			t.Errorf("the plan names the machinery %q:\n%s", machinery, got)
		}
	}

	// And it is not said to a workload that publishes nothing.
	var internal bytes.Buffer
	planHTTPService(context.Background(), &internal, newFakeClient(t), testWorkload, 0, &options{}, false)
	if strings.Contains(internal.String(), "TLS") {
		t.Errorf("nothing is being published, so TLS is not the user's problem:\n%s", internal.String())
	}
}
