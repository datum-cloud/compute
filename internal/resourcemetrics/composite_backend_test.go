// SPDX-License-Identifier: AGPL-3.0-only

package resourcemetrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompositeBackendPrefersLaterBackendForGet(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	standard := testBackend{pods: []PodMetric{{
		Namespace:  "default",
		Name:       "pod",
		Timestamp:  now,
		Window:     30 * time.Second,
		Containers: []ContainerMetric{{Name: "app", Usage: resourceList("100m", "128Mi")}},
	}}}
	provider := testBackend{pods: []PodMetric{{
		Namespace:  "default",
		Name:       "pod",
		Timestamp:  now,
		Window:     30 * time.Second,
		Containers: []ContainerMetric{{Name: "app", Usage: resourceList("500m", "512Mi")}},
	}}}

	metric, err := NewCompositeBackend(standard, provider).GetPodMetric(t.Context(), "default", "pod")
	require.NoError(t, err)
	require.Len(t, metric.Containers, 1)
	assert.Equal(t, "500m", metric.Containers[0].Usage.Cpu().String())
}
