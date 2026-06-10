// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// TestDeploymentWorkloadUIDIndexFunc verifies that deployments without a
// workload UID are excluded from the index: indexing them under the empty key
// would make them matchable by a GC query built from a corrupt (empty) UID.
func TestDeploymentWorkloadUIDIndexFunc(t *testing.T) {
	t.Parallel()

	withUID := &computev1alpha.WorkloadDeployment{
		Spec: computev1alpha.WorkloadDeploymentSpec{
			WorkloadRef: computev1alpha.WorkloadReference{UID: types.UID("wl-uid-1")},
		},
	}
	assert.Equal(t, []string{"wl-uid-1"}, deploymentWorkloadUIDIndexFunc(withUID))

	withoutUID := &computev1alpha.WorkloadDeployment{}
	assert.Nil(t, deploymentWorkloadUIDIndexFunc(withoutUID),
		"a deployment without a workload UID must not be indexed under the empty key")
}
