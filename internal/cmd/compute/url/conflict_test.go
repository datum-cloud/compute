// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"context"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// conflictOnce fails the first Update of one kind with the error the API
// server returns when the controller wrote status between our read and write.
func conflictOnce(kind string, calls *int) interceptor.Funcs {
	return interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if kindOf(obj) == kind {
				*calls++
				if *calls == 1 {
					return k8serrors.NewConflict(schema.GroupResource{Group: "networking.datumapis.com", Resource: kind},
						obj.GetName(), nil)
				}
			}
			return c.Update(ctx, obj, opts...)
		},
	}
}

func TestDeclareRetriesAServiceUpdateThatConflicts(t *testing.T) {
	var calls int
	c := interceptor.NewClient(newFakeClient(t,
		BuildNetworkService(workloadNamed(testWorkloadName), testPortName, 8080),
		BuildHTTPProxy(workloadNamed(testWorkloadName), testPortName, nil),
	), conflictOnce(kindService, &calls))

	if err := Declare(context.Background(), c, workloadNamed(testWorkloadName), testPortName, 9090, nil); err != nil {
		t.Fatalf("Declare returned error: %v", err)
	}
	if calls != 2 {
		t.Errorf("service updates = %d, want a retry after the conflict", calls)
	}

	var svc networkingv1alpha.NetworkService
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: util.ResourceNamespace, Name: testWorkloadName}, &svc); err != nil {
		t.Fatalf("getting service: %v", err)
	}
	if svc.Spec.Ports[0].Port != 9090 {
		t.Errorf("port = %d, want the new port 9090", svc.Spec.Ports[0].Port)
	}
}

func TestDeclareRetriesAProxyUpdateThatConflicts(t *testing.T) {
	var calls int
	c := interceptor.NewClient(newFakeClient(t,
		BuildNetworkService(workloadNamed(testWorkloadName), testPortName, 8080),
		BuildHTTPProxy(workloadNamed(testWorkloadName), testPortName, nil),
	), conflictOnce(kindProxy, &calls))

	if err := Declare(context.Background(), c, workloadNamed(testWorkloadName), testPortName, 8080,
		[]string{"api.example.com"}); err != nil {
		t.Fatalf("Declare returned error: %v", err)
	}
	if calls != 2 {
		t.Errorf("proxy updates = %d, want a retry after the conflict", calls)
	}
}

func TestDeclareLeavesAnUnchangedServiceAlone(t *testing.T) {
	// The API defaults traffic distribution, which a deploy never sets. That
	// default must not read as a change, or every deploy rewrites the service
	// and risks colliding with the controller.
	svc := BuildNetworkService(workloadNamed(testWorkloadName), testPortName, 8080)
	svc.Spec.TrafficDistribution.Strategy = networkingv1alpha.NetworkServiceTrafficDistributionStrategyNearest

	rec := &recorder{}
	c := interceptor.NewClient(newFakeClient(t,
		svc,
		BuildHTTPProxy(workloadNamed(testWorkloadName), testPortName, nil),
	), rec.funcs())

	if err := Declare(context.Background(), c, workloadNamed(testWorkloadName), testPortName, 8080, nil); err != nil {
		t.Fatalf("Declare returned error: %v", err)
	}
	if len(rec.updates) != 0 {
		t.Errorf("updates = %v, want none for an unchanged deploy", rec.updates)
	}
}

func TestDeclareKeepsTheTrafficDistributionWhenThePortChanges(t *testing.T) {
	svc := BuildNetworkService(workloadNamed(testWorkloadName), testPortName, 8080)
	svc.Spec.TrafficDistribution.Strategy = networkingv1alpha.NetworkServiceTrafficDistributionStrategyNearest
	c := newFakeClient(t, svc, BuildHTTPProxy(workloadNamed(testWorkloadName), testPortName, nil))

	if err := Declare(context.Background(), c, workloadNamed(testWorkloadName), testPortName, 9090, nil); err != nil {
		t.Fatalf("Declare returned error: %v", err)
	}

	var got networkingv1alpha.NetworkService
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: util.ResourceNamespace, Name: testWorkloadName}, &got); err != nil {
		t.Fatalf("getting service: %v", err)
	}
	if got.Spec.Ports[0].Port != 9090 {
		t.Errorf("port = %d, want the new port 9090", got.Spec.Ports[0].Port)
	}
	if got.Spec.TrafficDistribution.Strategy != networkingv1alpha.NetworkServiceTrafficDistributionStrategyNearest {
		t.Errorf("strategy = %q, want the existing Nearest kept", got.Spec.TrafficDistribution.Strategy)
	}
}
