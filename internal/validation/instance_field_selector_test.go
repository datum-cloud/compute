// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	fsTestNamespace = "default"
	fsTestClassA    = "azurite"
	fsTestClassB    = "basalt"

	fsTestInstanceA1      = "instance-class-a"
	fsTestInstanceA2      = "instance-class-a-2"
	fsTestInstanceB       = "instance-class-b"
	fsTestInstanceNoClass = "instance-no-class"
)

// fsTestInstance returns an Instance in the shape a control plane stores one,
// with the runtime class set to the given value. An empty class stands for an
// Instance created before any class was resolved for it.
func fsTestInstance(name, class string) *computev1alpha.Instance {
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: fsTestNamespace,
		},
		Spec: computev1alpha.InstanceSpec{
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Class: class,
				Resources: computev1alpha.InstanceRuntimeResources{
					InstanceType: "datumcloud/d1-standard-2",
				},
			},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{
				{
					Name:    "eth0",
					Network: networkingv1alpha.NetworkRef{Name: "default"},
				},
			},
		},
	}
}

// TestInstanceRuntimeClassFieldSelector covers the server-side selection a
// runtime class provider scopes its informer cache with. The API server, not
// the client, has to apply the selector, so only a real server can show that
// the CRD declares the field and that an unset class behaves as the empty
// string.
func TestInstanceRuntimeClassFieldSelector(t *testing.T) {
	ctx := context.Background()
	c := startRuntimeClassEnvtest(t)

	for name, class := range map[string]string{
		fsTestInstanceA1:      fsTestClassA,
		fsTestInstanceB:       fsTestClassB,
		fsTestInstanceNoClass: "",
		fsTestInstanceA2:      fsTestClassA,
	} {
		instance := fsTestInstance(name, class)
		require.NoError(t, c.Create(ctx, instance), name)
		t.Cleanup(func() { _ = c.Delete(ctx, instance) })
	}

	listNames := func(t *testing.T, selector client.ListOption) []string {
		t.Helper()
		var instances computev1alpha.InstanceList
		require.NoError(t, c.List(ctx, &instances, client.InNamespace(fsTestNamespace), selector))
		names := make([]string, 0, len(instances.Items))
		for _, instance := range instances.Items {
			names = append(names, instance.Name)
		}
		return names
	}

	t.Run("a provider lists only the instances in the class it serves", func(t *testing.T) {
		names := listNames(t, client.MatchingFieldsSelector{
			Selector: computev1alpha.InstanceRuntimeClassFieldSelector(fsTestClassA),
		})
		require.ElementsMatch(t, []string{fsTestInstanceA1, fsTestInstanceA2}, names)
	})

	// The default provider serves its own class and every instance no class
	// was resolved for. An unset class reads as the empty string, so excluding
	// the classes it does not serve is how it claims both.
	t.Run("excluding another class matches an unset class", func(t *testing.T) {
		names := listNames(t, client.MatchingFieldsSelector{
			Selector: computev1alpha.InstanceExcludingRuntimeClassFieldSelector(fsTestClassB),
		})
		require.ElementsMatch(t, []string{fsTestInstanceA1, fsTestInstanceA2, fsTestInstanceNoClass}, names)
	})

	t.Run("an unset class is selectable as the empty string", func(t *testing.T) {
		names := listNames(t, client.MatchingFieldsSelector{
			Selector: computev1alpha.InstanceRuntimeClassFieldSelector(""),
		})
		require.Equal(t, []string{fsTestInstanceNoClass}, names)
	})

	// An API server refuses a selector on a field a CRD does not declare
	// selectable, which is why the CRD has to roll out before any provider
	// selects on it.
	t.Run("an undeclared field is not selectable", func(t *testing.T) {
		var instances computev1alpha.InstanceList
		err := c.List(ctx, &instances, client.InNamespace(fsTestNamespace),
			client.MatchingFields{"spec.runtime.resources.instanceType": "datumcloud/d1-standard-2"})
		require.ErrorContains(t, err, "field label not supported")
	})
}
