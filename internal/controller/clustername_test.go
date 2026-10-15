// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"testing"
)

func TestEncodeDecodeClusterName_RoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{name: "simple name", input: "datum-cloud"},
		{name: "org/project path", input: "org/project"},
		{name: "three-segment path", input: "a/b/c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DecodeClusterName(EncodeClusterName(tc.input))
			if got != tc.input {
				t.Errorf("round-trip(%q): got %q, want %q", tc.input, got, tc.input)
			}
		})
	}
}
