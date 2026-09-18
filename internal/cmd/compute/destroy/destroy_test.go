// SPDX-License-Identifier: AGPL-3.0-only

package destroy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/url"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	testProject   = "acme-prod"
	testWorkload  = "api"
	testCanonical = "a1b2c3d4.datumproxy.net"
	testCustom    = "api.example.com"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := computev1alpha.AddToScheme(s); err != nil {
		t.Fatalf("registering compute scheme: %v", err)
	}
	if err := networkingv1alpha.AddToScheme(s); err != nil {
		t.Fatalf("registering networking scheme: %v", err)
	}
	return s
}

func testWorkloadObject() *computev1alpha.Workload {
	minReplicas := int32(2)
	return &computev1alpha.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkload,
			Namespace: util.ResourceNamespace,
			UID:       types.UID("uid-api"),
		},
		Spec: computev1alpha.WorkloadSpec{
			Placements: []computev1alpha.WorkloadPlacement{{
				Name:          "default",
				Locations:     []locationsv1alpha1.LocationReference{{Name: "us-east-1"}, {Name: "eu-west-1"}},
				ScaleSettings: computev1alpha.HorizontalScaleSettings{MinReplicas: minReplicas},
			}},
		},
	}
}

// publishedProxy is the proxy the platform reports for a live URL.
func publishedProxy(customHostnames ...string) *networkingv1alpha.HTTPProxy {
	hostnames := make([]gatewayv1.Hostname, 0, len(customHostnames))
	for _, h := range customHostnames {
		hostnames = append(hostnames, gatewayv1.Hostname(h))
	}
	return &networkingv1alpha.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkload,
			Namespace: util.ResourceNamespace,
			Labels:    map[string]string{computev1alpha.WorkloadNameLabel: testWorkload},
		},
		Spec: networkingv1alpha.HTTPProxySpec{Hostnames: hostnames},
		Status: networkingv1alpha.HTTPProxyStatus{
			CanonicalHostname: testCanonical,
			Conditions: []metav1.Condition{
				{Type: networkingv1alpha.HTTPProxyConditionProgrammed, Status: metav1.ConditionTrue, Reason: "Programmed"},
				{Type: networkingv1alpha.HTTPProxyConditionCertificatesReady, Status: metav1.ConditionTrue, Reason: "Issued"},
			},
		},
	}
}

func publishedService() *networkingv1alpha.NetworkService {
	return &networkingv1alpha.NetworkService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkload,
			Namespace: util.ResourceNamespace,
			Labels:    map[string]string{computev1alpha.WorkloadNameLabel: testWorkload},
		},
		Spec: networkingv1alpha.NetworkServiceSpec{
			Ports: []networkingv1alpha.NetworkServicePort{{Name: "http", Port: 8080}},
		},
	}
}

func newFakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
}

func exists(t *testing.T, c client.Client, obj client.Object) bool {
	t.Helper()
	err := c.Get(context.Background(), types.NamespacedName{Namespace: util.ResourceNamespace, Name: testWorkload}, obj)
	if err == nil {
		return true
	}
	if k8serrors.IsNotFound(err) {
		return false
	}
	t.Fatalf("get: %v", err)
	return false
}

// TestDestroyPrompt pins the sentence the user is asked to agree to. It has to
// name the URLs: a workload's URL is the part of a destroy a user is most
// likely not to have thought about.
func TestDestroyPrompt(t *testing.T) {
	const want = "This will delete the workload, all its instances, and its URLs. Continue? (y/N): "
	if destroyPrompt != want {
		t.Errorf("destroyPrompt = %q, want %q", destroyPrompt, want)
	}
}

func TestDestroySummary(t *testing.T) {
	tests := []struct {
		name        string
		objs        []client.Object
		wantLines   []string
		wantMissing []string
	}{
		{
			name: "published workload lists every URL it answers on",
			objs: []client.Object{testWorkloadObject(), publishedProxy(testCustom), publishedService()},
			wantLines: []string{
				"Workload:      " + testWorkload,
				"Placements:    1  Locations: us-east-1, eu-west-1",
				"Min replicas:  2",
				"URLs:          https://" + testCustom + ", https://" + testCanonical,
				"workload/api deleted.",
			},
		},
		{
			name:        "workload with no URL has no URLs line",
			objs:        []client.Object{testWorkloadObject()},
			wantLines:   []string{"Workload:      " + testWorkload, "workload/api deleted."},
			wantMissing: []string{"URLs:"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			c := newFakeClient(t, tc.objs...)

			if err := destroyWorkload(context.Background(), &out, &errOut, c, testProject, testWorkload, true); err != nil {
				t.Fatalf("destroyWorkload: %v", err)
			}

			got := out.String()
			for _, want := range tc.wantLines {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}
			for _, missing := range tc.wantMissing {
				if strings.Contains(got, missing) {
					t.Errorf("output should not contain %q:\n%s", missing, got)
				}
			}
			if errOut.Len() != 0 {
				t.Errorf("unexpected stderr: %s", errOut.String())
			}
		})
	}
}

