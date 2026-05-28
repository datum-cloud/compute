/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package reconcile

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// ClusterNotFoundWrapper wraps an existing [reconcile.TypedReconciler] and ignores [multicluster.ErrClusterNotFound] errors.
type ClusterNotFoundWrapper[request comparable] struct {
	wrapped reconcile.TypedReconciler[request]
}

// NewClusterNotFoundWrapper creates a new [ClusterNotFoundWrapper].
func NewClusterNotFoundWrapper[request comparable](w reconcile.TypedReconciler[request]) reconcile.TypedReconciler[request] {
	return &ClusterNotFoundWrapper[request]{wrapped: w}
}

// Reconcile implements [reconcile.TypedReconciler].
func (r *ClusterNotFoundWrapper[request]) Reconcile(ctx context.Context, req request) (reconcile.Result, error) {
	res, err := r.wrapped.Reconcile(ctx, req)

	// if the error returned by the reconciler is ErrClusterNotFound, we return without requeuing.
	if errors.Is(err, multicluster.ErrClusterNotFound) {
		return reconcile.Result{}, nil
	}

	return res, err
}

// String returns a string representation of the wrapped reconciler.
func (r *ClusterNotFoundWrapper[request]) String() string {
	return fmt.Sprintf("%v", r.wrapped)
}
