// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

func networkAttachmentTestClient(classes ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	if err := computev1alpha.AddToScheme(scheme); err != nil {
		panic(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(classes...).Build()
}

func runtimeClass(name string, attachment computev1alpha.RuntimeClassNetworkAttachment) *computev1alpha.RuntimeClass {
	return &computev1alpha.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: computev1alpha.RuntimeClassSpec{
			ControllerName:    "compute.datumapis.com/test-provider",
			Isolation:         computev1alpha.RuntimeClassIsolation{Boundary: "test"},
			NetworkAttachment: attachment,
		},
	}
}

func workloadInClass(class string) *computev1alpha.Workload {
	workload := &computev1alpha.Workload{}
	workload.Spec.Template.Spec.Runtime.Class = class
	return workload
}

// The class catalog is readable only where a deployment is created, so the
// answer is resolved there. A class that states nothing, and a class the
// catalog does not publish, both leave the cell deciding.
func TestWorkloadNetworkAttachmentResolvesFromTheCatalog(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, true)

	cl := networkAttachmentTestClient(
		runtimeClass("general-purpose", computev1alpha.RuntimeClassNetworkAttachmentHypervisorDeclared),
		runtimeClass("unikernel", ""),
	)
	r := &WorkloadReconciler{}

	attachment, err := r.networkAttachment(context.Background(), cl, workloadInClass("general-purpose"))
	require.NoError(t, err)
	assert.Equal(t, computev1alpha.RuntimeClassNetworkAttachmentHypervisorDeclared, attachment)

	attachment, err = r.networkAttachment(context.Background(), cl, workloadInClass("unikernel"))
	require.NoError(t, err)
	assert.Empty(t, attachment, "a class that states nothing leaves the cell deciding")

	attachment, err = r.networkAttachment(context.Background(), cl, workloadInClass("not-published"))
	require.NoError(t, err)
	assert.Empty(t, attachment, "a class the catalog does not publish resolves to nothing")

	attachment, err = r.networkAttachment(context.Background(), cl, workloadInClass(""))
	require.NoError(t, err)
	assert.Empty(t, attachment, "a workload in no class resolves to nothing")
}

// With runtime class selection off, the catalog is not consulted at all.
func TestWorkloadNetworkAttachmentIgnoredWhenClassesDisabled(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.RuntimeClasses, false)

	cl := networkAttachmentTestClient(
		runtimeClass("general-purpose", computev1alpha.RuntimeClassNetworkAttachmentHypervisorDeclared))
	r := &WorkloadReconciler{}

	attachment, err := r.networkAttachment(context.Background(), cl, workloadInClass("general-purpose"))
	require.NoError(t, err)
	assert.Empty(t, attachment)
}

// A deployment carries the resolved answer, and the cell translates it without
// consulting anything.
func TestNetworkAttachmentMode(t *testing.T) {
	t.Parallel()

	deployment := func(attachment computev1alpha.RuntimeClassNetworkAttachment) *computev1alpha.WorkloadDeployment {
		return &computev1alpha.WorkloadDeployment{
			Spec: computev1alpha.WorkloadDeploymentSpec{NetworkAttachment: attachment},
		}
	}

	assert.Equal(t, networkingv1alpha.NetworkInterfaceAttachmentModeNetns,
		networkAttachmentMode(deployment(computev1alpha.RuntimeClassNetworkAttachmentNetns)))
	assert.Equal(t, networkingv1alpha.NetworkInterfaceAttachmentModeHypervisor,
		networkAttachmentMode(deployment(computev1alpha.RuntimeClassNetworkAttachmentHypervisor)))
	assert.Equal(t, networkingv1alpha.NetworkInterfaceAttachmentModeHypervisorDeclared,
		networkAttachmentMode(deployment(computev1alpha.RuntimeClassNetworkAttachmentHypervisorDeclared)))
	assert.Empty(t, networkAttachmentMode(deployment("")),
		"a deployment carrying nothing asks the networking layer for nothing")
}
