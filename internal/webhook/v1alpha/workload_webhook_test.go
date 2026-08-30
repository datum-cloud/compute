// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	"go.datum.net/compute/pkg/runtimeclass"
)

func runtimeClass(name string, isDefault bool) computev1alpha.RuntimeClass {
	return computev1alpha.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       computev1alpha.RuntimeClassSpec{Default: isDefault},
	}
}

// TestWorkloadWebhookDefaultGateOff pins the disabled behavior: the field is
// left exactly as the customer wrote it, and the catalog is never read — the
// webhook has no manager here, so any attempt to read it would panic.
func TestWorkloadWebhookDefaultGateOff(t *testing.T) {
	cases := map[string]struct {
		class string
	}{
		"unset stays unset":              {},
		"an explicit class is untouched": {class: computev1alpha.RuntimeClassUnikernel},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, false)

			workload := &computev1alpha.Workload{}
			workload.Spec.Template.Spec.Runtime.Class = tc.class

			if err := (&workloadWebhook{}).Default(context.Background(), workload); err != nil {
				t.Fatalf("Default: %v", err)
			}

			if got := workload.Spec.Template.Spec.Runtime.Class; got != tc.class {
				t.Errorf("runtime class = %q, want %q", got, tc.class)
			}
		})
	}
}

// TestDefaultRuntimeClass covers which tier a workload that selected none is
// recorded as running in. The answer comes from the catalog, so publishing a
// different default is a catalog change; the compiled-in class is only the
// floor beneath a catalog that states nothing.
func TestDefaultRuntimeClass(t *testing.T) {
	cases := map[string]struct {
		class   string
		catalog runtimeclass.Catalog
		want    string
	}{
		"the class the catalog marks default is stamped": {
			catalog: runtimeclass.Catalog{
				runtimeClass(computev1alpha.RuntimeClassUnikernel, false),
				runtimeClass(computev1alpha.RuntimeClassGeneralPurpose, true),
			},
			want: computev1alpha.RuntimeClassGeneralPurpose,
		},
		"an explicit selection is never overwritten": {
			class: computev1alpha.RuntimeClassGeneralPurpose,
			catalog: runtimeclass.Catalog{
				runtimeClass(computev1alpha.RuntimeClassUnikernel, true),
			},
			want: computev1alpha.RuntimeClassGeneralPurpose,
		},
		"a catalog marking no default falls back to the tier served today": {
			catalog: runtimeclass.Catalog{
				runtimeClass(computev1alpha.RuntimeClassUnikernel, false),
				runtimeClass(computev1alpha.RuntimeClassGeneralPurpose, false),
			},
			want: computev1alpha.DefaultRuntimeClass,
		},
		"an ambiguous default is not guessed at": {
			catalog: runtimeclass.Catalog{
				runtimeClass("wasm", true),
				runtimeClass(computev1alpha.RuntimeClassGeneralPurpose, true),
			},
			want: "",
		},
		"a catalog with nothing to offer stamps nothing": {
			want: "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			workload := &computev1alpha.Workload{}
			workload.Spec.Template.Spec.Runtime.Class = tc.class

			defaultRuntimeClass(workload, tc.catalog)

			if got := workload.Spec.Template.Spec.Runtime.Class; got != tc.want {
				t.Errorf("runtime class = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRuntimeClassCatalogUnreachable pins the safe direction when the catalog
// cannot be read: the request fails. Admitting a workload while the platform
// cannot say which tiers exist would store a selection nothing has agreed to
// run, and the customer would learn only from a workload that is never placed.
func TestRuntimeClassCatalogUnreachable(t *testing.T) {
	scheme := k8sruntime.NewScheme()
	if err := computev1alpha.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	unreachable := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return errors.New("catalog unavailable")
			},
		}).
		Build()

	if _, err := runtimeClassCatalog(context.Background(), unreachable); err == nil {
		t.Fatal("expected an unreadable catalog to fail the request")
	}

	empty := fake.NewClientBuilder().WithScheme(scheme).Build()
	catalog, err := runtimeClassCatalog(context.Background(), empty)
	if err != nil {
		t.Fatalf("reading an empty catalog: %v", err)
	}
	if len(catalog) != 0 {
		t.Errorf("catalog = %v, want nothing published", catalog.Names())
	}
}
