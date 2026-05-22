package webhook

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

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
	}

	return nil, nil
}

func (r *workloadWebhook) ValidateUpdate(_ context.Context, _, _ *computev1alpha.Workload) (admission.Warnings, error) {
	// TODO(user): fill in your validation logic upon object update.
	return nil, nil
}

func (r *workloadWebhook) ValidateDelete(_ context.Context, _ *computev1alpha.Workload) (admission.Warnings, error) {
	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
