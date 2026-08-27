// SPDX-License-Identifier: AGPL-3.0-only

// Package locations reads the two location facts compute depends on: which
// cities a project may place workloads in, and which location a cell serves.
//
// Both are served today by network-services-operator and are moving to the
// locations service. Which one is read is selected per deployment by Source,
// so a control plane that has not been migrated keeps reading the types it
// already has.
package locations

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	// TopologyCityCodeKey is the topology key holding a location's city.
	TopologyCityCodeKey = "topology.datum.net/city-code"

	// ServingLocationTopologyLabel is the cluster label a cell carries to claim
	// the location it serves.
	ServingLocationTopologyLabel = "topology.datum.net/location"
)

// Source names the API group locations are read from.
type Source string

const (
	// SourceNetworkServices reads networking.datumapis.com LocationBindings and
	// ServingLocations. This is what every deployment reads today.
	SourceNetworkServices Source = "NetworkServices"

	// SourceLocations reads locations.miloapis.com Locations and
	// ServingLocations, served by the locations service.
	SourceLocations Source = "Locations"
)

var (
	locationGVK = schema.GroupVersionKind{
		Group:   "locations.miloapis.com",
		Version: "v1alpha1",
		Kind:    "Location",
	}

	servingLocationGVK = schema.GroupVersionKind{
		Group:   "locations.miloapis.com",
		Version: "v1alpha1",
		Kind:    "ServingLocation",
	}
)

// Resolve reports which source to read. An unset source reads network
// services, matching the config default.
func (s Source) Resolve() (Source, error) {
	switch s {
	case "", SourceNetworkServices:
		return SourceNetworkServices, nil
	case SourceLocations:
		return SourceLocations, nil
	default:
		return "", fmt.Errorf("unknown location source %q, want %q or %q", s, SourceNetworkServices, SourceLocations)
	}
}

// PlacementLocation is a location a project may place workloads at.
type PlacementLocation struct {
	Name     string
	Topology map[string]string
}

// CityCode returns the city the location serves, and whether it declares one.
func (l PlacementLocation) CityCode() (string, bool) {
	code, ok := l.Topology[TopologyCityCodeKey]
	return code, ok
}

// ServingLocation is the location a cell has been told it serves.
type ServingLocation struct {
	Name     string
	Topology map[string]string
}

// CityCode returns the city the cell sits in.
func (l ServingLocation) CityCode() string {
	return l.Topology[TopologyCityCodeKey]
}

// ListPlacementLocations returns the locations a project may place workloads
// at, read from the project's control plane.
func ListPlacementLocations(ctx context.Context, c client.Client, source Source) ([]PlacementLocation, error) {
	resolved, err := source.Resolve()
	if err != nil {
		return nil, err
	}

	if resolved == SourceNetworkServices {
		var bindings networkingv1alpha.LocationBindingList
		if err := c.List(ctx, &bindings); err != nil {
			return nil, fmt.Errorf("failed to list location bindings: %w", err)
		}

		locations := make([]PlacementLocation, 0, len(bindings.Items))
		for _, binding := range bindings.Items {
			locations = append(locations, PlacementLocation{
				Name:     binding.Name,
				Topology: binding.Spec.Topology,
			})
		}
		return locations, nil
	}

	items, err := listUnstructured(ctx, c, locationGVK)
	if err != nil {
		return nil, fmt.Errorf("failed to list locations: %w", err)
	}

	locations := make([]PlacementLocation, 0, len(items))
	for _, item := range items {
		topology, err := topologyOf(item)
		if err != nil {
			return nil, err
		}
		locations = append(locations, PlacementLocation{
			Name:     item.GetName(),
			Topology: topology,
		})
	}
	return locations, nil
}

// ListServingLocations returns the locations delivered to a cell.
func ListServingLocations(ctx context.Context, c client.Client, source Source) ([]ServingLocation, error) {
	resolved, err := source.Resolve()
	if err != nil {
		return nil, err
	}

	if resolved == SourceNetworkServices {
		var list networkingv1alpha.ServingLocationList
		if err := c.List(ctx, &list); err != nil {
			return nil, fmt.Errorf("failed to list serving locations: %w", err)
		}

		locations := make([]ServingLocation, 0, len(list.Items))
		for _, item := range list.Items {
			locations = append(locations, ServingLocation{
				Name:     item.Name,
				Topology: item.Spec.Topology,
			})
		}
		return locations, nil
	}

	items, err := listUnstructured(ctx, c, servingLocationGVK)
	if err != nil {
		return nil, fmt.Errorf("failed to list serving locations: %w", err)
	}

	locations := make([]ServingLocation, 0, len(items))
	for _, item := range items {
		topology, err := topologyOf(item)
		if err != nil {
			return nil, err
		}
		locations = append(locations, ServingLocation{
			Name:     item.GetName(),
			Topology: topology,
		})
	}
	return locations, nil
}

// ServingLocationObject returns the object a controller watches to learn that
// a cell has been told where it sits.
func ServingLocationObject(source Source) (client.Object, error) {
	resolved, err := source.Resolve()
	if err != nil {
		return nil, err
	}

	if resolved == SourceNetworkServices {
		return &networkingv1alpha.ServingLocation{}, nil
	}

	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(servingLocationGVK)
	return object, nil
}

// CityCodes returns the cities the given locations serve.
func CityCodes(locations []PlacementLocation) sets.Set[string] {
	codes := sets.Set[string]{}
	for _, location := range locations {
		if code, ok := location.CityCode(); ok {
			codes.Insert(code)
		}
	}
	return codes
}

// listUnstructured lists a kind that may not be installed. A control plane is
// only expected to serve the kinds its consumers read, so a kind that is not
// there reads as empty rather than failing the caller.
func listUnstructured(ctx context.Context, c client.Client, gvk schema.GroupVersionKind) ([]unstructured.Unstructured, error) {
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))

	if err := c.List(ctx, &list); err != nil {
		if apimeta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	return list.Items, nil
}

func topologyOf(object unstructured.Unstructured) (map[string]string, error) {
	topology, _, err := unstructured.NestedStringMap(object.Object, "spec", "topology")
	if err != nil {
		return nil, fmt.Errorf("failed to read the topology of location %q: %w", object.GetName(), err)
	}
	return topology, nil
}
