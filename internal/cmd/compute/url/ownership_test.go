// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	// handMadePort is the port name a load balancer set up outside datumctl
	// uses; datumctl's own is testPortName.
	handMadePort = "web"

	// sharedProxy names a load balancer set up outside datumctl.
	sharedProxy = "shared"
)

// handMadeService is a NetworkService set up outside datumctl — in the portal,
// or by hand — selecting the named workload's instances.
func handMadeService(name, workload string) *networkingv1alpha.NetworkService {
	return &networkingv1alpha.NetworkService{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: util.ResourceNamespace},
		Spec: networkingv1alpha.NetworkServiceSpec{
			NetworkInterfaces: networkingv1alpha.NetworkServiceInterfaceSelector{
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{computev1alpha.WorkloadNameLabel: workload},
				},
			},
			Ports: []networkingv1alpha.NetworkServicePort{{Name: handMadePort, Port: 8080, Protocol: networkingv1alpha.NetworkServiceProtocolTCP}},
		},
	}
}

// handMadeProxy is an HTTPProxy set up outside datumctl, routing to the named
// NetworkServices.
func handMadeProxy(name string, services ...string) *networkingv1alpha.HTTPProxy {
	rule := networkingv1alpha.HTTPProxyRule{}
	for _, s := range services {
		rule.Backends = append(rule.Backends, networkingv1alpha.HTTPProxyRuleBackend{
			NetworkService: &networkingv1alpha.NetworkServiceBackendRef{Name: s, Port: handMadePort},
		})
	}
	return &networkingv1alpha.HTTPProxy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: util.ResourceNamespace},
		Spec:       networkingv1alpha.HTTPProxySpec{Rules: []networkingv1alpha.HTTPProxyRule{rule}},
	}
}

// TestDeclareLeavesSomeoneElsesObjectsAlone: an object that only shares the
// workload's name was not created by datumctl. Publishing over it would
// rewrite a load balancer someone set up themselves, routing and all, so
// publishing stops and says so instead.
func TestDeclareLeavesSomeoneElsesObjectsAlone(t *testing.T) {
	tests := []struct {
		name  string
		objs  func() []client.Object
		check client.Object
	}{
		{
			name:  "backends",
			objs:  func() []client.Object { return []client.Object{handMadeService(testWorkloadName, testWorkloadName)} },
			check: &networkingv1alpha.NetworkService{},
		},
		{
			name: "load balancer",
			objs: func() []client.Object {
				// The backends are datumctl's, so the refusal comes from the proxy.
				return []client.Object{
					BuildNetworkService(workloadNamed(testWorkloadName), testPortName, 8080, false),
					handMadeProxy(testWorkloadName, sharedProxy),
				}
			},
			check: &networkingv1alpha.HTTPProxy{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := tc.objs()
			c := newFakeClient(t, objs...)

			err := Declare(context.Background(), c, workloadNamed(testWorkloadName), testPortName, 8080, false, nil)
			if !errors.Is(err, ErrNotCreatedByDatumctl) {
				t.Fatalf("Declare error = %v, want ErrNotCreatedByDatumctl", err)
			}

			key := client.ObjectKey{Namespace: util.ResourceNamespace, Name: testWorkloadName}
			if err := c.Get(context.Background(), key, tc.check); err != nil {
				t.Fatalf("reading the object back: %v", err)
			}
			if _, ok := tc.check.GetLabels()[computev1alpha.WorkloadNameLabel]; ok {
				t.Error("the object was labelled as datumctl's, so later lookups would treat it as the workload's own URL")
			}
			if len(tc.check.GetOwnerReferences()) != 0 {
				t.Error("the object was given an owner reference to the workload")
			}
		})
	}
}

// A workload deployed again under the same name gets a new UID. The URL it
// published before is still its own, and must be brought up to date rather
// than refused.
func TestDeclareKeepsAURLAcrossARecreatedWorkload(t *testing.T) {
	before := workloadNamed(testWorkloadName)
	c := newFakeClient(t,
		BuildNetworkService(before, testPortName, 8080, false),
		BuildHTTPProxy(before, testPortName, nil),
	)

	after := workloadNamed(testWorkloadName)
	after.UID = "uid-recreated"
	if err := Declare(context.Background(), c, after, testPortName, 8080, false, nil); err != nil {
		t.Fatalf("Declare: %v", err)
	}

	var svc networkingv1alpha.NetworkService
	key := client.ObjectKey{Namespace: util.ResourceNamespace, Name: testWorkloadName}
	if err := c.Get(context.Background(), key, &svc); err != nil {
		t.Fatalf("reading the backends back: %v", err)
	}
	if got := svc.Labels[computev1alpha.WorkloadUIDLabel]; got != "uid-recreated" {
		t.Errorf("workload UID label = %q, want the recreated workload's", got)
	}
}

