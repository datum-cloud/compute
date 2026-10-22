// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	ctrl "sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

type dnsRetryCluster struct {
	cluster.Cluster
	client client.Client
}

func (c dnsRetryCluster) GetClient() client.Client    { return c.client }
func (c dnsRetryCluster) GetAPIReader() client.Reader { return c.client }
func (c dnsRetryCluster) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return c, nil
}

func TestDNSPublisherRetrySemantics(t *testing.T) {
	for _, unavailable := range []bool{true, false} {
		name := "observation failure uses error backoff"
		if unavailable {
			name = "unconfigured location remains pending"
		}
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, computev1alpha.AddToScheme(scheme))
			require.NoError(t, networkingv1alpha.AddToScheme(scheme))
			for _, gvk := range []schema.GroupVersionKind{dnsResolverContextGVK, dnsRegistrationGVK, dnsContributionGVK} {
				scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
				scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
			}
			instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestInstanceName, Namespace: internalDNSTestNamespace, UID: internalDNSTestInstanceUID}, Spec: computev1alpha.InstanceSpec{NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{{Network: networkingv1alpha.NetworkRef{Name: internalDNSTestNetworkName}}}}}
			network := &networkingv1alpha.Network{ObjectMeta: metav1.ObjectMeta{Name: internalDNSTestNetworkName, Namespace: internalDNSTestNamespace, UID: internalDNSTestVPCUID}}
			contribution := newDNSObject(dnsContributionGVK, internalDNSTestNamespace, internalDNSResourceName(instance.UID, network.UID))
			contribution.SetLabels(map[string]string{internalDNSInstanceUID: string(instance.UID), internalDNSManagedBy: internalDNSManager})
			contribution.Object[internalDNSSpecField] = map[string]any{internalDNSEligibleField: true, internalDNSRecordSetsField: []any{map[string]any{internalDNSRecordTypeField: "A"}}}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, network, contribution).WithStatusSubresource(instance).Build()
			r := &InternalDNSPublisherReconciler{mgr: dnsRetryCluster{client: cl}, ProjectIdentities: map[string]InternalDNSProjectIdentity{internalDNSTestProjectUID: {ProjectUID: internalDNSTestProjectUID}}}
			if !unavailable {
				r.ObserveInterface = func(context.Context, client.Reader, string, *computev1alpha.Instance, computev1alpha.InstanceNetworkInterface) (InternalDNSObservedInterface, error) {
					return InternalDNSObservedInterface{}, context.DeadlineExceeded
				}
			}
			result, err := r.Reconcile(context.Background(), mcreconcile.Request{ClusterName: multicluster.ClusterName(internalDNSTestProjectUID), Request: ctrl.Request{NamespacedName: client.ObjectKeyFromObject(instance)}})
			if unavailable {
				require.NoError(t, err)
				require.GreaterOrEqual(t, result.RequeueAfter, internalDNSPendingWait)
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.Zero(t, result.RequeueAfter)
			}
			current := &computev1alpha.Instance{}
			require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(instance), current))
			require.Len(t, current.Status.DNS, 1)
			require.Equal(t, metav1.ConditionUnknown, current.Status.DNS[0].Conditions[0].Status)
			require.Empty(t, current.Status.Conditions, "DNS observation must not change compute readiness")
			retained := newDNSObject(dnsContributionGVK, contribution.GetNamespace(), contribution.GetName())
			require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(contribution), retained))
			require.Equal(t, contribution.Object[internalDNSSpecField], retained.Object[internalDNSSpecField], "unknown observation must preserve published records")
		})
	}
}
