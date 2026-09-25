package webhook

import (
	"context"
	"fmt"

	admissionv1 "k8s.io/api/admission/v1"
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
	"go.datum.net/compute/pkg/instancetypecatalog"
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

// readyLocations describes the locations a placement may run at: the
// locations projected into the project control plane that are Ready and where
// compute is available, as the names a placement may list and the topology a
// selector is matched against.
type readyLocations struct {
	names      []string
	topologies map[string]map[string]string
}

func (r *workloadWebhook) readyLocations(ctx context.Context, c client.Client) (readyLocations, error) {
	placementLocations, err := locations.ListPlacementLocations(ctx, c, r.locationSource)
	if err != nil {
		return readyLocations{}, err
	}

	ready := readyLocations{
		names:      sets.List(locations.PlaceableNames(placementLocations)),
		topologies: make(map[string]map[string]string),
	}
	for _, location := range placementLocations {
		if location.Placeable() {
			ready.topologies[location.Name] = location.Topology
		}
	}
	return ready, nil
}

var _ admission.Defaulter[*computev1alpha.Workload] = &workloadWebhook{}
var _ admission.Validator[*computev1alpha.Workload] = &workloadWebhook{}

// Default implements admission.Defaulter so a mutating webhook will be registered for the type.
func (r *workloadWebhook) Default(ctx context.Context, workload *computev1alpha.Workload) error {
	// A manifest written before placement moved to locations still names
	// city codes. It is rewritten here so what is stored is what the
	// controller places by, and so the stored object never carries the
	// deprecated field.
	workload.MigrateCityCodes()

	// With the gate off there is only one runtime class, so the field stays
	// empty rather than recording a class name the platform does not yet honor.
	if features.FeatureGate.Enabled(features.RuntimeClasses) {
		catalog, err := r.runtimeClassCatalog(ctx)
		if err != nil {
			return err
		}
		defaultFromCatalog(ctx, workload, catalog)
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
	ready, err := r.readyLocations(ctx, clusterClient)
	if err != nil {
		return nil, err
	}

	runtimeClasses, err := r.runtimeClassCatalogWhenEnabled(ctx)
	if err != nil {
		return nil, err
	}

	instanceTypes, err := r.instanceTypeCatalogWhenEnabled(ctx)
	if err != nil {
		return nil, err
	}

	opts := validation.WorkloadValidationOptions{
		Context:            ctx,
		Client:             clusterClient,
		AdmissionRequest:   req,
		Workload:           workload,
		ValidLocations:     ready.names,
		LocationTopologies: ready.topologies,
		RuntimeClasses:     runtimeClasses,
		InstanceTypes:      instanceTypes,
	}

	if errs := validation.ValidateWorkloadCreate(workload, opts); len(errs) > 0 {
		return nil, errors.NewInvalid(workload.GroupVersionKind().GroupKind(), workload.Name, errs)
	}

	return workloadInstanceTypeWarnings(true, workload, instanceTypes), nil
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

	ready, err := r.readyLocations(ctx, clusterClient)
	if err != nil {
		return nil, err
	}

	runtimeClasses, err := r.runtimeClassCatalogWhenEnabled(ctx)
	if err != nil {
		return nil, err
	}

	instanceTypes, err := r.instanceTypeCatalogWhenEnabled(ctx)
	if err != nil {
		return nil, err
	}

	opts := validation.WorkloadValidationOptions{
		Context:            ctx,
		Client:             clusterClient,
		AdmissionRequest:   req,
		Workload:           newWorkload,
		ValidLocations:     ready.names,
		LocationTopologies: ready.topologies,
		RuntimeClasses:     runtimeClasses,
		InstanceTypes:      instanceTypes,
	}

	if errs := validation.ValidateWorkloadUpdate(newWorkload, oldWorkload, opts); len(errs) > 0 {
		return nil, errors.NewInvalid(newWorkload.GroupVersionKind().GroupKind(), newWorkload.Name, errs)
	}

	return workloadInstanceTypeWarnings(false, newWorkload, instanceTypes), nil
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

// defaultFromCatalog applies the defaults drawn from the published catalog.
//
// The security context is stamped only when the workload is created. Defaulting
// runs on every update, so filling each empty field from the catalog on the way
// past would let an unrelated edit, such as a replica change, move a running
// container onto whatever the class publishes today. The stored security context
// also takes part in the instance template hash, so that edit would recreate
// every instance in the workload.
//
// An operation this cannot determine is treated as an update, because stamping
// privilege onto an object of unknown provenance is the worse failure. The class
// itself is still resolved on update, which is how a workload stored before the
// catalog existed acquires one.
func defaultFromCatalog(ctx context.Context, workload *computev1alpha.Workload, catalog runtimeclass.Catalog) {
	defaultRuntimeClass(workload, catalog)

	request, err := admission.RequestFromContext(ctx)
	if err != nil || request.Operation != admissionv1.Create {
		return
	}
	defaultSecurityContext(workload, catalog)
}

// defaultSecurityContext writes the selected class's published security context
// onto every sandbox container that states none, so the stored workload shows
// exactly what its containers run with.
//
// The platform picks a security configuration for every container either way.
// Leaving that pick unwritten is what turns a container the configuration stops
// from starting into an unexplained crash loop, because the only evidence is the
// container's own logs. Storing the pick lets a customer read it with the rest of
// their workload, and lets a provider run what the workload states rather than a
// configuration of its own.
//
// Because the value is stored and written only at creation, a later correction
// to the class moves workloads created after the change and leaves ones already
// stored as they are.
func defaultSecurityContext(workload *computev1alpha.Workload, catalog runtimeclass.Catalog) {
	sandbox := workload.Spec.Template.Spec.Runtime.Sandbox
	if sandbox == nil {
		return
	}

	class := catalog.Find(workload.Spec.Template.Spec.Runtime.Class)
	if class == nil {
		return
	}

	for i := range sandbox.Containers {
		sandbox.Containers[i].SecurityContext = runtimeclass.DefaultSecurityContext(
			class, sandbox.Containers[i].SecurityContext)
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

// instanceTypeCatalog lists the instance types published to the control plane
// the request is admitted into. The platform projects the catalog read-only
// into every project control plane, so validation uses the same catalog the
// customer reads.
//
// A read failure rejects the request. Storing a workload that selects an
// instance type no provider runs surfaces later as a workload that is never
// placed.
func (r *workloadWebhook) instanceTypeCatalog(ctx context.Context) (instancetypecatalog.Catalog, error) {
	clusterClient, err := r.clusterClient(ctx)
	if err != nil {
		return nil, err
	}
	return instanceTypeCatalog(ctx, clusterClient)
}

func instanceTypeCatalog(ctx context.Context, clusterClient client.Client) (instancetypecatalog.Catalog, error) {
	var types computev1alpha.InstanceTypeList
	if err := clusterClient.List(ctx, &types); err != nil {
		return nil, fmt.Errorf("failed to list instance types: %w", err)
	}

	return types.Items, nil
}

// instanceTypeCatalogWhenEnabled lists the catalog only when instance type
// selection is enabled. With the gate off, a control plane that has never
// published a type admits workloads as before.
func (r *workloadWebhook) instanceTypeCatalogWhenEnabled(ctx context.Context) (instancetypecatalog.Catalog, error) {
	if !features.FeatureGate.Enabled(features.InstanceTypes) {
		return nil, nil
	}
	return r.instanceTypeCatalog(ctx)
}

// workloadInstanceTypeWarnings returns the admission warnings that accompany an
// accepted workload selecting an instance type. Validation rejects before
// warnings are collected, so every warning here accompanies a workload that is
// stored.
//
// The catalog is empty when selection is disabled or nothing is published, in
// which case there is nothing to warn about.
func workloadInstanceTypeWarnings(isCreate bool, workload *computev1alpha.Workload, catalog instancetypecatalog.Catalog) admission.Warnings {
	if len(catalog) == 0 {
		return nil
	}

	selected := workload.Spec.Template.Spec.Runtime.Resources.InstanceType
	if len(selected) == 0 {
		// No type means the platform's hardcoded fallback applies. The fallback
		// is a migration convenience, not a published tier, so flag it on create
		// so the author can name a real one.
		if isCreate {
			return admission.Warnings{
				"no instance type selected; the workload will run on the platform's default instance type",
			}
		}
		return nil
	}

	t := catalog.Find(selected)
	if t == nil {
		// Unknown types are rejected before warnings surface.
		return nil
	}

	if t.Spec.Lifecycle.Phase != computev1alpha.InstanceTypePhaseDeprecated {
		return nil
	}

	return admission.Warnings{instancetypecatalog.DeprecatedMessage(selected, t.Spec.Lifecycle.ReplacementInstanceType)}
}
