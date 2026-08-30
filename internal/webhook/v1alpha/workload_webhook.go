package webhook

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	"go.datum.net/compute/internal/locations"
	"go.datum.net/compute/internal/validation"
	computewebhook "go.datum.net/compute/internal/webhook"
	"go.datum.net/compute/pkg/runtimeclass"
)

// SetupWorkloadWebhookWithManager will setup the manager to manage workload
// webhooks
func SetupWorkloadWebhookWithManager(mgr mcmanager.Manager, locationSource locations.Source) error {

	webhook := &workloadWebhook{
		mgr:            mgr,
		locationSource: locationSource,
	}

	return ctrl.NewWebhookManagedBy(mgr.GetLocalManager(), &computev1alpha.Workload{}).
		WithDefaulter(webhook).
		WithValidator(webhook).
		Complete()
}

// Admission resolves the runtime class a workload selected, so the webhook
// needs read access to RuntimeClass in every project control plane.
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=runtimeclasses,verbs=get;list;watch

// +kubebuilder:webhook:path=/mutate-compute-datumapis-com-v1alpha-workload,mutating=true,failurePolicy=fail,sideEffects=None,groups=compute.datumapis.com,resources=workloads,verbs=create;update,versions=v1alpha,name=mworkload.kb.io,admissionReviewVersions=v1

type workloadWebhook struct {
	mgr            mcmanager.Manager
	locationSource locations.Source
}

func (r *workloadWebhook) validCityCodes(ctx context.Context, c client.Client) ([]string, error) {
	placementLocations, err := locations.ListPlacementLocations(ctx, c, r.locationSource)
	if err != nil {
		return nil, err
	}
	return sets.List(locations.CityCodes(placementLocations)), nil
}

var _ admission.Defaulter[*computev1alpha.Workload] = &workloadWebhook{}
var _ admission.Validator[*computev1alpha.Workload] = &workloadWebhook{}

// Default implements admission.Defaulter so a mutating webhook will be registered for the type.
func (r *workloadWebhook) Default(ctx context.Context, workload *computev1alpha.Workload) error {
	// With the gate off there is only one runtime class, so the field stays
	// empty rather than recording a class name the platform does not yet honor.
	if features.FeatureGate.Enabled(features.RuntimeClasses) {
		catalog, err := r.runtimeClassCatalog(ctx)
		if err != nil {
			return err
		}
		defaultRuntimeClass(workload, catalog)
	}

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

	clusterClient, err := r.clusterClient(ctx)
	if err != nil {
		return nil, err
	}

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
	validCityCodes, err := r.validCityCodes(ctx, clusterClient)
	if err != nil {
		return nil, err
	}

	runtimeClasses, err := r.runtimeClassCatalogWhenEnabled(ctx)
	if err != nil {
		return nil, err
	}

	opts := validation.WorkloadValidationOptions{
		Context:          ctx,
		Client:           clusterClient,
		AdmissionRequest: req,
		Workload:         workload,
		ValidCityCodes:   validCityCodes,
		RuntimeClasses:   runtimeClasses,
	}

	if errs := validation.ValidateWorkloadCreate(workload, opts); len(errs) > 0 {
		return nil, errors.NewInvalid(workload.GroupVersionKind().GroupKind(), workload.Name, errs)
	}

	return nil, nil
}

func (r *workloadWebhook) ValidateUpdate(ctx context.Context, oldWorkload *computev1alpha.Workload, newWorkload *computev1alpha.Workload) (admission.Warnings, error) {
	clusterName := computewebhook.ClusterNameFromContext(ctx)

	clusterClient, err := r.clusterClient(ctx)
	if err != nil {
		return nil, err
	}

	logger := logf.FromContext(ctx).WithValues("cluster", clusterName)
	logger.Info("Validating Workload Update", "name", newWorkload.GetName(), "cluster", clusterName)

	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return nil, err
	}

	validCityCodes, err := r.validCityCodes(ctx, clusterClient)
	if err != nil {
		return nil, err
	}

	runtimeClasses, err := r.runtimeClassCatalogWhenEnabled(ctx)
	if err != nil {
		return nil, err
	}

	opts := validation.WorkloadValidationOptions{
		Context:          ctx,
		Client:           clusterClient,
		AdmissionRequest: req,
		Workload:         newWorkload,
		ValidCityCodes:   validCityCodes,
		RuntimeClasses:   runtimeClasses,
	}

	if errs := validation.ValidateWorkloadUpdate(newWorkload, oldWorkload, opts); len(errs) > 0 {
		return nil, errors.NewInvalid(newWorkload.GroupVersionKind().GroupKind(), newWorkload.Name, errs)
	}

	return nil, nil
}

func (r *workloadWebhook) ValidateDelete(_ context.Context, _ *computev1alpha.Workload) (admission.Warnings, error) {
	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}

// defaultRuntimeClass writes the selected class onto the stored workload
// instead of resolving it on read. A later change to the catalog default then
// cannot move a running workload to a different class.
//
// When the catalog publishes no default, the field stays empty and validation
// rejects the workload with the list of available classes.
func defaultRuntimeClass(workload *computev1alpha.Workload, catalog runtimeclass.Catalog) {
	if len(workload.Spec.Template.Spec.Runtime.Class) > 0 {
		return
	}
	if defaultClass := catalog.Default(); defaultClass != nil {
		workload.Spec.Template.Spec.Runtime.Class = defaultClass.Name
	}
}

// runtimeClassCatalog lists the runtime classes published to the control plane
// the request is admitted into. The platform projects classes read-only into
// every project control plane, so validation uses the same catalog the customer
// reads.
//
// A read failure rejects the request. Storing a workload that selects a class
// no provider runs surfaces later as a workload that is never placed.
func (r *workloadWebhook) runtimeClassCatalog(ctx context.Context) (runtimeclass.Catalog, error) {
	clusterClient, err := r.clusterClient(ctx)
	if err != nil {
		return nil, err
	}
	return runtimeClassCatalog(ctx, clusterClient)
}

func runtimeClassCatalog(ctx context.Context, clusterClient client.Client) (runtimeclass.Catalog, error) {
	var classes computev1alpha.RuntimeClassList
	if err := clusterClient.List(ctx, &classes); err != nil {
		return nil, fmt.Errorf("failed to list runtime classes: %w", err)
	}

	return classes.Items, nil
}

// clusterClient returns the client for the project control plane the request is
// admitted into.
func (r *workloadWebhook) clusterClient(ctx context.Context) (client.Client, error) {
	cluster, err := r.mgr.GetCluster(ctx, multicluster.ClusterName(computewebhook.ClusterNameFromContext(ctx)))
	if err != nil {
		return nil, err
	}
	return cluster.GetClient(), nil
}

// runtimeClassCatalogWhenEnabled lists the catalog only when runtime class
// selection is enabled. With the gate off, a control plane that has never
// published a class admits workloads as before.
func (r *workloadWebhook) runtimeClassCatalogWhenEnabled(ctx context.Context) (runtimeclass.Catalog, error) {
	if !features.FeatureGate.Enabled(features.RuntimeClasses) {
		return nil, nil
	}
	return r.runtimeClassCatalog(ctx)
}
