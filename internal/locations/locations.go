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
	"errors"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

// ComputeServiceName is the name the platform records compute availability
// under: a ServiceAvailability whose spec.serviceRef.name is this value says
// compute is deployed and validated at spec.locationRef.name.
const ComputeServiceName = "compute"

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

	// Ready reports whether the location itself is serving. A Location read
	// from the locations service is Ready when its Ready condition is true. A
	// LocationBinding carries no readiness contract that compute reads, so
	// every binding is Ready.
	Ready bool

	// ServiceAvailable reports whether compute is deployed and validated at
	// the location, read from the ServiceAvailability the platform mirrors
	// into the project. A location can be Ready for the platform generally
	// yet have no compute cell behind it; this is what tells the two apart.
	// A control plane that does not serve the kind enforces no such gate, so
	// every location there is available.
	ServiceAvailable bool
}

// Placeable reports whether a placement may run at the location: it is Ready
// and compute is available there.
func (l PlacementLocation) Placeable() bool {
	return l.Ready && l.ServiceAvailable
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
				Ready:    true,
			})
		}
		return markServiceAvailability(ctx, c, found)
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
			Ready:    apimeta.IsStatusConditionTrue(location.Status.Conditions, locationsv1alpha1.LocationConditionReady),
		})
	}
	return markServiceAvailability(ctx, c, found)
}

// markServiceAvailability sets ServiceAvailable on each location from the
// ServiceAvailability records the platform mirrors into the project.
//
// A location is available when a record for the compute service names it and
// reports Available. A control plane that does not serve the kind has no
// availability to consult, so every location is marked available and the
// Ready gate alone decides, which is what placement did before the platform
// began mirroring availability.
func markServiceAvailability(ctx context.Context, c client.Client, found []PlacementLocation) ([]PlacementLocation, error) {
	available, enforced, err := AvailableLocations(ctx, c)
	if err != nil {
		return nil, err
	}
	for i := range found {
		found[i].ServiceAvailable = !enforced || available.Has(found[i].Name)
	}
	return found, nil
}

// AvailableLocations returns the names of the locations where compute is
// available, and whether the control plane serves availability at all. When
// it does not, enforced is false and the set is empty.
func AvailableLocations(ctx context.Context, c client.Client) (available sets.Set[string], enforced bool, err error) {
	var list servicesv1alpha1.ServiceAvailabilityList
	if err := c.List(ctx, &list); err != nil {
		if kindNotInstalled(err) {
			return sets.Set[string]{}, false, nil
		}
		return nil, false, fmt.Errorf("failed to list service availabilities: %w", err)
	}

	available = sets.Set[string]{}
	for _, availability := range list.Items {
		if availability.Spec.ServiceRef.Name != ComputeServiceName {
			continue
		}
		if apimeta.IsStatusConditionTrue(availability.Status.Conditions, "Available") {
			available.Insert(availability.Spec.LocationRef.Name)
		}
	}
	return available, true, nil
}

// ServiceAvailabilityGVK returns the kind a controller watches to learn that
// compute availability at a location changed.
func ServiceAvailabilityGVK() schema.GroupVersionKind {
	return servicesv1alpha1.GroupVersion.WithKind("ServiceAvailability")
}