// TestUnpublishLeavesSomeoneElsesObjectsAlone: --no-http and destroy remove
// the URL datumctl published. A load balancer that merely shares the
// workload's name may be the very one the workload is served through.
func TestUnpublishLeavesSomeoneElsesObjectsAlone(t *testing.T) {
	c := newFakeClient(t,
		handMadeService(testWorkloadName, testWorkloadName),
		handMadeProxy(testWorkloadName, testWorkloadName),
	)

	kept, err := Unpublish(context.Background(), c, testWorkloadName)
	if err != nil {
		t.Fatalf("Unpublish: %v", err)
	}
	want := []Foreign{{Kind: kindLoadBalancer, Name: testWorkloadName}, {Kind: kindBackends, Name: testWorkloadName}}
	if !reflect.DeepEqual(kept, want) {
		t.Errorf("kept = %v, want %v — the caller has to be able to say they are still there", kept, want)
	}

	key := client.ObjectKey{Namespace: util.ResourceNamespace, Name: testWorkloadName}
	if err := c.Get(context.Background(), key, &networkingv1alpha.HTTPProxy{}); err != nil {
		t.Errorf("a load balancer datumctl did not create was deleted: %v", err)
	}
	if err := c.Get(context.Background(), key, &networkingv1alpha.NetworkService{}); err != nil {
		t.Errorf("load balancer backends datumctl did not create were deleted: %v", err)
	}
}

func TestServedElsewhere(t *testing.T) {
	tests := []struct {
		name string
		objs []client.Object
		want []string
	}{
		{
			name: "nothing else in the project",
			objs: nil,
		},
		{
			name: "a shared load balancer routing to the workload",
			objs: []client.Object{
				handMadeService("api-backends", testWorkloadName),
				handMadeProxy(sharedProxy, "api-backends"),
			},
			want: []string{sharedProxy},
		},
		{
			name: "every load balancer routing to it, in order",
			objs: []client.Object{
				handMadeService("api-backends", testWorkloadName),
				handMadeProxy("zeta", "api-backends"),
				handMadeProxy("alpha", "other", "api-backends"),
			},
			want: []string{"alpha", "zeta"},
		},
		{
			name: "a selector naming the workload among others",
			objs: func() []client.Object {
				s := handMadeService("many", "")
				s.Spec.NetworkInterfaces.Selector = metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{{
						Key:      computev1alpha.WorkloadNameLabel,
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{"frontend", testWorkloadName},
					}},
				}
				return []client.Object{s, handMadeProxy(sharedProxy, "many")}
			}(),
			want: []string{sharedProxy},
		},
		{
			name: "a catch-all on the workload-name label serves no workload in particular",
			objs: func() []client.Object {
				s := handMadeService("all-workloads", "")
				s.Spec.NetworkInterfaces.Selector = metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{{
						Key:      computev1alpha.WorkloadNameLabel,
						Operator: metav1.LabelSelectorOpExists,
					}},
				}
				return []client.Object{s, handMadeProxy(sharedProxy, "all-workloads")}
			}(),
		},
		{
			name: "excluding another workload does not name this one",
			objs: func() []client.Object {
				s := handMadeService("not-frontend", "")
				s.Spec.NetworkInterfaces.Selector = metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{{
						Key:      computev1alpha.WorkloadNameLabel,
						Operator: metav1.LabelSelectorOpNotIn,
						Values:   []string{"frontend"},
					}},
				}
				return []client.Object{s, handMadeProxy(sharedProxy, "not-frontend")}
			}(),
		},
		{
			name: "backends that no load balancer routes to serve nothing",
			objs: []client.Object{handMadeService("api-backends", testWorkloadName)},
		},
		{
			name: "a load balancer for another workload",
			objs: []client.Object{
				handMadeService("frontend-backends", "frontend"),
				handMadeProxy(sharedProxy, "frontend-backends"),
			},
		},
		{
			name: "an empty selector says nothing about this workload",
			objs: func() []client.Object {
				s := handMadeService("everything", "")
				s.Spec.NetworkInterfaces.Selector = metav1.LabelSelector{}
				return []client.Object{s, handMadeProxy(sharedProxy, "everything")}
			}(),
		},
		{
			name: "the workload's own URL is not somewhere else",
			objs: []client.Object{
				publishedService(testWorkloadName, 8080),
				publishedProxy(testWorkloadName, testCanonical),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ServedElsewhere(context.Background(), newFakeClient(t, tc.objs...), testWorkloadName)
			if err != nil {
				t.Fatalf("ServedElsewhere: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ServedElsewhere = %v, want %v", got, tc.want)
			}
		})
	}
}

// A URL datumctl published, whose backends were replaced by hand with an
// object of the same name: the proxy is datumctl's, the backends are not, and
// publishing must stop at the backends without touching either.
func TestCheckOwnershipLooksAtBothObjects(t *testing.T) {
	c := newFakeClient(t,
		publishedProxy(testWorkloadName, testCanonical),
		handMadeService(testWorkloadName, testWorkloadName),
	)

	err := CheckOwnership(context.Background(), c, testWorkloadName)
	if !errors.Is(err, ErrNotCreatedByDatumctl) {
		t.Fatalf("CheckOwnership error = %v, want ErrNotCreatedByDatumctl", err)
	}
	if want := `load balancer backends "api" already exists`; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to name %q", err, want)
	}
}

// TestDeclareWritesNothingWhenEitherObjectIsRefused: the backends are written
// before the proxy, so a proxy refused only once the backends are in would
// leave them behind, labelled as the workload's and routing nowhere.
func TestDeclareWritesNothingWhenEitherObjectIsRefused(t *testing.T) {
	c := interceptor.NewClient(newFakeClient(t, handMadeProxy(testWorkloadName, sharedProxy)), interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			t.Errorf("created %s %q before the refusal", kindOf(obj), obj.GetName())
			return nil
		},
	})

	err := Declare(context.Background(), c, workloadNamed(testWorkloadName), testPortName, 8080, false, nil)
	if !errors.Is(err, ErrNotCreatedByDatumctl) {
		t.Fatalf("Declare error = %v, want ErrNotCreatedByDatumctl", err)
	}
}
