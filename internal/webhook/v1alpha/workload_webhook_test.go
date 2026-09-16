// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
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

// TestWorkloadWebhookDefaultMigratesCityCodes covers the shim for manifests
// and stored objects written before placement moved to locations: a placement
// that only names city codes is stored as the equivalent selector.
func TestWorkloadWebhookDefaultMigratesCityCodes(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, false)

	workload := &computev1alpha.Workload{}
	workload.Spec.Placements = []computev1alpha.WorkloadPlacement{{Name: "default", CityCodes: []string{"DFW"}}}

	if err := (&workloadWebhook{}).Default(context.Background(), workload); err != nil {
		t.Fatalf("Default: %v", err)
	}

	placement := workload.Spec.Placements[0]
	if placement.CityCodes != nil {
		t.Errorf("cityCodes = %v, want cleared", placement.CityCodes)
	}
	if placement.LocationSelector == nil || placement.LocationSelector.MatchLabels["topology.datum.net/city-code"] != "DFW" {
		t.Errorf("locationSelector = %v, want a city-code selector for DFW", placement.LocationSelector)
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

// securityClass builds a catalog entry publishing a default security context,
// so a test can state the class contract a container is defaulted from.
func securityClass(name string, defaults *computev1alpha.RuntimeClassSecurityContext) computev1alpha.RuntimeClass {
	class := runtimeClass(name, true)
	class.Spec.Capabilities.Features = []computev1alpha.RuntimeClassFeature{
		computev1alpha.RuntimeClassFeatureSandboxRuntime,
		computev1alpha.RuntimeClassFeatureContainerCapabilities,
	}
	class.Spec.Capabilities.GrantableCapabilities = []computev1alpha.Capability{"CHOWN", "NET_BIND_SERVICE", "SETGID"}
	class.Spec.DefaultSecurityContext = defaults
	return class
}

// sandboxWorkload builds a workload with one sandbox container carrying the
// security context a customer stated.
func sandboxWorkload(class string, stated *computev1alpha.SandboxSecurityContext) *computev1alpha.Workload {
	workload := &computev1alpha.Workload{}
	workload.Spec.Template.Spec.Runtime.Class = class
	workload.Spec.Template.Spec.Runtime.Sandbox = &computev1alpha.SandboxRuntime{
		Containers: []computev1alpha.SandboxContainer{{
			Name:            "api",
			Image:           "ghcr.io/acme/api:1.4.2",
			SecurityContext: stated,
		}},
	}
	return workload
}

func boolPtr(value bool) *bool { return &value }

// TestDefaultSecurityContext covers what a customer reads back on their own
// workload. A container that states nothing records the class's published
// default, and a container that states something keeps exactly what it stated,
// because merging a class default into a customer's statement would put back
// the invisible configuration this replaces.
func TestDefaultSecurityContext(t *testing.T) {
	classDefaults := &computev1alpha.RuntimeClassSecurityContext{
		Capabilities: &computev1alpha.RuntimeClassDefaultCapabilities{
			Add: []computev1alpha.Capability{"SETGID", "CHOWN"},
		},
		AllowPrivilegeEscalation: boolPtr(true),
		SeccompProfile: &computev1alpha.SandboxSeccompProfile{
			Type: computev1alpha.SeccompProfileTypeRuntimeDefault,
		},
	}

	cases := map[string]struct {
		catalog runtimeclass.Catalog
		class   string
		stated  *computev1alpha.SandboxSecurityContext
		want    *computev1alpha.SandboxSecurityContext
	}{
		"a container stating nothing records the published default": {
			catalog: runtimeclass.Catalog{securityClass(testClassBasalt, classDefaults)},
			class:   testClassBasalt,
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
					Add:  []computev1alpha.Capability{"CHOWN", "SETGID"},
				},
				AllowPrivilegeEscalation: boolPtr(true),
				SeccompProfile: &computev1alpha.SandboxSeccompProfile{
					Type: computev1alpha.SeccompProfileTypeRuntimeDefault,
				},
			},
		},
		"stated capabilities are kept and never merged with the default": {
			catalog: runtimeclass.Catalog{securityClass(testClassBasalt, classDefaults)},
			class:   testClassBasalt,
			stated: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Add: []computev1alpha.Capability{"NET_BIND_SERVICE"},
				},
			},
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Add: []computev1alpha.Capability{"NET_BIND_SERVICE"},
				},
				AllowPrivilegeEscalation: boolPtr(true),
				SeccompProfile: &computev1alpha.SandboxSeccompProfile{
					Type: computev1alpha.SeccompProfileTypeRuntimeDefault,
				},
			},
		},
		"a stated seccomp profile and escalation survive defaulting": {
			catalog: runtimeclass.Catalog{securityClass(testClassBasalt, classDefaults)},
			class:   testClassBasalt,
			stated: &computev1alpha.SandboxSecurityContext{
				AllowPrivilegeEscalation: boolPtr(false),
				SeccompProfile: &computev1alpha.SandboxSeccompProfile{
					Type: computev1alpha.SeccompProfileTypeUnconfined,
				},
			},
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
					Add:  []computev1alpha.Capability{"CHOWN", "SETGID"},
				},
				AllowPrivilegeEscalation: boolPtr(false),
				SeccompProfile: &computev1alpha.SandboxSeccompProfile{
					Type: computev1alpha.SeccompProfileTypeUnconfined,
				},
			},
		},
		"a class publishing no default still records the capability floor": {
			catalog: runtimeclass.Catalog{securityClass(testClassBasalt, nil)},
			class:   testClassBasalt,
			want: &computev1alpha.SandboxSecurityContext{
				Capabilities: &computev1alpha.SandboxCapabilities{
					Drop: []computev1alpha.Capability{computev1alpha.CapabilityAll},
				},
			},
		},
		"a class the catalog does not offer changes nothing": {
			catalog: runtimeclass.Catalog{securityClass(testClassBasalt, classDefaults)},
			class:   testClassCitrine,
			want:    nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			workload := sandboxWorkload(tc.class, tc.stated)

			defaultSecurityContext(workload, tc.catalog)

			got := workload.Spec.Template.Spec.Runtime.Sandbox.Containers[0].SecurityContext
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("security context mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestDefaultSecurityContextIsIdempotent checks that re-admitting an unchanged
// workload stores an unchanged object. Defaulting runs on every update, and the
// stored security context takes part in the instance template hash, so a value
// that drifted on each pass would roll instances that nothing changed.
func TestDefaultSecurityContextIsIdempotent(t *testing.T) {
	catalog := runtimeclass.Catalog{securityClass(testClassBasalt, &computev1alpha.RuntimeClassSecurityContext{
		Capabilities: &computev1alpha.RuntimeClassDefaultCapabilities{
			Add: []computev1alpha.Capability{"SETGID", "CHOWN"},
		},
		AllowPrivilegeEscalation: boolPtr(false),
		SeccompProfile: &computev1alpha.SandboxSeccompProfile{
			Type: computev1alpha.SeccompProfileTypeRuntimeDefault,
		},
	})}

	workload := sandboxWorkload(testClassBasalt, nil)

	defaultSecurityContext(workload, catalog)
	once := workload.DeepCopy()

	defaultSecurityContext(workload, catalog)

	if diff := cmp.Diff(once, workload); diff != "" {
		t.Errorf("second defaulting changed the workload (-first +second):\n%s", diff)
	}
}
