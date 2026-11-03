// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"regexp"
	"strings"

	"go.miloapis.com/telemetry/cli/logql"
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
	return regexp.QuoteMeta(workload) + "-.+-(" + logql.Alternation(lower...) + ")-[0-9]+"
}

func albRoutePattern(proxies []string) string {
	return "httproute/[^/]+/(" + logql.Alternation(proxies...) + ")/.*"
}
