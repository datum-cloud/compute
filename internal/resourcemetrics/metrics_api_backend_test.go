// SPDX-License-Identifier: AGPL-3.0-only

package resourcemetrics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func TestMetricsAPIBackendGetPodMetric(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/apis/metrics.k8s.io/v1beta1/namespaces/default/pods/example", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		metric := metricsv1beta1.PodMetrics{
			TypeMeta:   metav1.TypeMeta{APIVersion: "metrics.k8s.io/v1beta1", Kind: "PodMetrics"},
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "example"},
			Timestamp:  metav1.NewTime(now),
			Window:     metav1.Duration{Duration: 30 * time.Second},
			Containers: []metricsv1beta1.ContainerMetrics{{
				Name:  "app",
				Usage: resourceList("100m", "128Mi"),
			}},
		}
		require.NoError(t, json.NewEncoder(w).Encode(metric))
	}))
	t.Cleanup(server.Close)

	backend, err := NewMetricsAPIBackendForURL(server.URL, MetricsAPIBackendOptions{})
	require.NoError(t, err)

	metric, err := backend.GetPodMetric(t.Context(), "default", "example")
	require.NoError(t, err)
	assert.Equal(t, "default", metric.Namespace)
	assert.Equal(t, "example", metric.Name)
	require.Len(t, metric.Containers, 1)
	assert.Equal(t, "100m", metric.Containers[0].Usage.Cpu().String())
	assert.Equal(t, "128Mi", metric.Containers[0].Usage.Memory().String())
}

func TestMetricsAPIBackendNotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)

	backend, err := NewMetricsAPIBackendForURL(server.URL, MetricsAPIBackendOptions{})
	require.NoError(t, err)
	_, err = backend.GetPodMetric(t.Context(), "default", "missing")
	assert.ErrorIs(t, err, ErrNotFound)
}
