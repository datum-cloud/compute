// SPDX-License-Identifier: AGPL-3.0-only

package referenceddata

import (
	"fmt"
	"hash/fnv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// MaxCompanionNameLength is the longest companion name that can propagate.
	//
	// It is 10 characters below Kubernetes' 253-character name limit because the
	// federation engine derives a companion's binding name by appending a kind
	// suffix ("-configmap", the longer of the two kinds this package names). A
	// companion named right up to 253 characters would produce a binding name
	// the API server rejects, and the companion would never leave the hub.
	//
	// The resolver rejects an over-long source rather than shortening it:
	// consumers reference a companion by its source name with no translation
	// step, so a shortened companion would propagate successfully and then fail
	// to mount.
	MaxCompanionNameLength = 243

	// hashSuffixLength is the number of hex characters appended when a name
	// would otherwise exceed MaxCompanionNameLength.
	hashSuffixLength = 8
)

// CompanionName returns the deterministic companion object name for a given
// (kind, sourceName) pair. The companion is named after the SOURCE name only —
// no kind prefix — so that consumer references (volumes, env, envFrom) resolve
// naturally without any translation layer.
//
// Cross-kind collisions are safe: a ConfigMap companion and a Secret companion
// may both be named "app-config" because they are distinct Kubernetes objects of
// different resource types in the same namespace.
//
// Callers reject a source name longer than [MaxCompanionNameLength] before
// reaching here, so the shortening path below only runs for a name that is not
// a valid DNS subdomain — which the API server does not allow an existing
// ConfigMap or Secret to have. It remains as a total function so a name is
// always returned, never an invalid one.
//
// Shortening truncates and appends a deterministic 8-character FNV-1a hex
// suffix to avoid collisions. The returned name always satisfies the DNS
// subdomain constraints Kubernetes requires.
func CompanionName(_, sourceName string) string {
	if len(sourceName) <= MaxCompanionNameLength && isValidDNSSubdomain(sourceName) {
		return sourceName
	}

	// Truncate the source name so that truncated + "-" + hash fits within
	// MaxCompanionNameLength. Format: "<truncated>-<8-char-hash>"
	hashStr := shortHash(sourceName)
	suffix := "-" + hashStr
	maxSourceLen := MaxCompanionNameLength - len(suffix)
	if maxSourceLen < 1 {
		maxSourceLen = 1
	}

	truncated := sourceName
	if len(truncated) > maxSourceLen {
		truncated = truncated[:maxSourceLen]
	}
	// Strip any trailing non-alphanumeric characters to keep the name clean.
	truncated = strings.TrimRight(truncated, "-.")

	// If stripping trailing separators emptied the truncated segment (e.g. a
	// source name composed entirely of '-' or '.'), fall back to just the hash.
	if truncated == "" {
		return hashStr
	}

	return fmt.Sprintf("%s%s", truncated, suffix)
}

// CompanionNameForRef is a convenience wrapper around CompanionName that
// accepts an ObjectRef.
func CompanionNameForRef(ref ObjectRef) string {
	return CompanionName(ref.Kind, ref.Name)
}

// CompanionToken returns the kind-qualified token "Kind/name" used in the
// expected-referenced-data annotation so that the cell can disambiguate
// companions by kind without probing both resource types.
func CompanionToken(kind, name string) string {
	return kind + "/" + name
}

// isValidDNSSubdomain returns true if s satisfies Kubernetes DNS subdomain
// naming rules.
func isValidDNSSubdomain(s string) bool {
	return len(validation.IsDNS1123Subdomain(s)) == 0
}

// shortHash returns an 8-character hex string derived from FNV-1a of the input.
// Used as a collision-avoidance suffix when names are truncated.
func shortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}
