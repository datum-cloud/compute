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
	testNameMySecret  = "my-secret"
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
			want:       testNameAppConfig,
		},
		"secret simple": {
			kind:       testKindSecret,
			sourceName: testNameDBCreds,
			want:       testNameDBCreds,
		},
		"kind already lower": {
			kind:       "configmap",
			sourceName: testNameCfg,
			want:       testNameCfg,
		},
		"secret upper": {
			kind:       "SECRET",
			sourceName: testNameMySecret,
			want:       testNameMySecret,
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

// TestCompanionName_SourceNameContract is the seam-crossing contract test.
// It asserts that CompanionName always returns the source name unchanged for
// names that fit in 253 chars and satisfy DNS subdomain rules — regardless of
// the kind argument. Old prefixed code would fail this contract.
func TestCompanionName_SourceNameContract(t *testing.T) {
	cases := []struct {
		kind string
		name string
	}{
		{testKindConfigMap, testNameAppConfig},
		{testKindSecret, testNameAppConfig}, // same source name, different kind
		{testKindConfigMap, testNameDBCreds},
		{"configmap", "my-cm"},
		{"SECRET", testNameMySecret},
	}

	for _, tc := range cases {
		got := CompanionName(tc.kind, tc.name)
		if got != tc.name {
			t.Errorf("CompanionName(%q, %q) = %q, want source name %q (contract violation)",
				tc.kind, tc.name, got, tc.name)
		}
	}
}

// TestCompanionName_SameSourceDifferentKind asserts that a ConfigMap and a
// Secret with the same source name produce the same companion name. Cross-kind
// collision is safe because ConfigMap and Secret are distinct object types in
// Kubernetes — they cannot conflict in the same namespace.
func TestCompanionName_SameSourceDifferentKind(t *testing.T) {
	name := testNameAppConfig
	cmCompanion := CompanionName(testKindConfigMap, name)
	secretCompanion := CompanionName(testKindSecret, name)

	if cmCompanion != name {
		t.Errorf("ConfigMap companion = %q, want %q", cmCompanion, name)
	}
	if secretCompanion != name {
		t.Errorf("Secret companion = %q, want %q", secretCompanion, name)
	}
	if cmCompanion != secretCompanion {
		t.Errorf("same source name must produce the same companion name regardless of kind: %q != %q",
			cmCompanion, secretCompanion)
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
	// when long. After TrimRight, truncated becomes "". The function must
	// produce a valid DNS subdomain (just the hash).
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
	// TrimRight wipes it out, producing just the hash.
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
	// A source name whose truncation point lands exactly on a '.'. The
	// trailing '.' is stripped and the result must still be a valid subdomain.
	//
	// maxNameLength=253; suffix="-HHHHHHHH" (9).
	// maxSourceLen = 253 - 9 (suffix) = 244.
	// Build a name that is exactly 244 chars and ends with '.'.
	base := strings.Repeat("a", 243) + "."
	result := CompanionName("configmap", base)

	if len(result) > maxNameLength {
		t.Errorf("len=%d exceeds maxNameLength=%d", len(result), maxNameLength)
	}
	if !isValidDNSSubdomain(result) {
		t.Errorf("produced invalid DNS subdomain: %q", result)
	}
}

func TestCompanionName_ValidShortName(t *testing.T) {
	// Positive case: a simple name that fits within maxNameLength without
	// truncation should be returned unchanged.
	result := CompanionName(testKindSecret, testNameMySecret)
	want := testNameMySecret
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
		a := CompanionName(testKindSecret, testNameMySecret)
		b := CompanionName(testKindSecret, testNameMySecret)
		if a != b {
			t.Fatalf("non-deterministic: %q != %q", a, b)
		}
	}
}

func TestCompanionNameForRef(t *testing.T) {
	ref := ObjectRef{Kind: testKindConfigMap, Name: testNameAppConfig, Namespace: "default"}
	got := CompanionNameForRef(ref)
	want := testNameAppConfig
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

func TestCompanionToken(t *testing.T) {
	cases := []struct {
		kind string
		name string
		want string
	}{
		{testKindConfigMap, testNameAppConfig, testKindConfigMap + "/" + testNameAppConfig},
		{testKindSecret, testNameDBCreds, testKindSecret + "/" + testNameDBCreds},
		{testKindSecret, testNameAppConfig, testKindSecret + "/" + testNameAppConfig},
	}
	for _, tc := range cases {
		got := CompanionToken(tc.kind, tc.name)
		if got != tc.want {
			t.Errorf("CompanionToken(%q, %q) = %q, want %q", tc.kind, tc.name, got, tc.want)
		}
	}
}
