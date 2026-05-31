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
// (kind, sourceName) pair. The format is "<lower-kind>.<source-name>", for
// example:
//
//	CompanionName("ConfigMap", "app-config") → "configmap.app-config"
//	CompanionName("Secret",    "db-creds")   → "secret.db-creds"
//
// If the resulting name exceeds maxNameLength, the source-name portion is
// truncated and a deterministic 8-character FNV-1a hex suffix is appended to
// avoid collisions. The returned name always satisfies DNS subdomain
// constraints required by Kubernetes.
func CompanionName(kind, sourceName string) string {
	prefix := strings.ToLower(kind)
	candidate := fmt.Sprintf("%s.%s", prefix, sourceName)

	if len(candidate) <= maxNameLength && isValidDNSSubdomain(candidate) {
		return candidate
	}

	// Truncate the source name so that prefix + "." + truncated + "-" + hash
	// fits within maxNameLength.
	// Format: "<prefix>.<truncated>-<8-char-hash>"
	hashStr := shortHash(sourceName)
	suffix := "-" + hashStr
	maxSourceLen := maxNameLength - len(prefix) - 1 /* dot */ - len(suffix)
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
	// source name composed entirely of '-' or '.'), omit the segment entirely
	// so we produce "<prefix>.<hash>" rather than "<prefix>.-<hash>" which
	// would start a DNS label with '-'.
	if truncated == "" {
		return fmt.Sprintf("%s.%s", prefix, hashStr)
	}

	return fmt.Sprintf("%s.%s%s", prefix, truncated, suffix)
}

// CompanionNameForRef is a convenience wrapper around CompanionName that
// accepts an ObjectRef.
func CompanionNameForRef(ref ObjectRef) string {
	return CompanionName(ref.Kind, ref.Name)
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
