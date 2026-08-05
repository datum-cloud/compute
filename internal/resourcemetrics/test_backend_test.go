// SPDX-License-Identifier: AGPL-3.0-only

package resourcemetrics

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type testBackend struct {
	pods  []PodMetric
	nodes []NodeMetric
}

func (b testBackend) GetPodMetric(_ context.Context, namespace, name string) (PodMetric, error) {
	for _, metric := range b.pods {
		if metric.Namespace == namespace && metric.Name == name {
			return metric, nil
		}
	}
	return PodMetric{}, ErrNotFound
}

func (b testBackend) GetNodeMetric(_ context.Context, name string) (NodeMetric, error) {
	for _, metric := range b.nodes {
		if metric.Name == name {
			return metric, nil
		}
	}
	return NodeMetric{}, ErrNotFound
}

func resourceList(cpu, memory string) corev1.ResourceList {
	usage := corev1.ResourceList{}
	if cpu != "" {
		usage[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if memory != "" {
		usage[corev1.ResourceMemory] = resource.MustParse(memory)
	}
	return usage
}
