package util

import (
	"context"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/locations"
	"go.datum.net/datumctl/plugin"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

// CompleteInstanceNames is a ValidArgsFunction that lists instance names from the API.
func CompleteInstanceNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	project := ProjectFromCmd(cmd)
	c, err := NewClient(project)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var list computev1alpha.InstanceList
	if err := c.List(context.Background(), &list, client.InNamespace(ResourceNamespace)); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, len(list.Items))
	for i, inst := range list.Items {
		names[i] = inst.Name
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// CompleteLocations completes a --location flag with every location projected
// into the project, whether or not it is Ready. List and describe commands
// filter by location, and a location that is no longer Ready may still have
// deployments worth finding.
func CompleteLocations(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	list, ok := projectedLocations(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeCommaList(locationCandidates(list, nil), toComplete)
}

// CompletePlacementLocations completes a deploy-time --location flag with the
// locations a placement may name: those projected into the project that are
// Ready and where compute is available. Admission rejects any other, so they
// are not offered.
func CompletePlacementLocations(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	list, ok := projectedLocations(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeCommaList(locationCandidates(list, placeable(cmd)), toComplete)
}

// CompleteCityCodes completes --city with the city codes of the locations a
// placement may run at.
func CompleteCityCodes(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	list, ok := projectedLocations(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeCommaList(cityCodeCandidates(list, placeable(cmd)), toComplete)
}

// CompleteLocationSelector completes --location-selector with the key=value
// pairs found in the topology of the locations a placement may run at, so a
// user can discover which topology keys exist without reading each Location.
func CompleteLocationSelector(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	list, ok := projectedLocations(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeCommaList(selectorCandidates(list, placeable(cmd)), toComplete)
}

// placeableFilter decides which projected locations a placement may run at.
// A nil filter accepts every location.
type placeableFilter func(locationsv1alpha1.Location) bool

// placeable returns the filter a placement is held to: the location is Ready
// and compute is available there. Availability is read from the
// ServiceAvailability records the platform mirrors into the project; a project
// that does not serve them enforces no availability gate.
func placeable(cmd *cobra.Command) placeableFilter {
	available, enforced := availableLocations(cmd)
	return func(location locationsv1alpha1.Location) bool {
		return locationIsReady(location) && (!enforced || available.Has(location.Name))
	}
}

// locationCandidates returns the names of the locations the filter accepts,
// sorted.
func locationCandidates(list locationsv1alpha1.LocationList, accept placeableFilter) []string {
	names := make([]string, 0, len(list.Items))
	for _, location := range list.Items {
		if accept != nil && !accept(location) {
			continue
		}
		names = append(names, location.Name)
	}
	sort.Strings(names)
	return names
}

// cityCodeCandidates returns the distinct city codes of the locations the
// filter accepts, sorted.
func cityCodeCandidates(list locationsv1alpha1.LocationList, accept placeableFilter) []string {
	codes := sets.New[string]()
	for _, location := range list.Items {
		if accept != nil && !accept(location) {
			continue
		}
		if code := location.Spec.Topology[locationsv1alpha1.TopologyCityCodeKey]; code != "" {
			codes.Insert(code)
		}
	}
	return sets.List(codes)
}

// selectorCandidates returns every distinct key=value pair in the topology of
// the locations the filter accepts, sorted, which is what a selector on those
// locations can match.
func selectorCandidates(list locationsv1alpha1.LocationList, accept placeableFilter) []string {
	pairs := sets.New[string]()
	for _, location := range list.Items {
		if accept != nil && !accept(location) {
			continue
		}
		for key, value := range location.Spec.Topology {
			pairs.Insert(key + "=" + value)
		}
	}
	return sets.List(pairs)
}

func locationIsReady(location locationsv1alpha1.Location) bool {
	return apimeta.IsStatusConditionTrue(location.Status.Conditions, locationsv1alpha1.LocationConditionReady)
}

// completeCommaList completes the last element of a comma-separated flag
// value. The shell matches candidates against the whole value typed so far,
// so each candidate is returned with the already-typed elements in front of
// it; elements already present are not offered again. No space is appended,
// so the user can keep typing a comma for the next element.
func completeCommaList(candidates []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	prefix := ""
	chosen := sets.New[string]()
	if i := strings.LastIndex(toComplete, ","); i >= 0 {
		prefix = toComplete[:i+1]
		for _, element := range strings.Split(toComplete[:i], ",") {
			if element != "" {
				chosen.Insert(element)
			}
		}
	}

	completions := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if chosen.Has(candidate) {
			continue
		}
		completions = append(completions, prefix+candidate)
	}
	return completions, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// availableLocations returns the locations where compute is available, and
// whether the project serves availability at all. On any failure it reports
// the gate as not enforced, so completion degrades to Ready locations rather
// than offering nothing.
func availableLocations(cmd *cobra.Command) (sets.Set[string], bool) {
	c, err := NewClient(ProjectFromCmd(cmd))
	if err != nil {
		return nil, false
	}
	available, enforced, err := locations.AvailableLocations(context.Background(), c)
	if err != nil {
		return nil, false
	}
	return available, enforced
}

func projectedLocations(cmd *cobra.Command) (locationsv1alpha1.LocationList, bool) {
	project := ProjectFromCmd(cmd)
	c, err := NewClient(project)
	if err != nil {
		return locationsv1alpha1.LocationList{}, false
	}
	var list locationsv1alpha1.LocationList
	if err := c.List(context.Background(), &list); err != nil {
		return locationsv1alpha1.LocationList{}, false
	}
	return list, true
}

// CompleteOutputFormats returns a ValidArgsFunction that completes -o/--output
// to the given allowed values.
func CompleteOutputFormats(allowed ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return allowed, cobra.ShellCompDirectiveNoFileComp
	}
}

// CompleteWorkloadNames is a ValidArgsFunction that lists workload names from
// the API. It suppresses file completion in all cases so the shell never falls
// back to filename completion when completing a workload-name argument.
func CompleteWorkloadNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	project := ProjectFromCmd(cmd)
	c, err := NewClient(project)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var list computev1alpha.WorkloadList
	if err := c.List(context.Background(), &list, client.InNamespace(ResourceNamespace)); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]string, len(list.Items))
	for i, w := range list.Items {
		names[i] = w.Name
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// CompleteWorkloadNamesAndFlags lists workload names from the API and also
// surfaces the command's own flags as completions. Used by commands where flags
// are the primary input (e.g. deploy) so that plain <TAB> offers flags without
// requiring the user to type "--" first.
var CompleteWorkloadNamesAndFlags = plugin.WithFlagCompletion(CompleteWorkloadNames)
