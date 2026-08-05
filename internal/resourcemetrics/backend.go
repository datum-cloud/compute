// SPDX-License-Identifier: AGPL-3.0-only

package resourcemetrics

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
)

var ErrNotFound = errors.New("metric not found")

type Backend interface {
	GetPodMetric(ctx context.Context, namespace, name string) (PodMetric, error)
	GetNodeMetric(ctx context.Context, name string) (NodeMetric, error)
}

type PodMetric struct {
	Namespace  string
	Name       string
	Timestamp  time.Time
	Window     time.Duration
	Containers []ContainerMetric
}

type ContainerMetric struct {
	Name  string
	Usage corev1.ResourceList
}

type NodeMetric struct {
	Name      string
	Timestamp time.Time
	Window    time.Duration
	Usage     corev1.ResourceList
}

type EmptyBackend struct{}

func (EmptyBackend) GetPodMetric(_ context.Context, _, _ string) (PodMetric, error) {
	return PodMetric{}, ErrNotFound
}

func (EmptyBackend) GetNodeMetric(_ context.Context, _ string) (NodeMetric, error) {
	return NodeMetric{}, ErrNotFound
}
