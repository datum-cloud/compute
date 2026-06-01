// SPDX-License-Identifier: AGPL-3.0-only

package referenceddata

import (
	"fmt"
	"hash/fnv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// maxNameLength is the maximum length of a Kubernetes object name.
	maxNameLength = 253

	// hashSuffixLength is the number of hex characters appended when a name
	// would otherwise exceed maxNameLength.
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
// If the source name exceeds maxNameLength (253 chars), the name is truncated
// and a deterministic 8-character FNV-1a hex suffix is appended to avoid
// collisions. The returned name always satisfies DNS subdomain constraints
// required by Kubernetes.
func CompanionName(_, sourceName string) string {
	if len(sourceName) <= maxNameLength && isValidDNSSubdomain(sourceName) {
		return sourceName
	}

	// Truncate the source name so that truncated + "-" + hash fits within
	// maxNameLength. Format: "<truncated>-<8-char-hash>"
	hashStr := shortHash(sourceName)
	suffix := "-" + hashStr
	maxSourceLen := maxNameLength - len(suffix)
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
