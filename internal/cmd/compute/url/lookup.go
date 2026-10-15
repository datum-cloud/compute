// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"context"
	"fmt"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// Status strings shown for a hostname. A hostname is active or it is not; when
// it is not, the server's own message is shown rather than a CLI translation
// of it — and never the server's condition reason, which is camelCase internal
// state the developer is not meant to read.
const (
	statusActive  = "active"
	statusPending = "pending"

	certificateValid = "valid"
)

// Backends totals the instances serving a URL, across every city.
type Backends struct {
	// Cities is how many cities the URL has backends in.
	Cities int32 `json:"cities"`
	// Total is how many backends are registered, healthy or not.
	Total int32 `json:"total"`
	// Healthy is how many of them are taking traffic.
	Healthy int32 `json:"healthy"`
}

// Location reports the backends a URL has in one city, and whether that city
// is taking traffic.
type Location struct {
	// Location is the location as the platform reports it.
	Location string `json:"location"`
	// Backends is how many instances in this location back the URL.
	Backends int32 `json:"backends"`
	// Healthy is how many of them are taking traffic.
	Healthy int32 `json:"healthy"`
	// Serving reports whether this location is in rotation.
	Serving bool `json:"serving"`
}

// Hostname is the state of one hostname on a URL.
type Hostname struct {
	// Hostname is the fully qualified name.
	Hostname string `json:"hostname"`
	// URL is the hostname as an https:// URL. Datum never serves plaintext.
	URL string `json:"url"`
	// Managed is true for the platform-assigned hostname, which the user
	// neither creates nor removes.
	Managed bool `json:"managed"`
	// Active is true when this hostname is serving traffic.
	Active bool `json:"active"`
	// Status is "active", or the server's blocking message verbatim, or
	// "pending" when the server has not said anything a human can read.
	Status string `json:"status"`
	// Certificate is "valid", the server's certificate message verbatim,
	// "pending" when it is not ready and the server said nothing readable, or
	// empty when the platform has not reported on a certificate at all.
	Certificate string `json:"certificate,omitempty"`
	// Detail is the server's message for the first blocking condition, shown
	// verbatim. Empty when nothing is blocking.
	Detail string `json:"detail,omitempty"`
	// Conditions are the raw per-hostname conditions, for -o yaml and for
	// callers that want to render more than Status.
	Conditions []metav1.Condition `json:"-"`
}

// Info is everything the CLI knows about one workload's URL. A workload that
// has not been published has no Info at all — callers get nil, not a zero
// value.
type Info struct {
	// WorkloadName is the workload this URL belongs to.
	WorkloadName string `json:"workloadName"`
	// URL is the one URL to show the user: the first active custom hostname if
	// there is one, otherwise the platform-managed hostname. Always https://.
	URL string `json:"url"`
	// CanonicalHostname is the platform-managed hostname. Empty until the
	// server assigns one.
	CanonicalHostname string `json:"canonicalHostname,omitempty"`
	// CustomHostnames are the hostnames the user attached, in declared order.
	CustomHostnames []string `json:"customHostnames,omitempty"`
	// Hostnames carries per-hostname state for every hostname on the URL,
	// custom ones first, the managed one last.
	Hostnames []Hostname `json:"hostnames,omitempty"`

	// PortName and Port are the port the backends answer on. Protocol is the
	// transport, always TCP today.
	PortName string `json:"portName,omitempty"`
	Port     int32  `json:"port,omitempty"`
	Protocol string `json:"protocol,omitempty"`

	// Backends totals the serving instances; Locations breaks that down by
	// city, which is what makes a multi-city deployment legible.
	Backends  Backends   `json:"backends"`
	Locations []Location `json:"locations,omitempty"`

	// EdgeProgrammed is true once the edge is carrying the configuration.
	EdgeProgrammed bool `json:"edgeProgrammed"`
	// CertificateIssued is true once the primary hostname has a certificate.
	CertificateIssued bool `json:"certificateIssued"`
	// CertificateKnown is false when the platform has reported nothing about a
	// certificate, which is different from reporting that there isn't one.
	CertificateKnown bool `json:"-"`

	// ProxyConditions and ServiceConditions are the raw conditions behind the
	// fields above. Render whatever the server emits; never branch on a reason.
	ProxyConditions   []metav1.Condition `json:"-"`
	ServiceConditions []metav1.Condition `json:"-"`

	// Proxy and Service are the objects themselves, for `-o yaml`. They are
	// the machinery: never name them in normal output. Reach them through
	// Objects(), which is the shape structured output renders.
	Proxy   *networkingv1alpha.HTTPProxy      `json:"-"`
	Service *networkingv1alpha.NetworkService `json:"-"`
}

