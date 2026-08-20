// SPDX-License-Identifier: AGPL-3.0-only

package controller_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

	// A pre-existing WorkloadDeployment in the consumer project, labelled so the hooks
	// can find it.
	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName, // Keep name for simplicity
			Namespace: instanceNS,
			Labels: map[string]string{
				"services.miloapis.com/service-name": serviceName,
			},
		},
		Spec: computev1alpha.WorkloadDeploymentSpec{
			Template: computev1alpha.InstanceTemplateSpec{
				Spec: computev1alpha.InstanceSpec{
					Runtime: computev1alpha.InstanceRuntimeSpec{
						Resources: computev1alpha.InstanceRuntimeResources{},
					},
					NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{},
				},
			},
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
		WithObjects(deployment).
		WithStatusSubresource(deployment).
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
		// No retained objects: the WorkloadDeployment itself is what the hooks operate on
		// (status.suspended flips), so it is not a bystander that must stay unchanged.
	)
}

// TestSuspendResumeSetsWorkloadDeploymentAnnotation verifies the actual
// mechanism ComputeSuspend/ComputeResume use to request suspension: the
// SuspendedAnnotation on the target WorkloadDeployment, not Status.Suspended
// directly. A prior version of this hook wrote Status.Suspended directly,
// which WorkloadDeploymentFederator's syncStatusFromDownstream silently
// reverted on the next reconcile because Status is never propagated hub->cell
// (see SuspendedAnnotation's doc comment) — a bug the generic conformance
// test above cannot catch because it only asserts on ServiceConsumer's Paused
// condition, not on the compute-specific field the hook actually writes.
func TestSuspendResumeSetsWorkloadDeploymentAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := computev1alpha.AddToScheme(scheme); err != nil {
		t.Fatalf("adding computev1alpha scheme: %v", err)
	}

	const serviceName = "compute.miloapis.com"
	deployment := &computev1alpha.WorkloadDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
			Labels: map[string]string{
				"services.miloapis.com/service-name": serviceName,
			},
		},
	}

	consumerClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deployment).
		WithStatusSubresource(deployment).
		Build()

	ctx := context.Background()
	key := types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}

	if err := controller.NewComputeSuspend(nil).SuspendConsumer(ctx, "test-project", consumerClient, []string{serviceName}); err != nil {
		t.Fatalf("SuspendConsumer: %v", err)
	}
	var got computev1alpha.WorkloadDeployment
	if err := consumerClient.Get(ctx, key, &got); err != nil {
		t.Fatalf("get after suspend: %v", err)
	}
	if got.Annotations[computev1alpha.SuspendedAnnotation] != "true" {
		t.Fatalf("SuspendedAnnotation = %q, want %q", got.Annotations[computev1alpha.SuspendedAnnotation], "true")
	}

	if err := controller.NewComputeResume(nil).ResumeConsumer(ctx, "test-project", consumerClient, []string{serviceName}); err != nil {
		t.Fatalf("ResumeConsumer: %v", err)
	}
	if err := consumerClient.Get(ctx, key, &got); err != nil {
		t.Fatalf("get after resume: %v", err)
	}
	if _, ok := got.Annotations[computev1alpha.SuspendedAnnotation]; ok {
		t.Fatalf("SuspendedAnnotation still present after resume: %q", got.Annotations[computev1alpha.SuspendedAnnotation])
	}
}
