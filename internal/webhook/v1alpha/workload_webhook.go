package webhook

import (
	"context"

	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/validation"
	computewebhook "go.datum.net/compute/internal/webhook"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// SetupWorkloadWebhookWithManager will setup the manager to manage workload
// webhooks
func SetupWorkloadWebhookWithManager(mgr mcmanager.Manager) error {

	webhook := &workloadWebhook{
		mgr: mgr,
	}

	return ctrl.NewWebhookManagedBy(mgr.GetLocalManager()).
		For(&computev1alpha.Workload{}).
		WithDefaulter(webhook).
		WithValidator(webhook).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-compute-datumapis-com-v1alpha-workload,mutating=true,failurePolicy=fail,sideEffects=None,groups=compute.datumapis.com,resources=workloads,verbs=create;update,versions=v1alpha,name=mworkload.kb.io,admissionReviewVersions=v1

type workloadWebhook struct {
	mgr mcmanager.Manager
}

var _ admission.CustomDefaulter = &workloadWebhook{}
var _ admission.CustomValidator = &workloadWebhook{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *workloadWebhook) Default(ctx context.Context, obj runtime.Object) error {
	workload, ok := obj.(*computev1alpha.Workload)
	if !ok {
		return fmt.Errorf("unexpected type %T", obj)
	}
	_ = workload

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

func (r *workloadWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	workload, ok := obj.(*computev1alpha.Workload)
	if !ok {
		return nil, fmt.Errorf("unexpected type %T", obj)
	}

	clusterName := computewebhook.ClusterNameFromContext(ctx)

	cluster, err := r.mgr.GetCluster(ctx, clusterName)
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
	var locations networkingv1alpha.LocationList
	if err := clusterClient.List(ctx, &locations); err != nil {
		return nil, fmt.Errorf("failed to list locations: %w", err)
	}

	validCityCodes := sets.Set[string]{}
	for _, location := range locations.Items {
		cityCode, ok := location.Spec.Topology["topology.datum.net/city-code"]
		if ok {
			validCityCodes.Insert(cityCode)
		}
	}

	opts := validation.WorkloadValidationOptions{
		Context:          ctx,
		Client:           clusterClient,
		AdmissionRequest: req,
		Workload:         workload,
		ValidCityCodes:   sets.List(validCityCodes),
	}

	if errs := validation.ValidateWorkloadCreate(workload, opts); len(errs) > 0 {
		return nil, errors.NewInvalid(obj.GetObjectKind().GroupVersionKind().GroupKind(), workload.Name, errs)
	}

	return nil, nil
}

func (r *workloadWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldworkload, ok := oldObj.(*computev1alpha.Workload)
	if !ok {
		return nil, fmt.Errorf("unexpected type %T", oldObj)
	}

	_ = oldworkload

	newworkload, ok := newObj.(*computev1alpha.Workload)
	if !ok {
		return nil, fmt.Errorf("unexpected type %T", newObj)
	}

	_ = newworkload

	// TODO(user): fill in your validation logic upon object update.
	return nil, nil
}

func (r *workloadWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	workload, ok := obj.(*computev1alpha.Workload)
	if !ok {
		return nil, fmt.Errorf("unexpected type %T", obj)
	}
	_ = workload

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
