// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"regexp"
	"testing"
)

func TestSelectorString(t *testing.T) {
	tests := []struct {
		name  string
		build func(*selector)
		want  string
	}{
		{
			name:  "quotes and backslashes are escaped",
			build: func(s *selector) { s.eq("a", `say "hi" \o/`) },
			want:  `{a="say \"hi\" \\o/"}`,
		},
		{
			name:  "one value uses equality",
			build: func(s *selector) { s.oneOf("a", []string{"x.y"}); s.noneOf("b", []string{"x.y"}) },
			want:  `{a="x.y", b!="x.y"}`,
		},
		{
			name:  "several values use an escaped alternation",
			build: func(s *selector) { s.oneOf("a", []string{"p.q", "z+"}); s.noneOf("b", []string{"x", "y"}) },
			want:  `{a=~"p\\.q|z\\+", b!~"x|y"}`,
		},
		{
			name:  "no values add nothing",
			build: func(s *selector) { s.eq("a", "1"); s.oneOf("b", nil); s.noneOf("c", nil) },
			want:  `{a="1"}`,
		},
		{
			name:  "search becomes a line filter",
			build: func(s *selector) { s.eq("a", "1"); s.search = `"quoted"` },
			want:  `{a="1"} |= "\"quoted\""`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s selector
			tt.build(&s)
			if got := s.String(); got != tt.want {
				t.Errorf("String() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestPatterns(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		matches map[string]bool
	}{
		{
			name:    "location",
			pattern: locationPattern(testWorkload, []string{"US-East-1", "us-central-1"}),
			matches: map[string]bool{
				"api-default-us-east-1-0":    true,
				"api-dfw-b-us-central-1-12":  true,
				"api-default-us-east-1b-0":   false,
				"api-v2-default-us-west-1-0": false,
				"other-default-us-east-1-0":  false,
			},
		},
		{
			name:    "alb route",
			pattern: albRoutePattern([]string{testWorkload, "api.v2"}),
			matches: map[string]bool{
				"httproute/ns-1/api/rule/0/match/0/host": true,
				"httproute/ns-1/api.v2/rule/1":           true,
				"httproute/ns-1/api-other/rule/0":        false,
				"httproute/ns-1/apixv2/rule/0":           false,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The query API anchors matcher regexes, as Loki does.
			re := regexp.MustCompile("^(?:" + tt.pattern + ")$")
			for s, want := range tt.matches {
				if got := re.MatchString(s); got != want {
					t.Errorf("%s matches %q = %v, want %v", tt.pattern, s, got, want)
				}
			}
		})
	}
}