// ServesServiceAvailabilityKind reports whether the control plane serves
// ServiceAvailability, so a watch on it is safe to register.
func ServesServiceAvailabilityKind(mapper apimeta.RESTMapper) (bool, error) {
	return servesKind(mapper, ServiceAvailabilityGVK())
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

// Select returns the Ready locations whose topology matches the selector,
// sorted by name so callers derive a stable set of deployments from it.
//
// An empty selector is an error rather than a match for every location. The
// webhook rejects one, so reaching this with one means the stored object was
// not admitted through it.
func Select(found []PlacementLocation, selector *metav1.LabelSelector) ([]PlacementLocation, error) {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return nil, errors.New("location selector is empty")
	}

	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("invalid location selector: %w", err)
	}

	var matched []PlacementLocation
	for _, location := range found {
		if location.Placeable() && sel.Matches(labels.Set(location.Topology)) {
			matched = append(matched, location)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
	return matched, nil
}

// PlacementLocationObject returns the object a controller watches to learn
// that the locations a project may place workloads at have changed.
func PlacementLocationObject(source Source) (client.Object, error) {
	resolved, err := source.Resolve()
	if err != nil {
		return nil, err
	}

	if resolved == SourceNetworkServices {
		return &networkingv1alpha.LocationBinding{}, nil
	}
	return &locationsv1alpha1.Location{}, nil
}

// PlacementLocationGVK returns the kind a controller watches to learn that the
// locations a project may place workloads at have changed.
func PlacementLocationGVK(source Source) (schema.GroupVersionKind, error) {
	resolved, err := source.Resolve()
	if err != nil {
		return schema.GroupVersionKind{}, err
	}

	if resolved == SourceNetworkServices {
		return networkingv1alpha.GroupVersion.WithKind("LocationBinding"), nil
	}
	return locationsv1alpha1.GroupVersion.WithKind("Location"), nil
}

// ServesPlacementLocationKind reports whether the control plane behind the
// mapper serves the kind the source watches for placement locations.
//
// Unlike EnsureServingLocationKind this answers rather than refuses. A cell
// serves the deployments it is asked about, so a missing serving location kind
// there is a misconfiguration worth failing on. Placement locations are read
// from many project control planes engaged one at a time, and a control plane
// that does not carry the kind is skipped rather than taking the whole manager
// down with it.
func ServesPlacementLocationKind(mapper apimeta.RESTMapper, source Source) (bool, error) {
	gvk, err := PlacementLocationGVK(source)
	if err != nil {
		return false, err
	}
	return servesKind(mapper, gvk)
}

// servesKind reports whether the control plane behind the mapper serves the
// kind. A kind that is not installed reads as not served; any other mapper
// failure is returned, since it says nothing about the kind.
func servesKind(mapper apimeta.RESTMapper, gvk schema.GroupVersionKind) (bool, error) {
	if _, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
		if kindNotInstalled(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to determine whether %s is served: %w", gvk, err)
	}
	return true, nil
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

// ServingLocationGVK returns the kind a controller watches for the source.
func ServingLocationGVK(source Source) (schema.GroupVersionKind, error) {
	resolved, err := source.Resolve()
	if err != nil {
		return schema.GroupVersionKind{}, err
	}

	if resolved == SourceNetworkServices {
		return networkingv1alpha.GroupVersion.WithKind("ServingLocation"), nil
	}
	return locationsv1alpha1.GroupVersion.WithKind("ServingLocation"), nil
}

// EnsureServingLocationKind fails unless the control plane serves the kind the
// source watches.
//
// A watch is not a read: a list against a kind the control plane does not serve
// degrades to no locations, but a watch cannot, and registering one wedges the
// manager during cache sync. Refusing to start says which CRD is missing, where
// a wedged manager says nothing.
//
// This gates only the selected source. A deployment reading network services
// must not be made to depend on the locations service being installed.
func EnsureServingLocationKind(mapper apimeta.RESTMapper, source Source) error {
	gvk, err := ServingLocationGVK(source)
	if err != nil {
		return err
	}

	if _, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
		if kindNotInstalled(err) {
			resolved, _ := source.Resolve()
			return fmt.Errorf(
				"locationSource %q watches %s, which this control plane does not serve: "+
					"install the %s CustomResourceDefinition, or set locationSource to %q",
				resolved, gvk, crdName(gvk), otherSource(resolved))
		}
		return fmt.Errorf("failed to determine whether %s is served: %w", gvk, err)
	}

	return nil
}

func crdName(gvk schema.GroupVersionKind) string {
	return fmt.Sprintf("%ss.%s", strings.ToLower(gvk.Kind), gvk.Group)
}

func otherSource(source Source) Source {
	if source == SourceLocations {
		return SourceNetworkServices
	}
	return SourceLocations
}

// PlaceableNames returns the names of the given locations a placement may run
// at: those that are Ready and where compute is available.
func PlaceableNames(found []PlacementLocation) sets.Set[string] {
	names := sets.Set[string]{}
	for _, location := range found {
		if location.Placeable() {
			names.Insert(location.Name)
		}
	}
	return names
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
