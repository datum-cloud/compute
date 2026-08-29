// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"testing"

	featuregatetesting "k8s.io/component-base/featuregate/testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
)

// TestWorkloadWebhook_DefaultRuntimeClass covers stamping the effective
// execution tier onto the stored spec. The tier is only recorded while the
// feature is enabled, and a customer's explicit selection is never overwritten.
func TestWorkloadWebhook_DefaultRuntimeClass(t *testing.T) {
	cases := map[string]struct {
		gateEnabled bool
		class       string
		want        string
	}{
		"gate off leaves the field absent": {},
		"gate off does not touch an explicit class": {
			class: computev1alpha.RuntimeClassUnikernel,
			want:  computev1alpha.RuntimeClassUnikernel,
		},
		"gate on stamps the default": {
			gateEnabled: true,
			want:        computev1alpha.DefaultRuntimeClass,
		},
		"gate on preserves an explicit non-default class": {
			gateEnabled: true,
			class:       computev1alpha.RuntimeClassGeneralPurpose,
			want:        computev1alpha.RuntimeClassGeneralPurpose,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, tc.gateEnabled)

			workload := &computev1alpha.Workload{}
			workload.Spec.Template.Spec.Runtime.Class = tc.class

			if err := (&workloadWebhook{}).Default(context.Background(), workload); err != nil {
				t.Fatalf("Default: %v", err)
			}

			if got := workload.Spec.Template.Spec.Runtime.Class; got != tc.want {
				t.Errorf("runtime class = %q, want %q", got, tc.want)
			}
		})
	}
}
