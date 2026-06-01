// SPDX-License-Identifier: AGPL-3.0-only

package referenceddata

import (
	"strings"
	"testing"
)

const (
	testKindConfigMap = "ConfigMap"
	testKindSecret    = "Secret"
	testNameAppConfig = "app-config"
	testNameDBCreds   = "db-creds"
	testNameCfg       = "cfg"
)

func TestCompanionName(t *testing.T) {
	cases := map[string]struct {
		kind       string
		sourceName string
		want       string
	}{
		"configmap simple": {
			kind:       testKindConfigMap,
			sourceName: testNameAppConfig,
			want:       "configmap.app-config",
		},
		"secret simple": {
			kind:       testKindSecret,
			sourceName: testNameDBCreds,
			want:       "secret.db-creds",
		},
		"kind already lower": {
			kind:       "configmap",
			sourceName: testNameCfg,
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
	result := CompanionName(testKindConfigMap, longName)

	if len(result) > maxNameLength {
		t.Errorf("CompanionName with long source: len=%d exceeds maxNameLength=%d", len(result), maxNameLength)
	}

	if !isValidDNSSubdomain(result) {
		t.Errorf("CompanionName with long source produced invalid DNS subdomain: %q", result)
	}

	// The result must be deterministic.
	result2 := CompanionName(testKindConfigMap, longName)
	if result != result2 {
		t.Errorf("CompanionName is not deterministic: %q != %q", result, result2)
	}
}

func TestCompanionName_AllDashesSource(t *testing.T) {
	// A source name composed entirely of '-' characters exceeds maxNameLength
	// when prefixed. After TrimRight, truncated becomes "". The function must
	// produce a valid DNS subdomain: "<prefix>.<hash>" (no leading '-').
	longDashes := strings.Repeat("-", 250)
	result := CompanionName(testKindConfigMap, longDashes)

	if len(result) > maxNameLength {
		t.Errorf("len=%d exceeds maxNameLength=%d", len(result), maxNameLength)
	}
	if !isValidDNSSubdomain(result) {
		t.Errorf("produced invalid DNS subdomain: %q", result)
	}
	// Must not contain a segment starting with '-'.
	for _, seg := range strings.Split(result, ".") {
		if strings.HasPrefix(seg, "-") {
			t.Errorf("segment starts with '-': %q in %q", seg, result)
		}
	}
}

func TestCompanionName_AllDotsSource(t *testing.T) {
	// A source name composed entirely of '.' characters has the same edge:
	// TrimRight wipes it out, producing "<prefix>.<hash>".
	longDots := strings.Repeat(".", 250)
	result := CompanionName(testKindConfigMap, longDots)

	if len(result) > maxNameLength {
		t.Errorf("len=%d exceeds maxNameLength=%d", len(result), maxNameLength)
	}
	if !isValidDNSSubdomain(result) {
		t.Errorf("produced invalid DNS subdomain: %q", result)
	}
}

func TestCompanionName_NameEndingOnDot(t *testing.T) {
	// A source name whose truncation point lands exactly on a '.'.  The
	// trailing '.' is stripped and the result must still be a valid subdomain.
	//
	// "configmap." is 10 chars; maxNameLength=253; suffix="-HHHHHHHH" (9).
	// maxSourceLen = 253 - 9 (prefix "configmap") - 1 (dot) - 9 (suffix) = 234.
	// Build a name that is exactly 234 chars and ends with '.'.
	base := strings.Repeat("a", 233) + "."
	result := CompanionName("configmap", base)

	if len(result) > maxNameLength {
		t.Errorf("len=%d exceeds maxNameLength=%d", len(result), maxNameLength)
	}
	if !isValidDNSSubdomain(result) {
		t.Errorf("produced invalid DNS subdomain: %q", result)
	}
}

func TestCompanionName_ValidPrefix(t *testing.T) {
	// Positive case: a simple name that fits within maxNameLength without
	// truncation should be returned unchanged in "<prefix>.<source>" form.
	result := CompanionName(testKindSecret, "my-secret")
	want := "secret.my-secret"
	if result != want {
		t.Errorf("CompanionName = %q, want %q", result, want)
	}
	if !isValidDNSSubdomain(result) {
		t.Errorf("produced invalid DNS subdomain: %q", result)
	}
}

func TestCompanionName_Deterministic(t *testing.T) {
	// Same inputs always produce the same output.
	for i := 0; i < 100; i++ {
		a := CompanionName(testKindSecret, "my-secret")
		b := CompanionName(testKindSecret, "my-secret")
		if a != b {
			t.Fatalf("non-deterministic: %q != %q", a, b)
		}
	}
}

func TestCompanionNameForRef(t *testing.T) {
	ref := ObjectRef{Kind: testKindConfigMap, Name: testNameAppConfig, Namespace: "default"}
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
