package webhook

import (
	"context"

	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/validation"
	computewebhook "go.datum.net/compute/internal/webhook"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
)

// SetupWorkloadWebhookWithManager will setup the manager to manage workload
// webhooks
func SetupWorkloadWebhookWithManager(mgr mcmanager.Manager) error {

	webhook := &workloadWebhook{
		mgr: mgr,
	}

	return ctrl.NewWebhookManagedBy(mgr.GetLocalManager(), &computev1alpha.Workload{}).
		WithDefaulter(webhook).
		WithValidator(webhook).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-compute-datumapis-com-v1alpha-workload,mutating=true,failurePolicy=fail,sideEffects=None,groups=compute.datumapis.com,resources=workloads,verbs=create;update,versions=v1alpha,name=mworkload.kb.io,admissionReviewVersions=v1

type workloadWebhook struct {
	mgr mcmanager.Manager
}

var _ admission.Defaulter[*computev1alpha.Workload] = &workloadWebhook{}
var _ admission.Validator[*computev1alpha.Workload] = &workloadWebhook{}

// Default implements admission.Defaulter so a mutating webhook will be registered for the type.
func (r *workloadWebhook) Default(_ context.Context, _ *computev1alpha.Workload) error {
	// // TODO(jreese) review and test gateway defaulting / logic
	// if gw := workload.Spec.Gateway; gw != nil {
	// 	for i, tcpRoute := range gw.TCPRoutes {
	// 		for j := range tcpRoute.ParentRefs {
	// 			workload.Spec.Gateway.TCPRoutes[i].ParentRefs[j].Name = "workload-gateway"
	// 		}

	// 		for j := range tcpRoute.Rules {
	// 			for k := range tcpRoute.Rules[j].BackendRefs {
	// 				// TODO(jreese) think about this Kind more
	// 				kind := gatewayv1.Kind("NamedPort")
	// 				workload.Spec.Gateway.TCPRoutes[i].Rules[j].
	// 					BackendRefs[k].Kind = &kind
	// 			}
	// 		}
	// 	}
	// }

	// TODO(user): fill in your defaulting logic.
	return nil
}

// +kubebuilder:webhook:path=/validate-compute-datumapis-com-v1alpha-workload,mutating=false,failurePolicy=fail,sideEffects=None,groups=compute.datumapis.com,resources=workloads,verbs=create;update,versions=v1alpha,name=vworkload.kb.io,admissionReviewVersions=v1

func (r *workloadWebhook) ValidateCreate(ctx context.Context, workload *computev1alpha.Workload) (admission.Warnings, error) {
	clusterName := computewebhook.ClusterNameFromContext(ctx)

	cluster, err := r.mgr.GetCluster(ctx, multicluster.ClusterName(clusterName))
	if err != nil {
		return nil, err
	}
	clusterClient := cluster.GetClient()

	logger := logf.FromContext(ctx).WithValues("cluster", clusterName)
	logger.Info("Validating Workload Create", "name", workload.GetName(), "cluster", clusterName)

	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// TODO(jreese) validate caller access to individual locations, consider what
	// that means for the scheduling phase, since there would not currently be
	// sufficient context to know who created the workload and what locations
	// are valid candidates based on that. Maybe an annotation, or spec field?
	var locations locationsv1alpha1.LocationList
	if err := clusterClient.List(ctx, &locations); err != nil {
		return nil, fmt.Errorf("failed to list locations: %w", err)
	}

	validLocations := sets.Set[string]{}
	for _, location := range locations.Items {
		if apimeta.IsStatusConditionTrue(location.Status.Conditions, locationsv1alpha1.LocationConditionReady) {
			validLocations.Insert(location.Name)
		}
	}

	opts := validation.WorkloadValidationOptions{
		Context:          ctx,
		Client:           clusterClient,
		AdmissionRequest: req,
		Workload:         workload,
		ValidLocations:   sets.List(validLocations),
	}

	if errs := validation.ValidateWorkloadCreate(workload, opts); len(errs) > 0 {
		return nil, errors.NewInvalid(workload.GroupVersionKind().GroupKind(), workload.Name, errs)
	}

	return nil, nil
}

func (r *workloadWebhook) ValidateUpdate(ctx context.Context, _ *computev1alpha.Workload, newWorkload *computev1alpha.Workload) (admission.Warnings, error) {
	clusterName := computewebhook.ClusterNameFromContext(ctx)

	cluster, err := r.mgr.GetCluster(ctx, multicluster.ClusterName(clusterName))
	if err != nil {
		return nil, err
	}
	clusterClient := cluster.GetClient()

	logger := logf.FromContext(ctx).WithValues("cluster", clusterName)
	logger.Info("Validating Workload Update", "name", newWorkload.GetName(), "cluster", clusterName)

	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return nil, err
	}

	var locations locationsv1alpha1.LocationList
	if err := clusterClient.List(ctx, &locations); err != nil {
		return nil, fmt.Errorf("failed to list locations: %w", err)
	}

	validLocations := sets.Set[string]{}
	for _, location := range locations.Items {
		if apimeta.IsStatusConditionTrue(location.Status.Conditions, locationsv1alpha1.LocationConditionReady) {
			validLocations.Insert(location.Name)
		}
	}

	opts := validation.WorkloadValidationOptions{
		Context:          ctx,
		Client:           clusterClient,
		AdmissionRequest: req,
		Workload:         newWorkload,
		ValidLocations:   sets.List(validLocations),
	}

	if errs := validation.ValidateWorkloadCreate(newWorkload, opts); len(errs) > 0 {
		return nil, errors.NewInvalid(newWorkload.GroupVersionKind().GroupKind(), newWorkload.Name, errs)
	}

	return nil, nil
}

func (r *workloadWebhook) ValidateDelete(_ context.Context, _ *computev1alpha.Workload) (admission.Warnings, error) {
	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
