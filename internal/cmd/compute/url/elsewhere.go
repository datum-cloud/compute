// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"context"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// ServedElsewhere names the load balancers datumctl did not create that
// already route to a workload: HTTPProxies with a backend whose NetworkService
// selects the workload's instances by name. A shared load balancer set up in
// the portal or by hand is the everyday case.
//
// A deploy uses this to say what is already serving a workload before it puts
// a second URL in front of it. It is advice, not a gate, so the caller decides
// what a failed read means.
//
// A NetworkService that no proxy routes to is not reported: it serves nothing
// a user would recognise as a URL.
func ServedElsewhere(ctx context.Context, c client.Client, workloadName string) ([]string, error) {
	opts := []client.ListOption{client.InNamespace(util.ResourceNamespace)}

	var services networkingv1alpha.NetworkServiceList
	if err := c.List(ctx, &services, opts...); err != nil {
		if notPublished(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing load balancer backends: %w", err)
	}

	selecting := map[string]bool{}
	for i := range services.Items {
		s := &services.Items[i]
		if isWorkloadsOwn(s, workloadName) {
			continue
		}
		if selectsWorkload(s.Spec.NetworkInterfaces.Selector, workloadName) {
			selecting[s.Name] = true
		}
	}
	if len(selecting) == 0 {
		return nil, nil
	}

	var proxies networkingv1alpha.HTTPProxyList
	if err := c.List(ctx, &proxies, opts...); err != nil {
		if notPublished(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing load balancers: %w", err)
	}

	var names []string
	for i := range proxies.Items {
		p := &proxies.Items[i]
		if isWorkloadsOwn(p, workloadName) {
			continue
		}
		if routesTo(p, selecting) {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// selectsWorkload reports whether a NetworkService selector picks out the
// workload by name. Compute stamps the workload name on every network
// interface it creates, so that label is how a selector targets a workload;
// the other labels it stamps (placement, location, instance index) narrow a
// selection within a workload rather than choosing one.
//
// Only a requirement that names the workload counts: workload-name equal to
// it, or in a set that includes it. One that matches it without naming it —
// workload-name exists, or is not some other workload — selects every
// workload in the project alike, and would report a catch-all load balancer
// as serving each of them. An empty selector is not counted for the same
// reason.
func selectsWorkload(selector metav1.LabelSelector, workloadName string) bool {
	sel, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil {
		return false
	}
	reqs, _ := sel.Requirements()
	for _, r := range reqs {
		if r.Key() != computev1alpha.WorkloadNameLabel {
			continue
		}
		switch r.Operator() {
		case selection.Equals, selection.DoubleEquals, selection.In:
			if r.Values().Has(workloadName) {
				return true
			}
		}
	}
	return false
}

// routesTo reports whether any of the proxy's backends is one of the named
// NetworkServices.
func routesTo(p *networkingv1alpha.HTTPProxy, services map[string]bool) bool {
	for _, rule := range p.Spec.Rules {
		for _, b := range rule.Backends {
			if b.NetworkService != nil && services[b.NetworkService.Name] {
				return true
			}
		}
	}
	return false
}
