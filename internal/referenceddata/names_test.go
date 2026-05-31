// SPDX-License-Identifier: AGPL-3.0-only

package referenceddata

import (
	"strings"
	"testing"
)

func TestCompanionName(t *testing.T) {
	cases := map[string]struct {
		kind       string
		sourceName string
		want       string
	}{
		"configmap simple": {
			kind:       "ConfigMap",
			sourceName: "app-config",
			want:       "configmap.app-config",
		},
		"secret simple": {
			kind:       "Secret",
			sourceName: "db-creds",
			want:       "secret.db-creds",
		},
		"kind already lower": {
			kind:       "configmap",
			sourceName: "cfg",
			want:       "configmap.cfg",
		},
		"secret upper": {
			kind:       "SECRET",
			sourceName: "my-secret",
			want:       "secret.my-secret",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := CompanionName(tc.kind, tc.sourceName)
			if got != tc.want {
				t.Errorf("CompanionName(%q, %q) = %q, want %q", tc.kind, tc.sourceName, got, tc.want)
			}
		})
	}
}

func TestCompanionName_LongName(t *testing.T) {
	// Build a name that would exceed 253 chars.
	longName := strings.Repeat("a", 250)
	result := CompanionName("ConfigMap", longName)

	if len(result) > maxNameLength {
		t.Errorf("CompanionName with long source: len=%d exceeds maxNameLength=%d", len(result), maxNameLength)
	}

	if !isValidDNSSubdomain(result) {
		t.Errorf("CompanionName with long source produced invalid DNS subdomain: %q", result)
	}

	// The result must be deterministic.
	result2 := CompanionName("ConfigMap", longName)
	if result != result2 {
		t.Errorf("CompanionName is not deterministic: %q != %q", result, result2)
	}
}

func TestCompanionName_Deterministic(t *testing.T) {
	// Same inputs always produce the same output.
	for i := 0; i < 100; i++ {
		a := CompanionName("Secret", "my-secret")
		b := CompanionName("Secret", "my-secret")
		if a != b {
			t.Fatalf("non-deterministic: %q != %q", a, b)
		}
	}
}

func TestCompanionNameForRef(t *testing.T) {
	ref := ObjectRef{Kind: "ConfigMap", Name: "app-config", Namespace: "default"}
	got := CompanionNameForRef(ref)
	want := "configmap.app-config"
	if got != want {
		t.Errorf("CompanionNameForRef = %q, want %q", got, want)
	}
}

func TestShortHash_Deterministic(t *testing.T) {
	h1 := shortHash("test-value")
	h2 := shortHash("test-value")
	if h1 != h2 {
		t.Errorf("shortHash is non-deterministic: %q != %q", h1, h2)
	}
	if len(h1) != hashSuffixLength {
		t.Errorf("shortHash len=%d, want %d", len(h1), hashSuffixLength)
	}
}
