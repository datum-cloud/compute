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
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

const (
	// TopologyCityCodeKey is the topology key holding a location's city.
	TopologyCityCodeKey = locationsv1alpha1.TopologyCityCodeKey

	// ServingLocationTopologyLabel is the cluster label a cell carries to claim
	// the location it serves.
	ServingLocationTopologyLabel = locationsv1alpha1.ServingLocationTopologyLabel
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

		found := make([]PlacementLocation, 0, len(bindings.Items))
		for _, binding := range bindings.Items {
			found = append(found, PlacementLocation{
				Name:     binding.Name,
				Topology: binding.Spec.Topology,
			})
		}
		return found, nil
	}

	var list locationsv1alpha1.LocationList
	if err := c.List(ctx, &list); err != nil {
		if kindNotInstalled(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list locations: %w", err)
	}

	found := make([]PlacementLocation, 0, len(list.Items))
	for _, location := range list.Items {
		found = append(found, PlacementLocation{
			Name:     location.Name,
			Topology: location.Spec.Topology,
		})
	}
	return found, nil
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

		found := make([]ServingLocation, 0, len(list.Items))
		for _, servingLocation := range list.Items {
			found = append(found, ServingLocation{
				Name:     servingLocation.Name,
				Topology: servingLocation.Spec.Topology,
			})
		}
		return found, nil
	}

	var list locationsv1alpha1.ServingLocationList
	if err := c.List(ctx, &list); err != nil {
		if kindNotInstalled(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list serving locations: %w", err)
	}

	found := make([]ServingLocation, 0, len(list.Items))
	for _, servingLocation := range list.Items {
		found = append(found, ServingLocation{
			Name:     servingLocation.Name,
			Topology: servingLocation.Spec.Topology,
		})
	}
	return found, nil
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
	return &locationsv1alpha1.ServingLocation{}, nil
}

// CityCodes returns the cities the given locations serve.
func CityCodes(found []PlacementLocation) sets.Set[string] {
	codes := sets.Set[string]{}
	for _, location := range found {
		if code, ok := location.CityCode(); ok {
			codes.Insert(code)
		}
	}
	return codes
}

// kindNotInstalled reports whether a list failed because the control plane
// does not serve the kind. A control plane is only expected to serve the kinds
// its consumers read, so a kind that is not there reads as empty rather than
// failing the caller.
//
// The REST mapper answers first. A typed client reaches it through discovery,
// so the no-match arrives wrapped in an ErrResourceDiscoveryFailed rather than
// bare, and only errors.Is unwrapping finds it. A mapper still holding the kind
// from before the CRD went away leaves the API server to answer, with a 404. A
// type missing from the scheme is neither of these: it is a wiring mistake, and
// must keep surfacing as one.
func kindNotInstalled(err error) bool {
	return apimeta.IsNoMatchError(err) || apierrors.IsNotFound(err)
}
