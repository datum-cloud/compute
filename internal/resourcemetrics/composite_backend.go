// SPDX-License-Identifier: AGPL-3.0-only

package resourcemetrics

import (
	"context"
	"errors"
	"slices"
)

type CompositeBackend struct {
	backends []Backend
}

func NewCompositeBackend(backends ...Backend) *CompositeBackend {
	return &CompositeBackend{backends: backends}
}

func (b *CompositeBackend) GetPodMetric(ctx context.Context, namespace, name string) (PodMetric, error) {
	for _, backend := range slices.Backward(b.backends) {
		metric, err := backend.GetPodMetric(ctx, namespace, name)
		if err == nil {
			return metric, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return PodMetric{}, err
		}
	}
	return PodMetric{}, ErrNotFound
}

func (b *CompositeBackend) GetNodeMetric(ctx context.Context, name string) (NodeMetric, error) {
	for _, backend := range slices.Backward(b.backends) {
		metric, err := backend.GetNodeMetric(ctx, name)
		if err == nil {
			return metric, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return NodeMetric{}, err
		}
	}
	return NodeMetric{}, ErrNotFound
}