// TestDestroyUnpublishes: the URL objects are deleted explicitly, not left to
// owner-reference garbage collection.
func TestDestroyUnpublishes(t *testing.T) {
	c := newFakeClient(t, testWorkloadObject(), publishedProxy(testCustom), publishedService())

	var out, errOut bytes.Buffer
	if err := destroyWorkload(context.Background(), &out, &errOut, c, testProject, testWorkload, true); err != nil {
		t.Fatalf("destroyWorkload: %v", err)
	}

	if exists(t, c, &computev1alpha.Workload{}) {
		t.Error("workload survived destroy")
	}
	if exists(t, c, &networkingv1alpha.HTTPProxy{}) {
		t.Error("URL survived destroy")
	}
	if exists(t, c, &networkingv1alpha.NetworkService{}) {
		t.Error("URL backends survived destroy")
	}
}

// TestDestroyLeftoverURLFails: a URL that could not be deleted names exactly
// what is still answering and how to finish the job — and fails the command.
// The workload really is gone, but a script that reads exit 0 here would carry
// on believing the URLs stopped answering when they may not have.
func TestDestroyLeftoverURLFails(t *testing.T) {
	boom := errors.New("forbidden")
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(testWorkloadObject(), publishedProxy(testCustom), publishedService()).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*networkingv1alpha.HTTPProxy); ok {
					return boom
				}
				return cl.Delete(ctx, obj, opts...)
			},
		}).
		Build()

	var out, errOut bytes.Buffer
	err := destroyWorkload(context.Background(), &out, &errOut, c, testProject, testWorkload, true)
	if err == nil {
		t.Fatal("URLs that outlived the workload must fail the command")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the server's failure", err)
	}

	if !strings.Contains(out.String(), "workload/api deleted.") {
		t.Errorf("stdout should still report the workload deleted:\n%s", out.String())
	}

	warning := errOut.String()
	for _, want := range []string{
		"workload was deleted, but its URLs were not",
		"https://" + testCustom,
		"https://" + testCanonical,
		"datumctl compute destroy " + testWorkload,
	} {
		if !strings.Contains(warning, want) {
			t.Errorf("message missing %q:\n%s", want, warning)
		}
	}
}

// TestDestroyLeftoverURLCleanup: the retry the warning advertises works —
// the workload is already gone, and destroy removes what it left behind.
func TestDestroyLeftoverURLCleanup(t *testing.T) {
	c := newFakeClient(t, publishedProxy(testCustom), publishedService())

	var out, errOut bytes.Buffer
	if err := destroyWorkload(context.Background(), &out, &errOut, c, testProject, testWorkload, true); err != nil {
		t.Fatalf("destroyWorkload: %v", err)
	}

	if !strings.Contains(out.String(), "already deleted") {
		t.Errorf("output should say the workload was already gone:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "URLs for api deleted.") {
		t.Errorf("output should report the URLs deleted:\n%s", out.String())
	}
	if exists(t, c, &networkingv1alpha.HTTPProxy{}) {
		t.Error("leftover URL survived")
	}
	if exists(t, c, &networkingv1alpha.NetworkService{}) {
		t.Error("leftover URL backends survived")
	}
}

// TestDestroyMissingWorkload: nothing to destroy and nothing left behind is
// the plain not-found error it always was.
func TestDestroyMissingWorkload(t *testing.T) {
	c := newFakeClient(t)

	var out, errOut bytes.Buffer
	err := destroyWorkload(context.Background(), &out, &errOut, c, testProject, testWorkload, true)
	if err == nil {
		t.Fatal("expected an error for a missing workload")
	}
	if !strings.Contains(err.Error(), `workload "api" not found in project acme-prod`) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHostnameURLs(t *testing.T) {
	tests := []struct {
		name string
		objs []client.Object
		want []string
	}{
		{
			name: "custom hostname first, managed last",
			objs: []client.Object{publishedProxy(testCustom), publishedService()},
			want: []string{"https://" + testCustom, "https://" + testCanonical},
		},
		{
			name: "managed hostname alone",
			objs: []client.Object{publishedProxy(), publishedService()},
			want: []string{"https://" + testCanonical},
		},
		{
			name: "unpublished workload has none",
			objs: nil,
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeClient(t, tc.objs...)
			info, err := url.ForWorkload(context.Background(), c, testWorkload)
			if err != nil {
				t.Fatalf("lookup: %v", err)
			}
			got := hostnameURLs(info)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("hostnameURLs = %v, want %v", got, tc.want)
			}
		})
	}
}
