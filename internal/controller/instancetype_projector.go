// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
)

const (
	// instanceTypesPropagationPolicyName is the single cluster-scoped propagation
	// policy that carries every catalog InstanceType to every cell. There is one
	// such policy per hub, not per type, because the catalog is global: every
	// catalog copy on the hub must reach every cell.
	instanceTypesPropagationPolicyName = "instance-types"

	// instanceTypeCatalogKey is the only work-queue key the projector uses. The
	// catalog is reconciled as a whole, because pruning a hub copy needs the full
	// set of types the catalog still declares.
	instanceTypeCatalogKey = "instance-type-catalog"

	kindInstanceType = "InstanceType"
)

// InstanceTypeCatalog reads the platform's instance type catalog from its
// source of truth.
type InstanceTypeCatalog interface {
	// Object returns an empty object of the kind the catalog is read from. The
	// projector watches that kind on the catalog cluster.
	Object() client.Object

	// Load returns the catalog's InstanceTypes. found is false when the catalog
	// source does not exist, which the projector treats as "unknown" rather than
	// "empty": it leaves the hub copies as they are instead of pruning every
	// tier off every cell.
	Load(ctx context.Context, c client.Reader) (catalog []computev1alpha.InstanceType, found bool, err error)
}

// ServiceConfigurationCatalog reads the catalog from the InstanceType objects
// the compute ServiceConfiguration provisions into every entitled project. That
// declaration is the one place the catalog is written; each project's copy is
// installed from it, so reading it here gives the hub a single writer no matter
// how many projects hold the catalog.
type ServiceConfigurationCatalog struct {
	// ServiceNames are the canonical service names this operator owns, the same
	// discovery.consumerScopedProjection.serviceNames that scope project
	// engagement. A ServiceConfiguration contributes to the catalog when its
	// resolved status.serviceName is one of them.
	ServiceNames []string
}

// +kubebuilder:rbac:groups=services.miloapis.com,resources=serviceconfigurations,verbs=get;list;watch

func (c ServiceConfigurationCatalog) Object() client.Object {
	return &servicesv1alpha1.ServiceConfiguration{}
}

func (c ServiceConfigurationCatalog) Load(ctx context.Context, reader client.Reader) ([]computev1alpha.InstanceType, bool, error) {
	var configs servicesv1alpha1.ServiceConfigurationList
	if err := reader.List(ctx, &configs); err != nil {
		return nil, false, fmt.Errorf("failed listing ServiceConfigurations: %w", err)
	}
	// List order is not guaranteed; sort so a name declared by two
	// configurations always resolves to the same one.
	sort.Slice(configs.Items, func(i, j int) bool { return configs.Items[i].Name < configs.Items[j].Name })

	logger := log.FromContext(ctx)
	found := false
	seen := map[string]string{}
	var catalog []computev1alpha.InstanceType
	for i := range configs.Items {
		config := &configs.Items[i]
		if !slices.Contains(c.ServiceNames, config.Status.ServiceName) {
			continue
		}
		// Draft configurations are not fanned out to projects, so their types
		// are not part of the catalog either.
		if config.Spec.Phase == servicesv1alpha1.PhaseDraft {
			continue
		}
		found = true
		if config.Spec.Provisioning == nil {
			continue
		}
		for _, resource := range config.Spec.Provisioning.Resources {
			for _, object := range resource.Objects {
				instanceType, ok, err := decodeProvisionedInstanceType(object.Raw)
				if err != nil {
					return nil, false, fmt.Errorf("ServiceConfiguration %q provisioning resource %q: %w", config.Name, resource.Name, err)
				}
				if !ok {
					continue
				}
				if owner, dup := seen[instanceType.Name]; dup {
					logger.Info("ignoring duplicate catalog InstanceType", "instanceType", instanceType.Name,
						"serviceConfiguration", config.Name, "keptFrom", owner)
					continue
				}
				seen[instanceType.Name] = config.Name
				catalog = append(catalog, *instanceType)
			}
		}
	}
	return catalog, found, nil
}

// decodeProvisionedInstanceType decodes a provisioned object, reporting false
// for objects of any other kind.
func decodeProvisionedInstanceType(raw []byte) (*computev1alpha.InstanceType, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	var typeMeta metav1.TypeMeta
	if err := json.Unmarshal(raw, &typeMeta); err != nil {
		return nil, false, fmt.Errorf("failed decoding provisioned object: %w", err)
	}
	if typeMeta.APIVersion != computev1alpha.GroupVersion.String() || typeMeta.Kind != kindInstanceType {
		return nil, false, nil
	}
	var instanceType computev1alpha.InstanceType
	if err := json.Unmarshal(raw, &instanceType); err != nil {
		return nil, false, fmt.Errorf("failed decoding provisioned InstanceType: %w", err)
	}
	return &instanceType, true, nil
}

