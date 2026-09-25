// SPDX-License-Identifier: AGPL-3.0-only

// Package naming builds the names of the objects compute derives from a
// workload.
package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"k8s.io/apimachinery/pkg/types"
)

const (
	deploymentNamePrefixLength = 30
	deploymentNameHashLength   = 10

	// MaxDeploymentNameLength is the longest name DeploymentName returns.
	MaxDeploymentNameLength = deploymentNamePrefixLength + 1 + deploymentNameHashLength
)

// DeploymentName returns the name of the WorkloadDeployment that runs a
// workload's placement at a location. The name is a readable prefix of the
// workload name followed by a hash of the workload UID, placement and
// location, so its length never depends on the placement or location and two
// workloads never share a name.
func DeploymentName(workloadName string, workloadUID types.UID, placement, location string) string {
	prefix := workloadName
	if len(prefix) > deploymentNamePrefixLength {
		prefix = prefix[:deploymentNamePrefixLength]
	}
	prefix = strings.TrimRight(prefix, "-")

	sum := sha256.Sum256([]byte(string(workloadUID) + "\x00" + placement + "\x00" + strings.ToLower(location)))
	return prefix + "-" + hex.EncodeToString(sum[:])[:deploymentNameHashLength]
}
