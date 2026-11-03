// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

const (
	placementTestCityDFW = "DFW"
	placementTestCityIAD = "IAD"
)

func TestCityCodeSelector(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &metav1.LabelSelector{
		MatchLabels: map[string]string{locationsv1alpha1.TopologyCityCodeKey: placementTestCityDFW},
	}, CityCodeSelector([]string{placementTestCityDFW}), "one city is a plain equality")

	assert.Equal(t, &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      locationsv1alpha1.TopologyCityCodeKey,
			Operator: metav1.LabelSelectorOpIn,
			Values:   []string{placementTestCityDFW, placementTestCityIAD},
		}},
	}, CityCodeSelector([]string{placementTestCityDFW, placementTestCityIAD}), "several cities are an In expression")
}

// TestMigrateCityCodes covers the shim for workloads stored before placement
// moved to locations: a placement that only names city codes becomes the
// equivalent selector, and anything else is left for validation to judge.
func TestMigrateCityCodes(t *testing.T) {
	t.Parallel()

	t.Run("city codes alone are rewritten", func(t *testing.T) {
		t.Parallel()
		w := &Workload{Spec: WorkloadSpec{Placements: []WorkloadPlacement{
			{Name: "a", CityCodes: []string{placementTestCityDFW, placementTestCityIAD}},
			{Name: "b", Locations: []locationsv1alpha1.LocationReference{{Name: "us-east-1"}}},
		}}}

		require.True(t, w.MigrateCityCodes())
		assert.Nil(t, w.Spec.Placements[0].CityCodes, "the deprecated field is cleared")
		assert.Equal(t, CityCodeSelector([]string{placementTestCityDFW, placementTestCityIAD}), w.Spec.Placements[0].LocationSelector)
		assert.Empty(t, w.Spec.Placements[1].LocationSelector, "a placement naming locations is untouched")
		assert.False(t, w.MigrateCityCodes(), "a second pass finds nothing to do")
	})

	t.Run("city codes beside locations or a selector are left for validation", func(t *testing.T) {
		t.Parallel()
		w := &Workload{Spec: WorkloadSpec{Placements: []WorkloadPlacement{
			{Name: "a", CityCodes: []string{placementTestCityDFW}, Locations: []locationsv1alpha1.LocationReference{{Name: "us-east-1"}}},
			{Name: "b", CityCodes: []string{placementTestCityDFW}, LocationSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"k": "v"}}},
		}}}

		assert.False(t, w.MigrateCityCodes())
		assert.Equal(t, []string{placementTestCityDFW}, w.Spec.Placements[0].CityCodes)
		assert.Equal(t, []string{placementTestCityDFW}, w.Spec.Placements[1].CityCodes)
	})
}
