// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Stream labels on compute logs. The collector's OTel attributes
// (datum.workload.name) reach the query API with dots as underscores.
const (
	labelWorkload  = "datum_workload_name"
	labelInstance  = "datum_instance_name"
	labelContainer = "k8s_container_name"
	labelStream    = "log_iostream"

	// A VM's generation is its ukpd instance uuid; a sandbox pod has none, so
	// its pod UID stands in.
	labelVMGeneration  = "container_id"
	labelPodGeneration = "k8s_pod_uid"

	// route_name is "httproute/<namespace>/<httpproxy>/rule/...". Unlike
	// upstream_cluster, it is set even when no upstream was picked.
	labelRouteName = "route_name"
)

// Envoy access log fields, each a stream label.
const (
	labelMethod        = "method"
	labelPath          = "path"
	labelResponseCode  = "response_code"
	labelDuration      = "duration"
	labelAuthority     = "authority"
	labelUpstreamHost  = "upstream_host"
	labelResponseFlags = "response_flags"
	labelRequestID     = "request_id"
	labelUserAgent     = "user_agent"
	labelForwardedFor  = "x_forwarded_for"
)

type matcher struct {
	label string
	op    string
	value string
}

// selector is one LogQL stream selector with an optional line filter. The
// query API rejects `{a} or {b}`, so every filter must fit in one.
type selector struct {
	matchers []matcher
	search   string
}

func (s *selector) eq(label, value string) {
	s.matchers = append(s.matchers, matcher{label, "=", value})
}

func (s *selector) re(label, pattern string) {
	s.matchers = append(s.matchers, matcher{label, "=~", pattern})
}

func (s *selector) oneOf(label string, values []string) {
	switch len(values) {
	case 0:
	case 1:
		s.eq(label, values[0])
	default:
		s.re(label, alternation(values))
	}
}

// noneOf excludes values. A stream without the label still matches.
func (s *selector) noneOf(label string, values []string) {
	switch len(values) {
	case 0:
	case 1:
		s.matchers = append(s.matchers, matcher{label, "!=", values[0]})
	default:
		s.matchers = append(s.matchers, matcher{label, "!~", alternation(values)})
	}
}

func (s selector) clone() selector {
	s.matchers = slices.Clone(s.matchers)
	return s
}

// String renders the selector; LogQL string literals use Go quoting.
func (s selector) String() string {
	parts := make([]string, len(s.matchers))
	for i, m := range s.matchers {
		parts[i] = m.label + m.op + strconv.Quote(m.value)
	}
	q := "{" + strings.Join(parts, ", ") + "}"
	if s.search != "" {
		q += " |= " + strconv.Quote(s.search)
	}
	return q
}

func alternation(values []string) string {
	escaped := make([]string, len(values))
	for i, v := range values {
		escaped[i] = regexp.QuoteMeta(v)
	}
	return strings.Join(escaped, "|")
}

// locationPattern matches a workload's instances in the given locations by
// name: <workload>-<placement>-<location>-<ordinal>, location lowercased. No
// log label carries the location yet; once the collector stamps one
// (datum.location.name, from the pod's compute.datumapis.com/location), match
// that instead.
func locationPattern(workload string, locations []string) string {
	lower := make([]string, len(locations))
	for i, l := range locations {
		lower[i] = strings.ToLower(l)
	}
	return regexp.QuoteMeta(workload) + "-.+-(" + alternation(lower) + ")-[0-9]+"
}

func albRoutePattern(proxies []string) string {
	return "httproute/[^/]+/(" + alternation(proxies) + ")/.*"
}
