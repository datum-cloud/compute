// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	// internalDNSOriginLabel names where a DNS object was written. A cell
	// publishing locally marks its objects with its region, and the region's
	// DNS control plane sends them on to the other regions and the project.
	internalDNSOriginLabel = "internal-dns.miloapis.com/origin"
	// internalDNSOriginUIDAnnotation and internalDNSOriginGenerationAnnotation
	// are the UID and generation an object has where it was written. DNS
	// copies in a cell carry them, and references use them.
	internalDNSOriginUIDAnnotation        = "internal-dns.miloapis.com/origin-uid"
	internalDNSOriginGenerationAnnotation = "internal-dns.miloapis.com/origin-generation"
	// internalDNSProjectUIDAnnotation is the project UID DNS records on the
	// namespaces it places in a cell.
	internalDNSProjectUIDAnnotation = "internal-dns.miloapis.com/project-uid"
)

// originUIDReader presents DNS copies with their origin's UID and generation,
// so references a cell writes resolve the same way they do in the project.
type originUIDReader struct{ client.Reader }

func (r originUIDReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := r.Reader.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	withOriginUID(obj)
	return nil
}

func (r originUIDReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := r.Reader.List(ctx, list, opts...); err != nil {
		return err
	}
	return meta.EachListItem(list, func(o runtime.Object) error {
		if obj, ok := o.(client.Object); ok {
			withOriginUID(obj)
		}
		return nil
	})
}

// withOriginUID translates only the project's copies, never an object the
// cell wrote itself, so updates of the cell's own objects keep their UID.
func withOriginUID(obj client.Object) {
	if _, dns := obj.(*unstructured.Unstructured); !dns || obj.GetLabels()[internalDNSOriginLabel] != "project" {
		return
	}
	if uid := obj.GetAnnotations()[internalDNSOriginUIDAnnotation]; uid != "" {
		obj.SetUID(types.UID(uid))
	}
	if g, err := strconv.ParseInt(obj.GetAnnotations()[internalDNSOriginGenerationAnnotation], 10, 64); err == nil {
		obj.SetGeneration(g)
	}
}

// LocalInternalDNSAccess publishes into the cell's own cluster.
func LocalInternalDNSAccess(_ context.Context, _ string, providerCluster cluster.Cluster) (InternalDNSProjectAccess, error) {
	return InternalDNSProjectAccess{Reader: originUIDReader{providerCluster.GetAPIReader()}, Writer: providerCluster.GetClient()}, nil
}

// LocalInternalDNSProjectID names an instance's project by its cell
// namespace, which DNS annotated with the project's identity.
func LocalInternalDNSProjectID(_ context.Context, _ multicluster.ClusterName, instance *computev1alpha.Instance) (string, error) {
	return instance.Namespace, nil
}

// LocalInternalDNSProjectIdentity reads the project identity DNS recorded on
// a cell namespace.
func LocalInternalDNSProjectIdentity(reader client.Reader) func(context.Context, string) (InternalDNSProjectIdentity, error) {
	return func(ctx context.Context, namespace string) (InternalDNSProjectIdentity, error) {
		ns := &unstructured.Unstructured{}
		ns.SetAPIVersion("v1")
		ns.SetKind("Namespace")
		if err := reader.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
			return InternalDNSProjectIdentity{}, fmt.Errorf("read cell namespace %q: %w", namespace, err)
		}
		uid := ns.GetAnnotations()[internalDNSProjectUIDAnnotation]
		if uid == "" || !ns.GetDeletionTimestamp().IsZero() {
			return InternalDNSProjectIdentity{}, fmt.Errorf("cell namespace %q has no DNS project identity yet", namespace)
		}
		return InternalDNSProjectIdentity{ProjectName: namespace, ProjectUID: types.UID(uid)}, nil
	}
}

var networkContextGVK = schema.GroupVersionKind{Group: "networking.datumapis.com", Version: "v1alpha", Kind: "NetworkContext"}

const (
	networkNameLabel     = "networking.datumapis.com/network"
	networkUIDLabel      = "networking.datumapis.com/network-uid"
	networkLocationLabel = "networking.datumapis.com/location"
)

// cellNetworkUID resolves a network's project UID in a cell, which holds the
// network's NetworkContext for its location rather than the Network itself.
func cellNetworkUID(location string) vpcUIDFunc {
	return func(ctx context.Context, reader client.Reader, namespace, network string) (types.UID, error) {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(networkContextGVK.GroupVersion().WithKind(networkContextGVK.Kind + "List"))
		selector := client.MatchingLabels{networkNameLabel: network}
		if location != "" {
			selector[networkLocationLabel] = location
		}
		if err := reader.List(ctx, list, client.InNamespace(namespace), selector); err != nil {
			return "", fmt.Errorf("resolve VPC %s/%s in the cell: %w", namespace, network, err)
		}
		var uid string
		for _, item := range list.Items {
			if v := item.GetLabels()[networkUIDLabel]; v != "" {
				if uid != "" && uid != v {
					return "", fmt.Errorf("network contexts for %s/%s disagree on the network UID", namespace, network)
				}
				uid = v
			}
		}
		if uid == "" {
			return "", fmt.Errorf("no network context for %s/%s in this cell names its network UID", namespace, network)
		}
		return types.UID(uid), nil
	}
}

// cellPublished reports whether a cell's own contribution for the instance
// whose cell copy has sourceUID has reached the project.
func cellPublished(ctx context.Context, reader client.Reader, namespace, sourceUID string) (bool, error) {
	if sourceUID == "" {
		return false, nil
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(dnsContributionGVK.GroupVersion().WithKind(dnsContributionGVK.Kind + "List"))
	if err := reader.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{internalDNSInstanceUID: sourceUID}); err != nil {
		return false, err
	}
	for _, item := range list.Items {
		if origin := item.GetLabels()[internalDNSOriginLabel]; origin != "" && origin != "project" {
			return true, nil
		}
	}
	return false, nil
}

// lookupInstanceDNSReservation finds the shared registration the project
// authors for a context. A cell never creates it: the copy arrives from the
// project, without owner references.
func lookupInstanceDNSReservation(ctx context.Context, access InternalDNSProjectAccess, resolver *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	registration := newDNSObject(dnsRegistrationGVK, resolver.GetNamespace(), instanceDNSReservationName(resolver))
	if err := access.Reader.Get(ctx, client.ObjectKeyFromObject(registration), registration); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	if registration.GetLabels()[internalDNSManagedBy] != internalDNSManager || !registration.GetDeletionTimestamp().IsZero() {
		return nil, fmt.Errorf("registration %s/%s is not the project's instance reservation", registration.GetNamespace(), registration.GetName())
	}
	// Only a copy of the project's registration can be referenced from every
	// region; wait for one that carries the project's identity.
	annotations := registration.GetAnnotations()
	if registration.GetLabels()[internalDNSOriginLabel] != "project" || annotations[internalDNSOriginUIDAnnotation] == "" || annotations[internalDNSOriginGenerationAnnotation] == "" {
		return nil, nil
	}
	return registration, nil
}
