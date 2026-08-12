// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"errors"
	"sync"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// hubOwnerScheme carries the compute kinds needed to resolve a hub
// WorkloadDeployment's GroupVersionKind when an owner reference is built outside
// a running manager (unit tests, and any caller constructed without a scheme).
var hubOwnerScheme = sync.OnceValue(func() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(computev1alpha.AddToScheme(s))
	return s
})

// federationScheme returns the scheme to use when writing owner references onto
// federation-hub objects, falling back to a compute-only scheme so ownership
// does not depend on a manager having been wired first.
func federationScheme(s *runtime.Scheme) *runtime.Scheme {
	if s != nil {
		return s
	}
	return hubOwnerScheme()
}

// reconcileHubOwnerReference makes owner the controller of obj, reporting
// whether the object's owner references changed. Both objects live on the
// federation hub, so this is an ordinary in-cluster controller reference.
//
// An object already controlled by something else is left untouched: adoption
// exists to stamp ownership onto copies that have none, not to take an object
// away from another controller.
func reconcileHubOwnerReference(
	owner *computev1alpha.WorkloadDeployment,
	obj client.Object,
	scheme *runtime.Scheme,
) (bool, error) {
	before := obj.GetOwnerReferences()
	existing := make([]metav1.OwnerReference, len(before))
	copy(existing, before)

	if err := controllerutil.SetControllerReference(owner, obj, scheme); err != nil {
		alreadyOwned := &controllerutil.AlreadyOwnedError{}
		if errors.As(err, &alreadyOwned) {
			return false, nil
		}
		return false, err
	}

	return !apiequality.Semantic.DeepEqual(existing, obj.GetOwnerReferences()), nil
}
