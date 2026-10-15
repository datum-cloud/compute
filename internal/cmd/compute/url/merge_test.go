// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// value dereferences an optional int32, reading nil as zero.
func value(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}

func readProxy(t *testing.T, c client.Client) networkingv1alpha.HTTPProxy {
	t.Helper()
	var proxy networkingv1alpha.HTTPProxy
	key := types.NamespacedName{Namespace: util.ResourceNamespace, Name: ResourceName(testWorkloadName)}
	if err := c.Get(context.Background(), key, &proxy); err != nil {
		t.Fatalf("reading the URL back: %v", err)
	}
	return proxy
}

// TestDeclareKeepsConfigurationSetElsewhere: a redeploy owns the workload's
// backend and nothing else. The algorithm, health checks, other backends and
// their weights, a redirect rule and rule filters are set in the portal, and
// an image bump must not undo them.
func TestDeclareKeepsConfigurationSetElsewhere(t *testing.T) {
	w := workloadNamed(testWorkloadName)
	c := newFakeClient(t)
	ctx := context.Background()

	if err := Declare(ctx, c, w, "http", 8080, nil); err != nil {
		t.Fatalf("first declare: %v", err)
	}

	proxy := readProxy(t, c)
	proxy.Spec.LoadBalancer = &networkingv1alpha.HTTPProxyLoadBalancer{Type: networkingv1alpha.HTTPProxyLoadBalancerTypeRoundRobin}
	proxy.Spec.HealthCheck = &networkingv1alpha.HTTPProxyHealthCheck{
		Passive: &networkingv1alpha.HTTPProxyPassiveHealthCheck{Consecutive5xxErrors: ptr(int32(3))},
	}
	backendRule := &proxy.Spec.Rules[0]
	backendRule.Backends[0].Weight = ptr(int32(70))
	backendRule.Backends = append(backendRule.Backends, networkingv1alpha.HTTPProxyRuleBackend{
		Endpoint: "https://legacy.example.com",
		Weight:   ptr(int32(30)),
	})
	backendRule.Filters = []gatewayv1.HTTPRouteFilter{{
		Type: gatewayv1.HTTPRouteFilterResponseHeaderModifier,
		ResponseHeaderModifier: &gatewayv1.HTTPHeaderFilter{
			Set: []gatewayv1.HTTPHeader{{Name: "Strict-Transport-Security", Value: "max-age=31536000"}},
		},
	}}
	redirect := networkingv1alpha.HTTPProxyRule{
		Filters: []gatewayv1.HTTPRouteFilter{{
			Type:            gatewayv1.HTTPRouteFilterRequestRedirect,
			RequestRedirect: &gatewayv1.HTTPRequestRedirectFilter{Scheme: ptr("https"), StatusCode: ptr(301)},
		}},
	}
	proxy.Spec.Rules = append([]networkingv1alpha.HTTPProxyRule{redirect}, proxy.Spec.Rules...)
	if err := c.Update(ctx, &proxy); err != nil {
		t.Fatalf("configuring the proxy as the portal would: %v", err)
	}

	// Redeploy with the workload now answering on a differently named port.
	if err := Declare(ctx, c, w, "web", 9090, nil); err != nil {
		t.Fatalf("redeclare: %v", err)
	}

	got := readProxy(t, c).Spec
	if got.LoadBalancer == nil || got.LoadBalancer.Type != networkingv1alpha.HTTPProxyLoadBalancerTypeRoundRobin {
		t.Errorf("loadBalancer = %+v, want RoundRobin kept", got.LoadBalancer)
	}
	if got.HealthCheck == nil || got.HealthCheck.Passive == nil || value(got.HealthCheck.Passive.Consecutive5xxErrors) != 3 {
		t.Errorf("healthCheck = %+v, want the passive check kept", got.HealthCheck)
	}
	if len(got.Rules) != 2 || got.Rules[0].Filters[0].RequestRedirect == nil {
		t.Fatalf("rules = %+v, want the redirect rule kept ahead of the backend rule", got.Rules)
	}
	backends := got.Rules[1].Backends
	if len(backends) != 2 {
		t.Fatalf("backends = %d, want the workload's and the other origin", len(backends))
	}
	if backends[0].NetworkService == nil || backends[0].NetworkService.Port != "web" {
		t.Errorf("workload backend = %+v, want its port moved to \"web\"", backends[0].NetworkService)
	}
	if value(backends[0].Weight) != 70 {
		t.Errorf("workload backend weight = %v, want 70 kept", value(backends[0].Weight))
	}
	if backends[1].Endpoint != "https://legacy.example.com" || value(backends[1].Weight) != 30 {
		t.Errorf("other backend = %+v, want it kept with weight 30", backends[1])
	}
	if len(got.Rules[1].Filters) != 1 {
		t.Errorf("backend rule filters = %d, want the HSTS filter kept", len(got.Rules[1].Filters))
	}
}

// TestDeclareRestoresTheWorkloadBackend: a deploy guarantees its URL routes to
// the workload. If the workload's backend was removed from the pool, it goes
// back into the rule that holds the others rather than a second rule.
func TestDeclareRestoresTheWorkloadBackend(t *testing.T) {
	w := workloadNamed(testWorkloadName)
	c := newFakeClient(t)
	ctx := context.Background()

	if err := Declare(ctx, c, w, "http", 8080, nil); err != nil {
		t.Fatalf("first declare: %v", err)
	}
	proxy := readProxy(t, c)
	proxy.Spec.Rules[0].Backends = []networkingv1alpha.HTTPProxyRuleBackend{{Endpoint: "https://other.example.com"}}
	if err := c.Update(ctx, &proxy); err != nil {
		t.Fatalf("removing the workload backend: %v", err)
	}

	if err := Declare(ctx, c, w, "http", 8080, nil); err != nil {
		t.Fatalf("redeclare: %v", err)
	}

	got := readProxy(t, c).Spec
	if len(got.Rules) != 1 || len(got.Rules[0].Backends) != 2 {
		t.Fatalf("rules = %+v, want one rule with the other origin and the workload", got.Rules)
	}
	if got.Rules[0].Backends[1].NetworkService == nil {
		t.Errorf("backends = %+v, want the workload's backend restored", got.Rules[0].Backends)
	}
}
