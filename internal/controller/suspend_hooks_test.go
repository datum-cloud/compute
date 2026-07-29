// SPDX-License-Identifier: AGPL-3.0-only

package controller_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
	consumerprovider "go.miloapis.com/service-catalog/pkg/multicluster-runtime/consumer"
)

func TestConformanceSuspendResume(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("adding clientgo scheme: %v", err)
	}
	if err := computev1alpha.AddToScheme(scheme); err != nil {
		t.Fatalf("adding computev1alpha scheme: %v", err)
	}
	if err := networkingv1alpha.AddToScheme(scheme); err != nil {
		t.Fatalf("adding networkingv1alpha scheme: %v", err)
	}
	if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding servicesv1alpha1 scheme: %v", err)
	}

	const (
		serviceName     = "compute.miloapis.com"
		consumerName    = "sc-testconsumer"
		consumerProject = "test-project"
		instanceNS      = "default"
		instanceName    = "test-instance"
	)

	// A pre-existing Instance in the consumer project, labelled so the hooks
	// can find it.
	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName,
			Namespace: instanceNS,
			Labels: map[string]string{
				"services.miloapis.com/service-name": serviceName,
			},
		},
		Spec: computev1alpha.InstanceSpec{
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Resources: computev1alpha.InstanceRuntimeResources{},
			},
			NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
		},
	}

	// ServiceConsumer in Active phase on the provider side.
	sc := &servicesv1alpha1.ServiceConsumer{
		ObjectMeta: metav1.ObjectMeta{
			Name: consumerName,
		},
		Spec: servicesv1alpha1.ServiceConsumerSpec{
			ConsumerProjectRef: servicesv1alpha1.ConsumerProjectRef{
				Name: consumerProject,
			},
		},
		Status: servicesv1alpha1.ServiceConsumerStatus{
			Phase: servicesv1alpha1.ConsumerPhaseActive,
		},
	}

	consumerClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(instance).
		WithStatusSubresource(instance).
		Build()

	providerClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sc).
		WithStatusSubresource(sc).
		Build()

	consumerprovider.ConformanceSuspendResume(
		t,
		providerClient,
		consumerClient,
		consumerName,
		consumerProject,
		[]string{serviceName},
		controller.NewComputeSuspend(nil),
		controller.NewComputeResume(nil),
		// No retained objects: the Instance itself is what the hooks operate on
		// (spec.suspended flips), so it is not a bystander that must stay unchanged.
	)
}