// Objects is the raw platform state behind a URL, in the shape structured
// output renders it. It is the escape hatch the product promises: plain
// language in normal output, the real objects behind `-o yaml`.
//
// Both fields may be nil — a proxy exists for a moment before its backends do,
// and a control plane that does not serve the backend kind reports none.
type Objects struct {
	HTTPProxy      *networkingv1alpha.HTTPProxy      `json:"httpProxy,omitempty"`
	NetworkService *networkingv1alpha.NetworkService `json:"networkService,omitempty"`
}

// Objects returns the real objects behind the URL, for a caller rendering
// `-o yaml` or `-o json`. It returns nil when there is nothing to show, so a
// caller can fall back to the human view without a second check.
//
// This is the only sanctioned way out of this package to the machinery: the
// default human output never names it.
func (i *Info) Objects() *Objects {
	if i == nil || (i.Proxy == nil && i.Service == nil) {
		return nil
	}
	return &Objects{HTTPProxy: i.Proxy, NetworkService: i.Service}
}

// Live reports whether the URL is ready to be handed to the user: it has a
// hostname, the edge is programmed, and a certificate has been issued (or the
// platform reports no certificate state at all, in which case there is nothing
// to wait for).
func (i *Info) Live() bool {
	if i == nil {
		return false
	}
	return i.URL != "" && i.EdgeProgrammed && (i.CertificateIssued || !i.CertificateKnown)
}

// ForWorkload returns the URL info for one workload, or nil when the workload
// has not been published. A workload without a URL is an ordinary state, not
// an error; so is a control plane that does not serve these kinds at all.
// Transport and permission errors propagate.
func ForWorkload(ctx context.Context, c client.Client, workloadName string) (*Info, error) {
	proxies, services, err := list(ctx, c, labels.Set{computev1alpha.WorkloadNameLabel: workloadName})
	if err != nil {
		return nil, err
	}
	if len(proxies) == 0 {
		return nil, nil
	}
	return newInfo(workloadName, &proxies[0], serviceFor(services, workloadName)), nil
}

// ForAll returns URL info for every published workload in the project, keyed
// by workload name. Workloads without a URL are absent from the map.
//
// It costs exactly two List calls no matter how many workloads there are: the
// list view renders a whole project through this.
func ForAll(ctx context.Context, c client.Client) (map[string]*Info, error) {
	proxies, services, err := list(ctx, c, nil)
	if err != nil {
		return nil, err
	}

	byWorkload := make(map[string]*networkingv1alpha.NetworkService, len(services))
	for i := range services {
		byWorkload[workloadOf(services[i].Labels, services[i].Name)] = &services[i]
	}

	infos := make(map[string]*Info, len(proxies))
	for i := range proxies {
		name := workloadOf(proxies[i].Labels, proxies[i].Name)
		infos[name] = newInfo(name, &proxies[i], byWorkload[name])
	}
	return infos, nil
}

