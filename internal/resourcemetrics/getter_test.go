// SPDX-License-Identifier: AGPL-3.0-only

package resourcemetrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMetricsGetterGetPodMetrics(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	getter := NewMetricsGetter(testBackend{pods: []PodMetric{{
		Namespace: "default",
		Name:      "example",
		Timestamp: now,
		Window:    30 * time.Second,
		Containers: []ContainerMetric{{
			Name:  "app",
			Usage: resourceList("100m", "128Mi"),
		}},
	}}})

	metrics, err := getter.GetPodMetrics(&metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "example"}})
	require.NoError(t, err)
	require.Len(t, metrics, 1)
	assert.Equal(t, "default", metrics[0].Namespace)
	assert.Equal(t, "example", metrics[0].Name)
	require.Len(t, metrics[0].Containers, 1)
	assert.Equal(t, "100m", metrics[0].Containers[0].Usage.Cpu().String())
	assert.Equal(t, "128Mi", metrics[0].Containers[0].Usage.Memory().String())
}

func TestMetricsGetterSkipsMissingPodMetrics(t *testing.T) {
	t.Parallel()

	getter := NewMetricsGetter(EmptyBackend{})
	metrics, err := getter.GetPodMetrics(&metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "missing"}})
	require.NoError(t, err)
	assert.Empty(t, metrics)
}
