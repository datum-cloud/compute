// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// proxiesFor returns the HTTPProxies routing to a workload: those labelled
// with it and those backed by a NetworkService selecting it. Unlike the url
// package, it counts hand-written proxies, as the portal's Logs tab does.
func proxiesFor(ctx context.Context, c client.Client, workload string) ([]string, error) {
	var services networkingv1alpha.NetworkServiceList
	if err := c.List(ctx, &services, client.InNamespace(util.ResourceNamespace)); err != nil {
		return nil, fmt.Errorf("listing network services: %w", err)
	}
	var proxies networkingv1alpha.HTTPProxyList
	if err := c.List(ctx, &proxies, client.InNamespace(util.ResourceNamespace)); err != nil {
		return nil, fmt.Errorf("listing HTTP proxies: %w", err)
	}

	var backing []string
	for _, s := range services.Items {
		if s.Labels[computev1alpha.WorkloadNameLabel] == workload ||
			s.Spec.NetworkInterfaces.Selector.MatchLabels[computev1alpha.WorkloadNameLabel] == workload {
			backing = append(backing, s.Name)
		}
	}

	var names []string
	for _, p := range proxies.Items {
		if p.Labels[computev1alpha.WorkloadNameLabel] == workload || routesTo(p, backing) {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func routesTo(p networkingv1alpha.HTTPProxy, services []string) bool {
	for _, rule := range p.Spec.Rules {
		for _, b := range rule.Backends {
			if b.NetworkService != nil && slices.Contains(services, b.NetworkService.Name) {
				return true
			}
		}
	}
	return false
}
