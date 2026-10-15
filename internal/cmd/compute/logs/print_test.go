// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"

	logsapi "go.miloapis.com/telemetry/cli/logs"
)

func appLine(ts time.Time, gen, text string) logsapi.Entry {
	return logsapi.Entry{Time: ts, Line: text, Labels: map[string]string{
		labelWorkload:     testWorkload,
		labelInstance:     testInstance,
		labelVMGeneration: gen,
	}}
}

func TestPrinter(t *testing.T) {
	defer func(v bool) { color.NoColor = v }(color.NoColor)
	color.NoColor = true

	ts := time.Date(2026, 9, 30, 14, 3, 22, 121_000_005, time.UTC)
	tests := []struct {
		name       string
		setup      func(*printer)
		entries    []logsapi.Entry
		want       string
		wantStatus string
	}{
		{
			name: "text marks generation changes",
			entries: []logsapi.Entry{
				appLine(ts, "g1", "one"),
				appLine(ts.Add(time.Second), "g2", "two"),
			},
			want: "[default-us-central-1-0] Sep 30 14:03:22.121  one\n" +
				"[default-us-central-1-0] Sep 30 14:03:23.121  two\n",
			wantStatus: "[default-us-central-1-0] new generation g2 (replaced or redeployed)\n",
		},
		{
			name:    "text without decorations",
			setup:   func(p *printer) { p.prefix, p.timestamps = false, false },
			entries: []logsapi.Entry{appLine(ts, "", "plain")},
			want:    "plain\n",
		},
		{
			name:    "json omits empty fields",
			setup:   func(p *printer) { p.json = true },
			entries: []logsapi.Entry{appLine(ts, "g1", "hi")},
			want: `{"generation":"g1","instance":"api-default-us-central-1-0","line":"hi",` +
				`"timestamp":"2026-09-30T14:03:22.121000005Z","workload":"api"}` + "\n",
		},
		{
			name:  "access log",
			setup: func(p *printer) { p.alb = true },
			entries: []logsapi.Entry{{Time: ts, Labels: map[string]string{
				labelMethod: "GET", labelResponseCode: "503", labelDuration: "3", labelPath: "/healthz",
			}}},
			want: "Sep 30 14:03:22.121  GET    503      3ms  /healthz\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, status strings.Builder
			p := &printer{
				out:        &out,
				status:     &status,
				workload:   testWorkload,
				timestamps: true,
				prefix:     true,
				loc:        time.UTC,
			}
			if tt.setup != nil {
				tt.setup(p)
			}
			for _, e := range tt.entries {
				p.emit(e)
			}
			if out.String() != tt.want {
				t.Errorf("out = %q, want %q", out.String(), tt.want)
			}
			if status.String() != tt.wantStatus {
				t.Errorf("status = %q, want %q", status.String(), tt.wantStatus)
			}
		})
	}
}