// ClusterCatalog reads the catalog from the InstanceTypes on the catalog
// cluster itself. It backs single-cluster mode, where the one cluster the
// operator manages is the only place types are defined, so its InstanceTypes
// are the catalog.
type ClusterCatalog struct{}

func (ClusterCatalog) Object() client.Object {
	return &computev1alpha.InstanceType{}
}

func (ClusterCatalog) Load(ctx context.Context, reader client.Reader) ([]computev1alpha.InstanceType, bool, error) {
	var list computev1alpha.InstanceTypeList
	if err := reader.List(ctx, &list); err != nil {
		return nil, false, fmt.Errorf("failed listing InstanceTypes: %w", err)
	}
	catalog := make([]computev1alpha.InstanceType, 0, len(list.Items))
	for _, instanceType := range list.Items {
		if instanceType.DeletionTimestamp.IsZero() {
			catalog = append(catalog, instanceType)
		}
	}
	return catalog, true, nil
}

// InstanceTypeProjector publishes the platform's instance type catalog to the
// federation hub so it can be propagated to every POP-cell cluster. Cells read
// instance types but do not maintain them; the provider resolves a workload's
// selected type against the catalog on its own cluster.
//
// The catalog is read from its single source of truth (see
// InstanceTypeCatalog), never from the per-project copies, so the hub has one
// writer: a tier's spec cannot flip between versions while projects pick up a
// change, and nothing on the hub grows with the number of projects.
//
// The controller reconciles the catalog as a whole:
//  1. Upserts a cluster-scoped hub copy of every catalog type, carrying its spec
//     and labels plus InstanceTypeCatalogLabel.
//  2. Deletes labelled hub copies the catalog no longer declares, so cells drop
//     the tier.
//  3. Ensures a single ClusterPropagationPolicy selects the labelled copies and
//     targets every cell (empty placement).
//
// It watches the catalog source and, through the federation cluster's cache,
// the hub copies and the policy, so drift on the hub is repaired immediately.
// The controller is registered on the leader-elected local manager so only the
// elected replica writes to the hub.
type InstanceTypeProjector struct {
	// FederationClient is a client pointed at the Karmada federation control
	// plane, where hub copies and the ClusterPropagationPolicy live.
	FederationClient client.Client

	// CatalogClient reads the catalog source. It is the catalog cluster's
	// cached client.
	CatalogClient client.Reader

	// Catalog reads the catalog from CatalogClient.
	Catalog InstanceTypeCatalog
}

func (r *InstanceTypeProjector) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	catalog, found, err := r.Catalog.Load(ctx, r.CatalogClient)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		logger.Info("instance type catalog source not found; leaving hub copies untouched")
		return ctrl.Result{}, nil
	}

	declared := make(map[string]struct{}, len(catalog))
	for i := range catalog {
		declared[catalog[i].Name] = struct{}{}
		if err := r.reconcileHubCopy(ctx, &catalog[i]); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.pruneHubCopies(ctx, declared); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensurePropagationPolicy(ctx); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *InstanceTypeProjector) reconcileHubCopy(ctx context.Context, instanceType *computev1alpha.InstanceType) error {
	hub := &computev1alpha.InstanceType{
		ObjectMeta: metav1.ObjectMeta{Name: instanceType.Name},
	}
	// CreateOrUpdate writes with the resourceVersion it read, so a concurrent
	// change to the hub copy fails with a conflict and is retried, rather than
	// being overwritten.
	result, err := controllerutil.CreateOrUpdate(ctx, r.FederationClient, hub, func() error {
		if hub.Labels == nil {
			hub.Labels = make(map[string]string)
		}
		for k, v := range instanceType.Labels {
			hub.Labels[k] = v
		}
		hub.Labels[computev1alpha.InstanceTypeCatalogLabel] = computev1alpha.InstanceTypeCatalogLabelValue
		hub.Spec = instanceType.Spec
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed upserting hub instance type %q: %w", instanceType.Name, err)
	}

	if result != controllerutil.OperationResultNone {
		log.FromContext(ctx).Info("reconciled hub instance type", "instanceType", instanceType.Name, "operation", result)
	}
	return nil
}

// pruneHubCopies deletes the catalog copies on the hub that the catalog no
// longer declares. Only labelled copies are considered, so an InstanceType the
// projector did not write is never removed.
func (r *InstanceTypeProjector) pruneHubCopies(ctx context.Context, declared map[string]struct{}) error {
	var hubCopies computev1alpha.InstanceTypeList
	if err := r.FederationClient.List(ctx, &hubCopies, client.MatchingLabels{
		computev1alpha.InstanceTypeCatalogLabel: computev1alpha.InstanceTypeCatalogLabelValue,
	}); err != nil {
		return fmt.Errorf("failed listing hub instance types: %w", err)
	}

	for i := range hubCopies.Items {
		hub := &hubCopies.Items[i]
		if _, ok := declared[hub.Name]; ok || !hub.DeletionTimestamp.IsZero() {
			continue
		}
		// The preconditions make the delete apply only to the copy that was
		// read, so a copy recreated or updated in between is left alone and
		// re-evaluated on the next reconcile.
		if err := r.FederationClient.Delete(ctx, hub, client.Preconditions{
			UID:             &hub.UID,
			ResourceVersion: &hub.ResourceVersion,
		}); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed deleting hub instance type %q: %w", hub.Name, err)
		}
		log.FromContext(ctx).Info("deleted hub instance type no longer in the catalog", "instanceType", hub.Name)
	}
	return nil
}

