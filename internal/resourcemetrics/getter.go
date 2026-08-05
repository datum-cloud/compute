// SPDX-License-Identifier: AGPL-3.0-only

package resourcemetrics

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/metrics/pkg/apis/metrics"
)

type MetricsGetter struct {
	backend Backend
}

func NewMetricsGetter(backend Backend) *MetricsGetter {
	return &MetricsGetter{backend: backend}
}

func (g *MetricsGetter) GetPodMetrics(pods ...*metav1.PartialObjectMetadata) ([]metrics.PodMetrics, error) {
	items := make([]metrics.PodMetrics, 0, len(pods))
	for _, pod := range pods {
		metric, err := g.backend.GetPodMetric(context.Background(), pod.Namespace, pod.Name)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		items = append(items, podMetricToAPI(metric))
	}
	return items, nil
}

func (g *MetricsGetter) GetNodeMetrics(nodes ...*corev1.Node) ([]metrics.NodeMetrics, error) {
	items := make([]metrics.NodeMetrics, 0, len(nodes))
	for _, node := range nodes {
		metric, err := g.backend.GetNodeMetric(context.Background(), node.Name)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		items = append(items, nodeMetricToAPI(metric))
	}
	return items, nil
}

func podMetricToAPI(metric PodMetric) metrics.PodMetrics {
	containers := make([]metrics.ContainerMetrics, 0, len(metric.Containers))
	for _, container := range metric.Containers {
		containers = append(containers, metrics.ContainerMetrics{Name: container.Name, Usage: container.Usage})
	}
	return metrics.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{Name: metric.Name, Namespace: metric.Namespace},
		Timestamp:  metav1.NewTime(metric.Timestamp),
		Window:     metav1.Duration{Duration: metric.Window},
		Containers: containers,
	}
}

func nodeMetricToAPI(metric NodeMetric) metrics.NodeMetrics {
	return metrics.NodeMetrics{
		ObjectMeta: metav1.ObjectMeta{Name: metric.Name},
		Timestamp:  metav1.NewTime(metric.Timestamp),
		Window:     metav1.Duration{Duration: metric.Window},
		Usage:      metric.Usage,
	}
}
