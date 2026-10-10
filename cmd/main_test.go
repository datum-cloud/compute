// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/yaml"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	multiclusterproviders "go.miloapis.com/milo/pkg/multicluster-runtime"

	"go.datum.net/compute/internal/config"
	"go.datum.net/compute/internal/locations"
)

func TestInternalDNSProjectIdentities(t *testing.T) {
	identities, err := internalDNSProjectIdentities(config.InternalDNSConfig{
		PrincipalSubject: "system:serviceaccount:compute-system:compute-manager",
		LeaseDuration:    metav1.Duration{Duration: 60 * time.Second},
		Projects: []config.InternalDNSProjectIdentity{{
			Name: "project-a", ProjectUID: "project-uid", SourceClusterUID: "source-uid",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "source-uid", identities["project-a"].SourceClusterUID)

	_, err = internalDNSProjectIdentities(config.InternalDNSConfig{PrincipalSubject: "subject"})
	assert.Error(t, err, "enabled publishing requires at least one pinned project identity")
	_, err = internalDNSProjectIdentities(config.InternalDNSConfig{
		PrincipalSubject: "subject",
		LeaseDuration:    metav1.Duration{Duration: 91 * time.Second},
		Projects: []config.InternalDNSProjectIdentity{{
			Name: "project-a", ProjectUID: "project-uid", SourceClusterUID: "source-uid",
		}},
	})
	assert.Error(t, err, "a producer must not configure a lease DNS admission rejects")
}

func TestInternalDNSObservationSourcesRequireExplicitCredentials(t *testing.T) {
	_, err := internalDNSObservationSources(config.InternalDNSConfig{})
	require.Error(t, err)
	_, err = internalDNSObservationSources(config.InternalDNSConfig{
		ObservationSources: []config.InternalDNSObservationSource{{Location: "dfw"}},
	})
	require.Error(t, err)
	path := filepath.Join(t.TempDir(), "edge.kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
clusters:
  - name: edge
    cluster:
      server: https://127.0.0.1:6443
contexts:
  - name: edge
    context:
      cluster: edge
current-context: edge
`), 0o600))
	source := config.InternalDNSObservationSource{Location: "dfw", ClusterUID: "edge-uid", KubeconfigPath: path}
	sources, err := internalDNSObservationSources(config.InternalDNSConfig{
		ObservationSources: []config.InternalDNSObservationSource{source},
	})
	require.NoError(t, err)
	require.EqualValues(t, "edge-uid", sources["dfw"].ClusterUID)
	_, err = internalDNSObservationSources(config.InternalDNSConfig{
		ObservationSources: []config.InternalDNSObservationSource{source, source},
	})
	require.Error(t, err)
}

// TestComputeWatchProviderClaims is the #171 guard: quota enforcement (and thus
// the ResourceClaim watch) is wired only in Milo mode. Single/cluster mode must
// stay false, so the manager never engages a ResourceClaim watch against a cell
// that has no quota CRD.
func TestComputeWatchProviderClaims(t *testing.T) {
	assert.True(t, computeWatchProviderClaims(multiclusterproviders.ProviderMilo),
		"milo mode wires the ResourceClaim watch")
	assert.False(t, computeWatchProviderClaims(multiclusterproviders.ProviderSingle),
		"single mode must not wire the watch (#171)")
	assert.False(t, computeWatchProviderClaims(multiclusterproviders.ProviderKind),
		"non-milo modes disable quota")
}

// TestLoadServerConfig_LocationSource covers the startup guard: a config that
// names an unknown location source fails to load rather than surfacing later
// on every reconcile.
func TestLoadServerConfig_LocationSource(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return path
	}

	const header = `apiVersion: apiserver.config.datumapis.com/v1alpha1
kind: WorkloadOperator
metricsServer:
  bindAddress: "0"
`

	cfg, err := loadServerConfig(write(t, header))
	require.NoError(t, err)
	assert.Equal(t, locations.SourceNetworkServices, cfg.LocationSource)

	cfg, err = loadServerConfig(write(t, header+"locationSource: Locations\n"))
	require.NoError(t, err)
	assert.Equal(t, locations.SourceLocations, cfg.LocationSource)

	_, err = loadServerConfig(write(t, header+"locationSource: Nonsense\n"))
	require.Error(t, err)
}

// TestSchemeResolvesLocationKinds is the runtime half of the typed locations
// dependency: compiling against the types says nothing about whether a client
// can resolve them. A kind missing here fails every list at runtime with "no
// kind is registered", not at build time.
func TestSchemeResolvesLocationKinds(t *testing.T) {
	for _, object := range []client.Object{
		&locationsv1alpha1.Location{},
		&locationsv1alpha1.ServingLocation{},
		&networkingv1alpha.LocationBinding{},
		&networkingv1alpha.ServingLocation{},
	} {
		gvk, err := apiutil.GVKForObject(object, scheme)
		require.NoErrorf(t, err, "%T must be registered on the manager scheme", object)
		assert.NotEmpty(t, gvk.Kind)
	}
}

// TestServingLocationObjectIsRegistered pins the watch the deployment
// reconciler installs: whichever source is selected, the object it watches has
// to be resolvable on the manager scheme.
func TestServingLocationObjectIsRegistered(t *testing.T) {
	for _, source := range []locations.Source{"", locations.SourceNetworkServices, locations.SourceLocations} {
		object, err := locations.ServingLocationObject(source)
		require.NoError(t, err)

		_, err = apiutil.GVKForObject(object, scheme)
		require.NoErrorf(t, err, "the watch object for source %q must be registered", source)
	}
}

// TestControllerRoleGrantsPlacementLocationWatches is the RBAC half of the
// locations dependency, and the regression guard for the watch the workload
// reconciler installs on a project's placement locations.
//
// A watch the shipped ClusterRole does not permit is worse than a missing
// feature. The informer retries the rejected list forever, that control plane's
// cache never syncs, and controller-runtime blocks every controller on the
// manager from starting — the workload reconciler, the referenced-data
// reconciler and the deployment federator all stall, so nothing is federated,
// no finalizer is written, and the pod stays Ready while reconciling nothing
// until cluster engagement times out and it restarts into the same state.
//
// Both sources are covered: the manager is deployed with one ClusterRole and
// locationSource is config, so whichever source a deployment selects has to be
// permitted by the role that ships.
func TestControllerRoleGrantsPlacementLocationWatches(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "config", "components", "controller_rbac", "role.yaml"))
	require.NoError(t, err)

	var role rbacv1.ClusterRole
	require.NoError(t, yaml.Unmarshal(body, &role))

	// granted reports whether the role permits the verb on the resource, taking
	// the wildcards RBAC honours into account.
	granted := func(group, resource, verb string) bool {
		matches := func(values []string, want string) bool {
			return slices.Contains(values, want) || slices.Contains(values, rbacv1.ResourceAll)
		}
		for _, rule := range role.Rules {
			if len(rule.ResourceNames) > 0 {
				continue
			}
			if matches(rule.APIGroups, group) && matches(rule.Resources, resource) && matches(rule.Verbs, verb) {
				return true
			}
		}
		return false
	}

	// The kinds every source watches or lists, named as the API server names
	// them in an RBAC rule.
	for _, resource := range []struct{ group, name string }{
		{"networking.datumapis.com", "locationbindings"},
		{"networking.datumapis.com", "servinglocations"},
		{"locations.miloapis.com", "locations"},
		{"locations.miloapis.com", "servinglocations"},
		{"services.miloapis.com", "serviceavailabilities"},
	} {
		for _, verb := range []string{"get", "list", "watch"} {
			assert.Truef(t, granted(resource.group, resource.name, verb),
				"the controller ClusterRole must grant %q on %s.%s: a watch it cannot list wedges the manager",
				verb, resource.name, resource.group)
		}
	}
}
