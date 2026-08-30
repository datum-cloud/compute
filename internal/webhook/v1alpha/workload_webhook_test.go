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

// These class names are invented rather than the ones the platform ships. A
// test using the shipped names could not distinguish a catalog lookup from a
// compiled-in fallback.
const (
	testClassAzurite = "azurite"
	testClassBasalt  = "basalt"
	testClassCitrine = "citrine"
)

func runtimeClass(name string, isDefault bool) computev1alpha.RuntimeClass {
	return computev1alpha.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       computev1alpha.RuntimeClassSpec{Default: isDefault},
	}
}

// TestWorkloadWebhookDefaultGateOff checks that defaulting leaves the class
// field as the customer wrote it and never reads the catalog. The webhook has
// no manager here, so a catalog read would panic.
func TestWorkloadWebhookDefaultGateOff(t *testing.T) {
	cases := map[string]struct {
		class string
	}{
		"unset stays unset":              {},
		"an explicit class is untouched": {class: testClassAzurite},
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

// TestDefaultRuntimeClass covers which class a workload that selected none
// records. The catalog's default marker decides it, and a catalog with no
// default leaves the field empty for validation to reject.
func TestDefaultRuntimeClass(t *testing.T) {
	cases := map[string]struct {
		class   string
		catalog runtimeclass.Catalog
		want    string
	}{
		"the class the catalog marks default is stamped": {
			catalog: runtimeclass.Catalog{
				runtimeClass(testClassAzurite, false),
				runtimeClass(testClassBasalt, true),
			},
			want: testClassBasalt,
		},
		"an explicit selection is never overwritten": {
			class: testClassBasalt,
			catalog: runtimeclass.Catalog{
				runtimeClass(testClassAzurite, true),
			},
			want: testClassBasalt,
		},
		"a catalog marking no default stamps nothing": {
			catalog: runtimeclass.Catalog{
				runtimeClass(testClassAzurite, false),
				runtimeClass(testClassBasalt, false),
			},
			want: "",
		},
		"an ambiguous default is not guessed at": {
			catalog: runtimeclass.Catalog{
				runtimeClass(testClassCitrine, true),
				runtimeClass(testClassBasalt, true),
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

// TestRuntimeClassCatalogUnreachable checks that a catalog the webhook cannot
// read rejects the request. Admitting the workload instead would store a class
// selection no provider has agreed to run.
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
