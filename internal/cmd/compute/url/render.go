// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"fmt"
	"io"
	"strings"

	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// RenderDetail writes the per-URL detail view: where the URL is, what backs
// it, and — the part a multi-location platform owes its users — which one is
// actually serving.
//
// Nothing here names the machinery. When something is wrong, the server's own
// reason and message are printed verbatim, so a condition this CLI has never
// heard of still reaches the user.
//
// A nil Info means the workload has no URL; the caller says so in its own
// words, because only it knows how the user asked.
func RenderDetail(out io.Writer, info *Info) {
	if info == nil {
		return
	}

	renderURLs(out, info)
	renderBackendLine(out, info)
	fmt.Fprintln(out)
	// "Serving", not "Health": this block is embedded under a workload's own
	// Health line, and two identically labelled rows at the same indent read as
	// a contradiction rather than two different facts. Backend health is about
	// whether the URL is taking traffic.
	fmt.Fprintf(out, "%-*s %s\n", labelWidth, "Serving", health(info))

	if len(info.Locations) > 0 {
		fmt.Fprintln(out)
		renderLocations(out, info)
	}

	renderDiagnosis(out, info)
}

// renderURLs lists every hostname the URL answers on, the working one first.
// A hostname that is not serving carries the server's reason beside it.
func renderURLs(out io.Writer, info *Info) {
	label := "URL"
	if len(info.Hostnames) == 0 {
		fmt.Fprintf(out, "%-*s %s\n", labelWidth, label, "—")
		return
	}

	for _, h := range info.Hostnames {
		line := h.URL
		if !h.Active {
			line += "  (" + h.Status + ")"
		}
		fmt.Fprintf(out, "%-*s %s\n", labelWidth, label, line)
		label = ""
	}
}

// renderBackendLine states what the edge forwards to.
func renderBackendLine(out io.Writer, info *Info) {
	if info.Port == 0 {
		return
	}
	protocol := strings.ToLower(info.Protocol)
	if protocol == "" {
		protocol = strings.ToLower(string(networkingv1alpha.NetworkServiceProtocolTCP))
	}
	fmt.Fprintf(out, "%-*s port %d/%s\n", labelWidth, "Backend", info.Port, protocol)
}

// renderLocations prints the per-location breakdown, indented under the label
// column so it reads as part of the health block.
func renderLocations(out io.Writer, info *Info) {
	indent := strings.Repeat(" ", labelWidth+1)
	tw := util.NewTabWriter(out)
	fmt.Fprintf(tw, "%sLOCATION\tBACKENDS\tHEALTHY\tSERVING\n", indent)
	for _, l := range info.Locations {
		fmt.Fprintf(tw, "%s%s\t%d\t%d\t%s\n", indent, l.Location, l.Backends, l.Healthy, yesNo(l.Serving))
	}
	_ = tw.Flush()
}

// health summarises the URL in one line, from counts rather than from any
// reason string.
func health(info *Info) string {
	b := info.Backends
	switch {
	case b.Total == 0:
		return "Unavailable — no backends registered"
	case b.Healthy == 0:
		return fmt.Sprintf("Unavailable — 0 of %d backends healthy", b.Total)
	case b.Healthy < b.Total:
		return fmt.Sprintf("Degraded — %d of %d backends healthy", b.Healthy, b.Total)
	default:
		return fmt.Sprintf("Healthy — %d of %d backends healthy", b.Healthy, b.Total)
	}
}

// renderDiagnosis explains a URL that is not fully healthy and says what to
// run next. A healthy URL gets nothing: there is nothing to do.
func renderDiagnosis(out io.Writer, info *Info) {
	var unhealthy []string
	for _, l := range info.Locations {
		if l.Backends > 0 && l.Healthy == 0 {
			unhealthy = append(unhealthy, l.Location)
		}
	}

	blocking := blockingLines(info)
	if len(unhealthy) == 0 && len(blocking) == 0 {
		return
	}

	fmt.Fprintln(out)
	indent := strings.Repeat(" ", 7)

	for _, location := range unhealthy {
		fmt.Fprintf(out, "  %s: no healthy backends — instances are running but not passing health checks.\n", location)
		serving := servingCities(info)
		switch {
		case len(serving) == 0:
			fmt.Fprintf(out, "%sNo location is taking traffic, so the URL is not answering.\n", indent)
		case len(serving) == 1:
			fmt.Fprintf(out, "%sTraffic is being served from %s only.\n", indent, serving[0])
		default:
			fmt.Fprintf(out, "%sTraffic is being served from %s.\n", indent, strings.Join(serving, ", "))
		}
	}

	for _, line := range blocking {
		fmt.Fprintf(out, "  %s\n", line)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Next steps:")
	if len(unhealthy) == 0 {
		fmt.Fprintf(out, "    Check instances:  datumctl compute instances --workload=%s\n", info.WorkloadName)
		return
	}
	for _, location := range unhealthy {
		fmt.Fprintf(out, "    Check instances:  datumctl compute instances --workload=%s --location=%s\n", info.WorkloadName, location)
	}
}

// blockingLines collects what the server says is wrong, in the server's own
// words. The condition reason is never shown — see HumanBlock.
func blockingLines(info *Info) []string {
	var lines []string
	add := func(reason, message string) {
		lines = append(lines, HumanBlock(reason, message))
	}

	if reason, message, blocked := util.ReadinessBlock(info.ServiceConditions, networkingv1alpha.NetworkServiceReady); blocked && reason != "" {
		add(reason, message)
	}
	if reason, message, blocked := util.ReadinessBlock(info.ProxyConditions, networkingv1alpha.HTTPProxyConditionProgrammed); blocked && reason != "" {
		add(reason, message)
	}
	for _, h := range info.Hostnames {
		if !h.Active && h.Detail != "" {
			lines = append(lines, fmt.Sprintf("%s: %s", h.Hostname, h.Detail))
		}
	}
	return lines
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
