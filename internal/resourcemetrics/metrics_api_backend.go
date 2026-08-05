// SPDX-License-Identifier: AGPL-3.0-only

package resourcemetrics

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

type MetricsAPIBackend struct {
	client metricsclient.Interface
}

type MetricsAPIBackendOptions struct {
	InsecureSkipTLSVerify bool
	BearerTokenFile       string
}

func NewMetricsAPIBackend(client metricsclient.Interface) *MetricsAPIBackend {
	return &MetricsAPIBackend{client: client}
}

func NewMetricsAPIBackendForURL(sourceURL string, opts MetricsAPIBackendOptions) (*MetricsAPIBackend, error) {
	config := &rest.Config{
		Host: sourceURL,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: opts.InsecureSkipTLSVerify,
		},
		BearerTokenFile: opts.BearerTokenFile,
	}
	client, err := metricsclient.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return NewMetricsAPIBackend(client), nil
}

func (b *MetricsAPIBackend) GetPodMetric(ctx context.Context, namespace, name string) (PodMetric, error) {
	metric, err := b.client.MetricsV1beta1().PodMetricses(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return PodMetric{}, translateError(err)
	}
	return podMetricFromAPI(*metric), nil
}

func (b *MetricsAPIBackend) GetNodeMetric(ctx context.Context, name string) (NodeMetric, error) {
	metric, err := b.client.MetricsV1beta1().NodeMetricses().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return NodeMetric{}, translateError(err)
	}
	return nodeMetricFromAPI(*metric), nil
}

func translateError(err error) error {
	if apierrors.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

func podMetricFromAPI(metric metricsv1beta1.PodMetrics) PodMetric {
	containers := make([]ContainerMetric, 0, len(metric.Containers))
	for _, container := range metric.Containers {
		containers = append(containers, ContainerMetric{Name: container.Name, Usage: container.Usage})
	}
	return PodMetric{
		Namespace:  metric.Namespace,
		Name:       metric.Name,
		Timestamp:  metric.Timestamp.Time,
		Window:     metric.Window.Duration,
		Containers: containers,
	}
}

func nodeMetricFromAPI(metric metricsv1beta1.NodeMetrics) NodeMetric {
	return NodeMetric{
		Name:      metric.Name,
		Timestamp: metric.Timestamp.Time,
		Window:    metric.Window.Duration,
		Usage:     metric.Usage,
	}
}
