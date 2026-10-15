// SPDX-License-Identifier: AGPL-3.0-only

// Package url owns the single mechanism by which a compute workload becomes a
// public HTTPS URL: a NetworkService that selects the workload's network
// interfaces by label, and an HTTPProxy whose only backend names that service.
//
// Every command that shows, creates, or removes a workload URL goes through
// this package so the two objects are always built, found, and deleted the
// same way. Nothing here prints machinery names — the vocabulary the user sees
// is "URL", "backends", "edge", and "certificate".
package url

import (
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// maxPortNameLength is the DNS-label limit NetworkServicePort.Name enforces.
const maxPortNameLength = 63

// ResourceName returns the name shared by the NetworkService and the HTTPProxy
// that publish a workload. Both objects are named after the workload so a
// human reading `datumctl get` output can tell what they belong to; lookups
// never depend on it, they select on labels.
func ResourceName(workloadName string) string {
	return workloadName
}

// PortName derives the NetworkServicePort name for a workload port.
//
// computev1alpha.NamedPort.Name has no pattern constraint, but
// NetworkServicePort.Name (and the backend reference naming it) must be a DNS
// label: lowercase alphanumerics and dashes, starting and ending with an
// alphanumeric, at most 63 characters. The name is sanitized to fit. A name
// with nothing usable in it is an error rather than an object the API server
// would reject with a message about a field the user never typed.
func PortName(p computev1alpha.NamedPort) (string, error) {
	if strings.TrimSpace(p.Name) == "" {
		return "", fmt.Errorf("port %d has no name — name the port to publish it on a URL", p.Port)
	}

	var b strings.Builder
	for _, r := range strings.ToLower(p.Name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	name := strings.Trim(b.String(), "-")
	if len(name) > maxPortNameLength {
		name = strings.Trim(name[:maxPortNameLength], "-")
	}
	if name == "" {
		return "", fmt.Errorf("port name %q cannot be used for a URL — use letters, digits and dashes", p.Name)
	}
	return name, nil
}

// objectMeta returns the metadata both published objects share: the workload's
// namespace-scoped name, the labels every lookup selects on, and an owner
// reference back to the workload.
//
// The owner reference is belt and braces. Garbage collection in a project
// virtual control plane is unverified, so Unpublish deletes both objects
// explicitly and lookups match on labels; the reference exists so that a
// control plane which does collect owned objects does the right thing.
func objectMeta(w *computev1alpha.Workload) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      ResourceName(w.Name),
		Namespace: util.ResourceNamespace,
		Labels: map[string]string{
			computev1alpha.WorkloadNameLabel: w.Name,
			computev1alpha.WorkloadUIDLabel:  string(w.UID),
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion:         computev1alpha.GroupVersion.String(),
			Kind:               "Workload",
			Name:               w.Name,
			UID:                w.UID,
			Controller:         ptr(false),
			BlockOwnerDeletion: ptr(false),
		}},
	}
}

// BuildNetworkService returns the NetworkService that gathers the workload's
// instances into one set of backends. Membership is selected by label, so
// instances appearing, disappearing and moving between cities need no edit.
//
// TrafficDistribution is deliberately left unset: the default serves each
// request from the location nearest the edge that received it, which is what
// is wanted without saying so.
func BuildNetworkService(w *computev1alpha.Workload, portName string, port int32) *networkingv1alpha.NetworkService {
	return &networkingv1alpha.NetworkService{
		ObjectMeta: objectMeta(w),
		Spec: networkingv1alpha.NetworkServiceSpec{
			NetworkInterfaces: networkingv1alpha.NetworkServiceInterfaceSelector{
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						computev1alpha.WorkloadNameLabel: w.Name,
					},
				},
			},
			Ports: []networkingv1alpha.NetworkServicePort{{
				Name:     portName,
				Port:     port,
				Protocol: networkingv1alpha.NetworkServiceProtocolTCP,
			}},
		},
	}
}

// BuildHTTPProxy returns the HTTPProxy that puts the workload on the internet.
// One rule, one backend, no matches (the CRD defaults to a PathPrefix match on
// "/"), and never any backend TLS: the edge reaches instances over plaintext
// inside the network, and the API rejects backend TLS for this backend form.
//
// hostnames are custom hostnames only. The platform-managed hostname is
// assigned by the server and read back from status.
func BuildHTTPProxy(w *computev1alpha.Workload, portName string, hostnames []string) *networkingv1alpha.HTTPProxy {
	proxy := &networkingv1alpha.HTTPProxy{
		ObjectMeta: objectMeta(w),
		Spec: networkingv1alpha.HTTPProxySpec{
			Rules: []networkingv1alpha.HTTPProxyRule{{
				Backends: []networkingv1alpha.HTTPProxyRuleBackend{{
					NetworkService: &networkingv1alpha.NetworkServiceBackendRef{
						Name: ResourceName(w.Name),
						Port: portName,
					},
				}},
			}},
		},
	}

	for _, h := range hostnames {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		proxy.Spec.Hostnames = append(proxy.Spec.Hostnames, gatewayv1.Hostname(h))
	}

	return proxy
}

func ptr[T any](v T) *T { return &v }
