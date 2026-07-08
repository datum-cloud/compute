// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	multiclusterproviders "go.miloapis.com/milo/pkg/multicluster-runtime"
)

// TestComputeWatchProviderClaims is the #171 guard: quota enforcement (and thus
// the ResourceClaim watch) is wired only in Milo mode. Single/cluster mode must
// stay false, so the manager never engages a ResourceClaim watch against a cell
// that has no quota CRD.
func TestComputeWatchProviderClaims(t *testing.T) {
	assert.True(t, computeWatchProviderClaims(multiclusterproviders.ProviderMilo),
		"milo mode wires the ResourceClaim watch")
	assert.False(t, computeWatchProviderClaims(multiclusterproviders.ProviderSingle),
		"single mode must not wire the watch (#171)")
	assert.False(t, computeWatchProviderClaims(multiclusterproviders.ProviderKind),
		"non-milo modes disable quota")
}
