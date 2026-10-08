// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

const (
	// itTestService is the canonical service name the catalog is scoped to.
	itTestService = "compute.datumapis.com"
	// itTestType is the name of the test InstanceType.
	itTestType = "datumcloud-d1-micro-1"
	// itTestTierLabel is a catalog label the hub copy must carry through.
	itTestTierLabel = "compute.datumapis.com/tier"
)

// itTestInstanceType returns a catalog InstanceType with the given name and CPU.
// The fixture mirrors the catalog entries cells actually resolve: a name, a tier
// label, resources, and an active lifecycle.
func itTestInstanceType(name, cpu string, opts ...func(*computev1alpha.InstanceType)) *computev1alpha.InstanceType {
	it := &computev1alpha.InstanceType{
		TypeMeta: metav1.TypeMeta{
			APIVersion: computev1alpha.GroupVersion.String(),
			Kind:       kindInstanceType,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{itTestTierLabel: "micro"},
		},
		Spec: computev1alpha.InstanceTypeSpec{
			Resources: computev1alpha.InstanceTypeResources{
				CPU:    resource.MustParse(cpu),
				Memory: resource.MustParse("1Gi"),
			},
			Lifecycle: computev1alpha.InstanceTypeLifecycle{
				Phase: computev1alpha.InstanceTypePhaseActive,
			},
		},
	}
	for _, opt := range opts {
		opt(it)
	}
	return it
}

// withITCatalogLabel marks the InstanceType as a projector-written hub copy.
func withITCatalogLabel(it *computev1alpha.InstanceType) {
	it.Labels[computev1alpha.InstanceTypeCatalogLabel] = computev1alpha.InstanceTypeCatalogLabelValue
}

// itTestServiceConfiguration returns a ServiceConfiguration resolved to the
// given service that provisions objs under a single "instance-types" resource.
func itTestServiceConfiguration(t *testing.T, name, serviceName string, phase servicesv1alpha1.Phase, objs ...any) *servicesv1alpha1.ServiceConfiguration {
	t.Helper()
	provisioned := make([]servicesv1alpha1.ProvisionedObject, 0, len(objs))
	for _, obj := range objs {
		raw, err := json.Marshal(obj)
		require.NoError(t, err)
		provisioned = append(provisioned, servicesv1alpha1.ProvisionedObject{RawExtension: runtime.RawExtension{Raw: raw}})
	}
	return &servicesv1alpha1.ServiceConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: servicesv1alpha1.ServiceConfigurationSpec{
			ServiceRef: servicesv1alpha1.ServiceReference{Name: name},
			Phase:      phase,
			Provisioning: &servicesv1alpha1.ServiceProvisioningConfig{
				Resources: []servicesv1alpha1.ProvisionedResourceSpec{
					{Name: "instance-types", Objects: provisioned},
				},
			},
		},
		Status: servicesv1alpha1.ServiceConfigurationStatus{ServiceName: serviceName},
	}
}

// newCatalogFakeClient returns a fake root-cluster client holding the given
// ServiceConfigurations.
func newCatalogFakeClient(objs ...client.Object) client.Client {
	s := runtime.NewScheme()
	_ = servicesv1alpha1.AddToScheme(s)
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

// newTestInstanceTypeProjector wires an InstanceTypeProjector that reads the
// catalog from the ServiceConfigurations in catalogClient.
func newTestInstanceTypeProjector(catalogClient, federationClient client.Client) *InstanceTypeProjector {
	return &InstanceTypeProjector{
		FederationClient: federationClient,
		CatalogClient:    catalogClient,
		Catalog:          ServiceConfigurationCatalog{ServiceNames: []string{itTestService}},
	}
}

// itReconcileRequest builds the single catalog request the projector handles.
func itReconcileRequest() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: instanceTypeCatalogKey}}
}

