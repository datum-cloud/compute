// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"errors"
	"fmt"

	"go.miloapis.com/milo/pkg/downstreamclient"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// errFederationNamespaceNotOwned marks a recorded hub namespace that belongs to
// another project namespace.
var errFederationNamespaceNotOwned = errors.New("hub namespace does not belong to this project namespace")

// verifyFederationNamespace checks that hubNS is the hub namespace of
// upstreamNamespace in the project clusterName. Project users can write the
// annotation that records a hub namespace, so it is trusted only once the hub
// namespace's owner labels, which only the federator writes, agree. It returns
// false with no error when the hub namespace no longer exists.
func verifyFederationNamespace(ctx context.Context, hub client.Reader, hubNS, clusterName, upstreamNamespace string) (bool, error) {
	var namespace corev1.Namespace
	if err := hub.Get(ctx, client.ObjectKey{Name: hubNS}, &namespace); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed getting hub namespace %q: %w", hubNS, err)
	}
	if namespace.Labels[downstreamclient.UpstreamOwnerClusterNameLabel] != EncodeClusterName(clusterName) ||
		namespace.Labels[downstreamclient.UpstreamOwnerNamespaceLabel] != upstreamNamespace {
		return false, fmt.Errorf("%w: %q is not the hub namespace of %s/%s", errFederationNamespaceNotOwned, hubNS, clusterName, upstreamNamespace)
	}
	return true, nil
}
