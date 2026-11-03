package util

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

const (
	completionTestRegionKey = "topology.datum.net/region"
	completionTestCityDFW   = "DFW"
	completionTestDFWA      = "dfw-a"
	completionTestDFWB      = "dfw-b"
	completionTestORD       = "ord"
)

func completionTestLocation(name, city, region string, ready bool) locationsv1alpha1.Location {
	location := locationsv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: locationsv1alpha1.LocationSpec{
			Topology: map[string]string{
				locationsv1alpha1.TopologyCityCodeKey: city,
				completionTestRegionKey:               region,
			},
		},
	}
	if ready {
		location.Status.Conditions = []metav1.Condition{{
			Type: locationsv1alpha1.LocationConditionReady, Status: metav1.ConditionTrue,
		}}
	}
	return location
}

func completionTestList() locationsv1alpha1.LocationList {
	return locationsv1alpha1.LocationList{Items: []locationsv1alpha1.Location{
		completionTestLocation(completionTestORD, "ORD", "us-central", true),
		completionTestLocation(completionTestDFWA, completionTestCityDFW, "us-south", true),
		completionTestLocation(completionTestDFWB, completionTestCityDFW, "us-south", true),
		completionTestLocation("lhr", "LHR", "eu-west", false),
	}}
}

// readyOnly is the filter a placement is held to when no availability gate is
// enforced; onlyDFWA is the same with compute available in one location.
var (
	readyOnly placeableFilter = locationIsReady
	onlyDFWA  placeableFilter = func(location locationsv1alpha1.Location) bool {
		return locationIsReady(location) && location.Name == completionTestDFWA
	}
)

func TestLocationCandidates(t *testing.T) {
	t.Parallel()
	list := completionTestList()
	assert.Equal(t, []string{completionTestDFWA, completionTestDFWB, "lhr", completionTestORD}, locationCandidates(list, nil),
		"list filters may name a location that is not Ready")
	assert.Equal(t, []string{completionTestDFWA, completionTestDFWB, completionTestORD}, locationCandidates(list, readyOnly),
		"a placement may only name a Ready location")
	assert.Equal(t, []string{completionTestDFWA}, locationCandidates(list, onlyDFWA),
		"the availability gate narrows what deploy offers")
}

func TestCityCodeCandidates(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{completionTestCityDFW, "ORD"}, cityCodeCandidates(completionTestList(), readyOnly),
		"city codes are deduplicated and only Ready locations count")
}

func TestSelectorCandidates(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{
		locationsv1alpha1.TopologyCityCodeKey + "=" + completionTestCityDFW,
		locationsv1alpha1.TopologyCityCodeKey + "=ORD",
		completionTestRegionKey + "=us-central",
		completionTestRegionKey + "=us-south",
	}, selectorCandidates(completionTestList(), readyOnly),
		"every topology key=value pair of a Ready location is offered once")
}

func TestCompleteCommaList(t *testing.T) {
	t.Parallel()
	candidates := []string{completionTestDFWA, completionTestDFWB, completionTestORD}

	tests := []struct {
		name       string
		toComplete string
		want       []string
	}{
		{"first element", "", []string{completionTestDFWA, completionTestDFWB, completionTestORD}},
		{"first element partial", "df", []string{completionTestDFWA, completionTestDFWB, completionTestORD}},
		{"second element keeps the first", completionTestDFWA + ",", []string{completionTestDFWA + "," + completionTestDFWB, completionTestDFWA + "," + completionTestORD}},
		{"second element partial", completionTestDFWA + ",o", []string{completionTestDFWA + "," + completionTestDFWB, completionTestDFWA + "," + completionTestORD}},
		{"third element skips both chosen", completionTestDFWA + "," + completionTestORD + ",", []string{completionTestDFWA + "," + completionTestORD + "," + completionTestDFWB}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, directive := completeCommaList(candidates, tt.toComplete)
			assert.Equal(t, tt.want, got, "the shell filters by prefix, so candidates carry the typed elements")
			assert.Equal(t, cobra.ShellCompDirectiveNoFileComp|cobra.ShellCompDirectiveNoSpace, directive)
		})
	}
}