func (r *InstanceTypeProjector) ensurePropagationPolicy(ctx context.Context) error {
	policy := &karmadapolicyv1alpha1.ClusterPropagationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: instanceTypesPropagationPolicyName},
	}
	result, err := controllerutil.CreateOrUpdate(ctx, r.FederationClient, policy, func() error {
		policy.Spec = karmadapolicyv1alpha1.PropagationSpec{
			// Compute owns every catalog copy, so a name collision on a cell must
			// not stall delivery.
			ConflictResolution: karmadapolicyv1alpha1.ConflictOverwrite,
			ResourceSelectors: []karmadapolicyv1alpha1.ResourceSelector{
				{
					APIVersion: computev1alpha.GroupVersion.String(),
					Kind:       kindInstanceType,
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							computev1alpha.InstanceTypeCatalogLabel: computev1alpha.InstanceTypeCatalogLabelValue,
						},
					},
				},
			},
			// Empty placement: Karmada targets every member cluster.
			Placement: karmadapolicyv1alpha1.Placement{},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed upserting ClusterPropagationPolicy %q: %w", instanceTypesPropagationPolicyName, err)
	}

	if result != controllerutil.OperationResultNone {
		log.FromContext(ctx).Info("reconciled ClusterPropagationPolicy", "policy", instanceTypesPropagationPolicyName, "operation", result)
	}
	return nil
}

// SetupWithManager registers the InstanceTypeProjector on mgr, which must be the
// leader-elected local manager. It watches the catalog source through
// catalogCluster's cache, and the hub copies and the propagation policy through
// federationCluster's cache. Every event maps to the single catalog key.
func (r *InstanceTypeProjector) SetupWithManager(mgr manager.Manager, catalogCluster, federationCluster cluster.Cluster) error {
	if r.FederationClient == nil {
		return fmt.Errorf("instance type projector requires a federation client")
	}
	if r.Catalog == nil {
		return fmt.Errorf("instance type projector requires a catalog")
	}
	if r.CatalogClient == nil {
		r.CatalogClient = catalogCluster.GetClient()
	}

	// None of the watched objects live in mgr's own cluster, so each watch is a
	// raw source on another cluster's cache rather than For or Watches.
	return ctrl.NewControllerManagedBy(mgr).
		Named("instance-type-projector").
		// The catalog source: the ServiceConfiguration on the Milo root, or the
		// InstanceTypes on the one cluster in single mode. This is what makes
		// adding, changing, or removing a type reach the hub without a restart.
		WatchesRawSource(source.Kind(
			catalogCluster.GetCache(),
			r.Catalog.Object(),
			enqueueInstanceTypeCatalog[client.Object](),
		)).
		// The hub copies. An edited or deleted copy is restored right away
		// instead of on the next catalog change or resync; until then cells
		// would size instances from the providers' hardcoded fallback.
		WatchesRawSource(source.Kind(
			federationCluster.GetCache(),
			&computev1alpha.InstanceType{},
			enqueueInstanceTypeCatalog[*computev1alpha.InstanceType](),
		)).
		// The instance-types propagation policy, for the same reason: an
		// edited or deleted policy would stop the catalog reaching new cells.
		WatchesRawSource(source.Kind(
			federationCluster.GetCache(),
			&karmadapolicyv1alpha1.ClusterPropagationPolicy{},
			enqueueInstanceTypeCatalog[*karmadapolicyv1alpha1.ClusterPropagationPolicy](),
			predicate.NewTypedPredicateFuncs(func(policy *karmadapolicyv1alpha1.ClusterPropagationPolicy) bool {
				return policy.Name == instanceTypesPropagationPolicyName
			}),
		)).
		Complete(r)
}

// enqueueInstanceTypeCatalog maps every event to the single catalog key.
func enqueueInstanceTypeCatalog[T client.Object]() handler.TypedEventHandler[T, reconcile.Request] {
	return handler.TypedEnqueueRequestsFromMapFunc(func(context.Context, T) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: instanceTypeCatalogKey}}}
	})
}