// TestInstanceTypeProjector_ProjectsCatalogToHub verifies that a catalog type is
// written to the hub with its spec and labels plus the catalog label, and that
// the ClusterPropagationPolicy selects labelled InstanceTypes with an empty
// placement (all cells).
func TestInstanceTypeProjector_ProjectsCatalogToHub(t *testing.T) {
	t.Parallel()

	catalogClient := newCatalogFakeClient(itTestServiceConfiguration(t, "compute", itTestService,
		servicesv1alpha1.PhasePublished, itTestInstanceType(itTestType, "250m")))
	federationClient := newKarmadaFakeClient()
	r := newTestInstanceTypeProjector(catalogClient, federationClient)

	_, err := r.Reconcile(context.Background(), itReconcileRequest())
	require.NoError(t, err)

	ctx := context.Background()

	var hub computev1alpha.InstanceType
	require.NoError(t, federationClient.Get(ctx, types.NamespacedName{Name: itTestType}, &hub))
	assert.Equal(t, resource.MustParse("250m"), hub.Spec.Resources.CPU)
	assert.Equal(t, resource.MustParse("1Gi"), hub.Spec.Resources.Memory)
	assert.Equal(t, computev1alpha.InstanceTypePhaseActive, hub.Spec.Lifecycle.Phase)
	assert.Equal(t, "micro", hub.Labels[itTestTierLabel], "catalog labels are carried to the hub copy")
	assert.Equal(t, computev1alpha.InstanceTypeCatalogLabelValue, hub.Labels[computev1alpha.InstanceTypeCatalogLabel])

	var policy karmadapolicyv1alpha1.ClusterPropagationPolicy
	require.NoError(t, federationClient.Get(ctx, types.NamespacedName{Name: instanceTypesPropagationPolicyName}, &policy))
	require.Len(t, policy.Spec.ResourceSelectors, 1)
	sel := policy.Spec.ResourceSelectors[0]
	assert.Equal(t, computev1alpha.GroupVersion.String(), sel.APIVersion)
	assert.Equal(t, kindInstanceType, sel.Kind)
	require.NotNil(t, sel.LabelSelector, "the policy selects only catalog copies")
	assert.Equal(t, map[string]string{
		computev1alpha.InstanceTypeCatalogLabel: computev1alpha.InstanceTypeCatalogLabelValue,
	}, sel.LabelSelector.MatchLabels)
	assert.Nil(t, policy.Spec.Placement.ClusterAffinity, "empty placement targets every cell")
	assert.Nil(t, policy.Spec.Placement.ClusterAffinities, "empty placement targets every cell")
	assert.Equal(t, karmadapolicyv1alpha1.ConflictOverwrite, policy.Spec.ConflictResolution)
}

// TestInstanceTypeProjector_UpdatesChangedSpec verifies that a catalog change
// reaches an existing hub copy, and that labels other writers set on it survive.
func TestInstanceTypeProjector_UpdatesChangedSpec(t *testing.T) {
	t.Parallel()

	existing := itTestInstanceType(itTestType, "250m", withITCatalogLabel, func(it *computev1alpha.InstanceType) {
		it.Labels["example.com/other"] = "kept"
	})
	catalogClient := newCatalogFakeClient(itTestServiceConfiguration(t, "compute", itTestService,
		servicesv1alpha1.PhasePublished, itTestInstanceType(itTestType, "500m")))
	federationClient := newKarmadaFakeClient(existing)
	r := newTestInstanceTypeProjector(catalogClient, federationClient)

	_, err := r.Reconcile(context.Background(), itReconcileRequest())
	require.NoError(t, err)

	var hub computev1alpha.InstanceType
	require.NoError(t, federationClient.Get(context.Background(), types.NamespacedName{Name: itTestType}, &hub))
	assert.Equal(t, resource.MustParse("500m"), hub.Spec.Resources.CPU)
	assert.Equal(t, "kept", hub.Labels["example.com/other"])
}

// TestInstanceTypeProjector_PrunesRemovedTypes verifies that a labelled hub copy
// the catalog no longer declares is deleted, while an InstanceType the projector
// did not write is left alone.
func TestInstanceTypeProjector_PrunesRemovedTypes(t *testing.T) {
	t.Parallel()

	removed := itTestInstanceType("datumcloud-retired", "250m", withITCatalogLabel)
	foreign := itTestInstanceType("someone-elses", "250m")
	catalogClient := newCatalogFakeClient(itTestServiceConfiguration(t, "compute", itTestService,
		servicesv1alpha1.PhasePublished, itTestInstanceType(itTestType, "250m")))
	federationClient := newKarmadaFakeClient(removed, foreign)
	r := newTestInstanceTypeProjector(catalogClient, federationClient)

	_, err := r.Reconcile(context.Background(), itReconcileRequest())
	require.NoError(t, err)

	ctx := context.Background()
	err = federationClient.Get(ctx, types.NamespacedName{Name: removed.Name}, &computev1alpha.InstanceType{})
	assert.True(t, apierrors.IsNotFound(err), "a catalog copy no longer declared must be deleted, got %v", err)
	require.NoError(t, federationClient.Get(ctx, types.NamespacedName{Name: foreign.Name}, &computev1alpha.InstanceType{}),
		"an InstanceType the projector did not write must survive")
	require.NoError(t, federationClient.Get(ctx, types.NamespacedName{Name: itTestType}, &computev1alpha.InstanceType{}))
}

