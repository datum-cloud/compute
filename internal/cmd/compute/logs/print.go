// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"strings"
	"time"

	"github.com/fatih/color"

	logsapi "go.miloapis.com/telemetry/cli/logs"
)

const timestampLayout = "Jan 02 15:04:05.000"

var (
	timestampColor = color.New(color.Faint)
	markerColor    = color.New(color.FgYellow, color.Bold)
)

var prefixColors = []color.Attribute{
	color.FgCyan, color.FgGreen, color.FgYellow, color.FgMagenta,
	color.FgBlue, color.FgHiCyan, color.FgHiGreen, color.FgHiMagenta,
}

type printer struct {
	out    io.Writer
	status io.Writer

	json       bool
	alb        bool
	workload   string
	timestamps bool
	prefix     bool
	loc        *time.Location

	width   int // widest prefix so far
	lastGen map[string]generation
	printed int
}

func (p *printer) emit(e logsapi.Entry) {
	if p.alb {
		p.emitALB(e)
	} else {
		p.emitApp(e)
	}
}

func (p *printer) emitApp(e logsapi.Entry) {
	inst := e.Labels[labelInstance]
	container := e.Labels[labelContainer]
	gen := generationOf(e.Labels)

	if p.json {
		p.writeJSON(map[string]string{
			"timestamp":  rfc3339(e.Time),
			"workload":   e.Labels[labelWorkload],
			"instance":   inst,
			"container":  container,
			"stream":     e.Labels[labelStream],
			"generation": gen.id,
			"line":       e.Line,
		})
		return
	}

	if p.lastGen == nil {
		p.lastGen = map[string]generation{}
	}
	if prev, ok := p.lastGen[inst]; ok && gen.id != "" && prev != gen {
		fmt.Fprintln(p.status, p.prefixFor(inst, container)+" "+markerColor.Sprintf("new generation %s (replaced or redeployed)", shortID(gen.id)))
	}
	if gen.id != "" {
		p.lastGen[inst] = gen
	}

	var b strings.Builder
	if p.prefix {
		b.WriteString(p.prefixFor(inst, container) + " ")
	}
	if p.timestamps {
		b.WriteString(p.timestamp(e.Time) + "  ")
	}
	b.WriteString(e.Line)
	p.writeLine(b.String())
}

// emitALB builds the line from labels; access log bodies are empty.
func (p *printer) emitALB(e logsapi.Entry) {
	l := e.Labels
	if p.json {
		p.writeJSON(map[string]string{
			"timestamp":   rfc3339(e.Time),
			"method":      l[labelMethod],
			"path":        l[labelPath],
			"status":      l[labelResponseCode],
			"durationMs":  l[labelDuration],
			"host":        l[labelAuthority],
			"upstream":    l[labelUpstreamHost],
			"flags":       l[labelResponseFlags],
			"requestId":   l[labelRequestID],
			"userAgent":   l[labelUserAgent],
			"forwardedIp": l[labelForwardedFor],
		})
		return
	}

	line := fmt.Sprintf("%-6s %s %6sms  %s", l[labelMethod], l[labelResponseCode], l[labelDuration], l[labelPath])
	if p.timestamps {
		line = p.timestamp(e.Time) + "  " + line
	}
	p.writeLine(line)
}

func (p *printer) writeLine(s string) {
	fmt.Fprintln(p.out, s)
	p.printed++
}

// writeJSON omits empty fields.
func (p *printer) writeJSON(fields map[string]string) {
	for k, v := range fields {
		if v == "" {
			delete(fields, k)
		}
	}
	b, _ := json.Marshal(fields)
	p.writeLine(string(b))
}

// timestamp is dimmed so it reads apart from timestamps in the line itself.
func (p *printer) timestamp(t time.Time) string {
	return timestampColor.Sprint(t.In(p.loc).Format(timestampLayout))
}

func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// short drops the workload name every instance of it starts with.
func (p *printer) short(inst string) string {
	if s := strings.TrimPrefix(inst, p.workload+"-"); s != "" {
		return s
	}
	return inst
}

func (p *printer) prefixFor(inst, container string) string {
	label := p.short(inst)
	if container != "" {
		label += "/" + container
	}
	label = "[" + label + "]"

	// Pad to the widest prefix so far so lines align as instances appear.
	p.width = max(p.width, len(label))
	label += strings.Repeat(" ", p.width-len(label))

	h := fnv.New32a()
	h.Write([]byte(inst))
	return color.New(prefixColors[h.Sum32()%uint32(len(prefixColors))]).Sprint(label)
}

func shortID(id string) string {
	return id[:min(8, len(id))]
}
