// SPDX-License-Identifier: AGPL-3.0-only

package controller

import "strings"

// The cross-plane cluster/project identity travels as a single Kubernetes label
// value: NSO's MappedNamespaceResourceStrategy encodes the name as "cluster-<name>"
// with "/" replaced by "_", so a full project path ("org/project") survives as a
// legal label value ("cluster-org_project").

// EncodeClusterName renders a project/cluster name into the label wire form
// "cluster-<name>" with "/" replaced by "_".
func EncodeClusterName(name string) string {
	return "cluster-" + strings.ReplaceAll(name, "/", "_")
}

// DecodeClusterName reverses EncodeClusterName, returning the full path.
func DecodeClusterName(encoded string) string {
	return strings.ReplaceAll(strings.TrimPrefix(encoded, "cluster-"), "_", "/")
}