// TestInstanceTypeProjector_MissingCatalogLeavesHubUntouched verifies that when
// no ServiceConfiguration for the service exists (or it is still a Draft), the
// projector does not mistake that for an empty catalog and strip every tier off
// every cell.
func TestInstanceTypeProjector_MissingCatalogLeavesHubUntouched(t *testing.T) {
	t.Parallel()

	tests := map[string][]client.Object{
		"no configuration": nil,
		"other service only": {itTestServiceConfiguration(t, "networking", "networking.datumapis.com",
			servicesv1alpha1.PhasePublished)},
		"draft configuration": {itTestServiceConfiguration(t, "compute", itTestService, servicesv1alpha1.PhaseDraft)},
	}
	for name, configs := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			existing := itTestInstanceType(itTestType, "250m", withITCatalogLabel)
			federationClient := newKarmadaFakeClient(existing)
			r := newTestInstanceTypeProjector(newCatalogFakeClient(configs...), federationClient)

			_, err := r.Reconcile(context.Background(), itReconcileRequest())
			require.NoError(t, err)

			require.NoError(t, federationClient.Get(context.Background(),
				types.NamespacedName{Name: itTestType}, &computev1alpha.InstanceType{}))
		})
	}
}

// TestServiceConfigurationCatalog_Load verifies how the catalog is read out of
// ServiceConfigurations: only InstanceTypes, only from this service's published
// configurations, and a name declared twice resolves to the configuration that
// sorts first.
func TestServiceConfigurationCatalog_Load(t *testing.T) {
	t.Parallel()

	runtimeClass := map[string]any{
		"apiVersion": computev1alpha.GroupVersion.String(),
		"kind":       "RuntimeClass",
		"metadata":   map[string]any{"name": "unikernel"},
	}
	catalogClient := newCatalogFakeClient(
		itTestServiceConfiguration(t, "compute-b", itTestService, servicesv1alpha1.PhasePublished,
			itTestInstanceType(itTestType, "999m"),
			itTestInstanceType("datumcloud-d1-standard-2", "1"),
		),
		itTestServiceConfiguration(t, "compute-a", itTestService, servicesv1alpha1.PhasePublished,
			runtimeClass,
			itTestInstanceType(itTestType, "250m"),
		),
		itTestServiceConfiguration(t, "networking", "networking.datumapis.com", servicesv1alpha1.PhasePublished,
			itTestInstanceType("networking-type", "1"),
		),
		itTestServiceConfiguration(t, "compute-draft", itTestService, servicesv1alpha1.PhaseDraft,
			itTestInstanceType("draft-type", "1"),
		),
	)

	catalog, found, err := ServiceConfigurationCatalog{ServiceNames: []string{itTestService}}.
		Load(context.Background(), catalogClient)
	require.NoError(t, err)
	require.True(t, found)

	byName := map[string]computev1alpha.InstanceType{}
	for _, it := range catalog {
		byName[it.Name] = it
	}
	assert.Len(t, byName, 2)
	assert.Equal(t, resource.MustParse("250m"), byName[itTestType].Spec.Resources.CPU,
		"a duplicate name resolves to the configuration that sorts first")
	assert.Contains(t, byName, "datumcloud-d1-standard-2")
}

// TestClusterCatalog_SkipsDeletingTypes verifies that the single-cluster catalog
// drops a type already being deleted, so its hub copy is pruned while the source
// object is still finalizing.
func TestClusterCatalog_SkipsDeletingTypes(t *testing.T) {
	t.Parallel()

	deleting := itTestInstanceType("datumcloud-retired", "250m", func(it *computev1alpha.InstanceType) {
		now := metav1.NewTime(time.Now())
		it.DeletionTimestamp = &now
		it.Finalizers = []string{"example.com/hold"}
	})
	c := newProjectFakeClient(itTestInstanceType(itTestType, "250m"), deleting)

	catalog, found, err := ClusterCatalog{}.Load(context.Background(), c)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, catalog, 1)
	assert.Equal(t, itTestType, catalog[0].Name)
}

// TestInstanceTypeProjector_SetupRequiresDependencies verifies that a
// projector missing its federation client or catalog fails at setup rather than
// on its first reconcile.
func TestInstanceTypeProjector_SetupRequiresDependencies(t *testing.T) {
	t.Parallel()

	err := (&InstanceTypeProjector{Catalog: ClusterCatalog{}}).SetupWithManager(nil, nil, nil)
	require.ErrorContains(t, err, "federation client")

	err = (&InstanceTypeProjector{FederationClient: newKarmadaFakeClient()}).SetupWithManager(nil, nil, nil)
	require.ErrorContains(t, err, "catalog")
}
