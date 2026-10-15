// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

// CityCodeSelector returns the location selector that places at every
// location in the given cities: an equality for one city, an In expression
// for several. It is what the deprecated cityCodes field and the CLI's --city
// flag both stand for, so a placement written either way resolves the same.
func CityCodeSelector(cityCodes []string) *metav1.LabelSelector {
	if len(cityCodes) == 1 {
		return &metav1.LabelSelector{
			MatchLabels: map[string]string{locationsv1alpha1.TopologyCityCodeKey: cityCodes[0]},
		}
	}
	return &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      locationsv1alpha1.TopologyCityCodeKey,
			Operator: metav1.LabelSelectorOpIn,
			Values:   append([]string(nil), cityCodes...),
		}},
	}
}

// MigrateCityCodes rewrites a placement that still names city codes into the
// equivalent locationSelector and clears the deprecated field. It reports
// whether anything changed. A placement that already names locations or a
// selector is left alone, including one that also carries city codes, which
// validation rejects rather than guessing which the author meant.
func (p *WorkloadPlacement) MigrateCityCodes() bool {
	if len(p.CityCodes) == 0 || len(p.Locations) > 0 || p.LocationSelector != nil {
		return false
	}
	p.LocationSelector = CityCodeSelector(p.CityCodes)
	p.CityCodes = nil
	return true
}

// MigrateCityCodes rewrites every placement that still names city codes. It
// reports whether the spec changed, so a caller that persisted the workload
// knows to write it back.
func (w *Workload) MigrateCityCodes() bool {
	migrated := false
	for i := range w.Spec.Placements {
		if w.Spec.Placements[i].MigrateCityCodes() {
			migrated = true
		}
	}
	return migrated
}