// list fetches both published kinds with one call each. An empty match lists
// every URL the CLI published in the namespace — and only those: an HTTPProxy
// a user wrote by hand is theirs, and is never reported as a workload's URL.
func list(ctx context.Context, c client.Client, match labels.Set) ([]networkingv1alpha.HTTPProxy, []networkingv1alpha.NetworkService, error) {
	selector := labels.SelectorFromSet(match)
	if len(match) == 0 {
		req, err := labels.NewRequirement(computev1alpha.WorkloadNameLabel, selection.Exists, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("building URL selector: %w", err)
		}
		selector = labels.NewSelector().Add(*req)
	}

	opts := []client.ListOption{
		client.InNamespace(util.ResourceNamespace),
		client.MatchingLabelsSelector{Selector: selector},
	}

	var proxyList networkingv1alpha.HTTPProxyList
	if err := c.List(ctx, &proxyList, opts...); err != nil {
		if notPublished(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("listing URLs: %w", err)
	}

	var serviceList networkingv1alpha.NetworkServiceList
	if err := c.List(ctx, &serviceList, opts...); err != nil {
		if notPublished(err) {
			return proxyList.Items, nil, nil
		}
		return nil, nil, fmt.Errorf("listing URL backends: %w", err)
	}

	return proxyList.Items, serviceList.Items, nil
}

// notPublished reports whether an error means "there is nothing published
// here" rather than a real failure. A control plane that has never had these
// CRDs installed answers with a no-match error, and a client whose scheme
// lacks the kinds answers with a not-registered error; neither is something to
// report to a user who only asked for a URL.
func notPublished(err error) bool {
	return err == nil ||
		k8serrors.IsNotFound(err) ||
		meta.IsNoMatchError(err) ||
		runtime.IsNotRegisteredError(err)
}

// serviceFor picks the NetworkService belonging to a workload out of a list.
func serviceFor(services []networkingv1alpha.NetworkService, workloadName string) *networkingv1alpha.NetworkService {
	for i := range services {
		if workloadOf(services[i].Labels, services[i].Name) == workloadName {
			return &services[i]
		}
	}
	return nil
}

// workloadOf reads the workload a published object belongs to from its labels,
// falling back to the object's own name, which ResourceName keeps in step.
func workloadOf(objLabels map[string]string, objName string) string {
	if name := objLabels[computev1alpha.WorkloadNameLabel]; name != "" {
		return name
	}
	return objName
}

// newInfo assembles the user-facing view from the two objects. service may be
// nil: a proxy can exist for a moment before its backends do.
func newInfo(workloadName string, proxy *networkingv1alpha.HTTPProxy, service *networkingv1alpha.NetworkService) *Info {
	info := &Info{
		WorkloadName:      workloadName,
		CanonicalHostname: proxy.Status.CanonicalHostname,
		ProxyConditions:   proxy.Status.Conditions,
		Proxy:             proxy,
		Service:           service,
	}

	if c := util.FindCondition(proxy.Status.Conditions, networkingv1alpha.HTTPProxyConditionProgrammed); c != nil {
		info.EdgeProgrammed = c.Status == metav1.ConditionTrue
	}

	for _, h := range proxy.Spec.Hostnames {
		info.CustomHostnames = append(info.CustomHostnames, string(h))
	}

	for _, h := range info.CustomHostnames {
		info.Hostnames = append(info.Hostnames, hostnameInfo(proxy, h, false))
	}
	if info.CanonicalHostname != "" {
		info.Hostnames = append(info.Hostnames, hostnameInfo(proxy, info.CanonicalHostname, true))
	}

	info.URL, info.CertificateIssued, info.CertificateKnown = primary(info)

	if service != nil {
		info.ServiceConditions = service.Status.Conditions
		if len(service.Spec.Ports) > 0 {
			p := service.Spec.Ports[0]
			info.PortName, info.Port = p.Name, p.Port
			info.Protocol = string(p.Protocol)
			if info.Protocol == "" {
				info.Protocol = string(networkingv1alpha.NetworkServiceProtocolTCP)
			}
		}
		info.Backends = Backends{
			Cities:  service.Status.Summary.Locations,
			Total:   service.Status.Summary.Members,
			Healthy: service.Status.Summary.Healthy,
		}
		for _, l := range service.Status.Locations {
			info.Locations = append(info.Locations, Location{
				Location: l.Name,
				Backends: l.Members,
				Healthy:  l.Healthy,
				Serving:  l.Serving,
			})
		}
	}

	return info
}

// primary chooses the URL to show and reports the certificate state behind it.
// An active custom hostname is what the user wants to see; until one is
// active, the platform-managed hostname is the one that actually answers.
func primary(info *Info) (url string, certIssued, certKnown bool) {
	var fallback *Hostname
	var managed *Hostname

	for i := range info.Hostnames {
		h := &info.Hostnames[i]
		switch {
		case h.Managed:
			managed = h
		case h.Active:
			return h.URL, h.Certificate == certificateValid, h.Certificate != ""
		case fallback == nil:
			fallback = h
		}
	}

	if managed != nil {
		return managed.URL, managed.Certificate == certificateValid, managed.Certificate != ""
	}
	if fallback != nil {
		return fallback.URL, fallback.Certificate == certificateValid, fallback.Certificate != ""
	}
	return "", false, false
}

// hostnameInfo derives one hostname's state from that hostname's own entry in
// the proxy's per-hostname statuses. Nothing else can say whether a hostname is
// serving: the proxy-level conditions are a roll-up over every hostname on the
// proxy, so one hostname waiting on a certificate turns the proxy's certificate
// condition False while every other hostname carries on serving normally.
//
// Two rules follow from that, and both matter to a user:
//
//   - A custom hostname the platform has published no status for is pending,
//     never active. It has just been attached and nothing about it has been
//     checked yet, so `domains add` must show the DNS records and wait.
//   - The platform-managed hostname may fall back to the proxy-level
//     conditions, because it is the hostname the proxy is for. On a control
//     plane that publishes per-hostname statuses for other hostnames, only the
//     conditions that are True may inform it: a True roll-up covers every
//     hostname, while a False one names none of them.
func hostnameInfo(proxy *networkingv1alpha.HTTPProxy, hostname string, managed bool) Hostname {
	h := Hostname{
		Hostname: hostname,
		URL:      "https://" + hostname,
		Managed:  managed,
		Status:   statusPending,
	}

	conditions := perHostnameConditions(proxy, hostname)
	h.Conditions = conditions

	if len(conditions) == 0 {
		if !managed {
			return h
		}
		if len(proxy.Status.HostnameStatuses) == 0 {
			conditions = proxy.Status.Conditions
		} else {
			conditions = satisfied(proxy.Status.Conditions)
		}
	}

	// Certificate state, in the server's own words.
	if c := certificateCondition(conditions); c != nil {
		if c.Status == metav1.ConditionTrue {
			h.Certificate = certificateValid
		} else {
			h.Certificate = humanReason(c)
		}
	}

	// A hostname is active when nothing about it is blocking and the edge is
	// carrying the configuration.
	blocked := firstBlocking(conditions)
	programmed := util.FindCondition(proxy.Status.Conditions, networkingv1alpha.HTTPProxyConditionProgrammed)
	switch {
	case blocked != nil:
		h.Status = humanReason(blocked)
		h.Detail = blocked.Message
	case programmed != nil && programmed.Status == metav1.ConditionTrue:
		h.Status = statusActive
		h.Active = true
	}

	return h
}

// humanReason renders why a condition is not satisfied, for a user to read.
//
// It is the server's message, which is written for a human, and never the
// condition's reason, which is camelCase internal state. A condition with no
// message says only that the platform has not finished — which is "pending",
// the same plain word an unreported hostname gets.
func humanReason(c *metav1.Condition) string {
	if c.Message != "" {
		return c.Message
	}
	return statusPending
}

// satisfied returns the conditions that are True. Used to let a proxy-level
// roll-up vouch for the managed hostname without letting it blame it.
func satisfied(conditions []metav1.Condition) []metav1.Condition {
	var ok []metav1.Condition
	for _, c := range conditions {
		if c.Status == metav1.ConditionTrue {
			ok = append(ok, c)
		}
	}
	return ok
}

// perHostnameConditions returns the conditions the server published for one
// hostname, or nil when it published none.
func perHostnameConditions(proxy *networkingv1alpha.HTTPProxy, hostname string) []metav1.Condition {
	for _, s := range proxy.Status.HostnameStatuses {
		if s.Hostname == hostname {
			return s.Conditions
		}
	}
	return nil
}

// certificateCondition finds whichever certificate condition the given set
// carries: per-hostname status uses one type, proxy-level status another.
func certificateCondition(conditions []metav1.Condition) *metav1.Condition {
	if c := util.FindCondition(conditions, networkingv1alpha.HostnameConditionCertificateReady); c != nil {
		return c
	}
	return util.FindCondition(conditions, networkingv1alpha.HTTPProxyConditionCertificatesReady)
}

// firstBlocking returns the first condition that is not True, so its reason and
// message can be shown verbatim. Unknown counts as blocking: the platform has
// not yet said the hostname works.
func firstBlocking(conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Status != metav1.ConditionTrue {
			return &conditions[i]
		}
	}
	return nil
}
