// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	karmadaworkv1alpha2 "github.com/karmada-io/api/work/v1alpha2"
)

const (
	// orphanRBSweepInterval is how often the periodic backstop fires to catch
	// ResourceBindings whose hub companion was deleted before the controller started.
	orphanRBSweepInterval = 5 * time.Minute

	// ppNameAnnotationKey is the annotation Karmada's binding-controller stamps on
	// every ResourceBinding to link it back to its governing PropagationPolicy.
	// Despite the "name" in the key string, Karmada stores this value in
	// metadata.annotations, NOT metadata.labels (labels carry only permanent-id
	// UUIDs). Reading from labels always returns "" and breaks scope filtering.
	ppNameAnnotationKey = "propagationpolicy.karmada.io/name"

	// cityPPPrefix is the prefix of PropagationPolicy names created by
	// propagationPolicyNameFor in workloaddeployment_federator.go. RBs whose
	// PP annotation starts with this prefix were created for referenced-data
	// companion propagation.
	cityPPPrefix = "city-"
)

// OrphanRBReconciler runs on the Karmada hub and deletes ResourceBindings
// whose hub companion (ConfigMap or Secret) no longer exists. It is the
// backstop sweep that handles Karmada's event-driven deletion gap: if the
// binding-controller misses the companion-deletion event (e.g. PP deleted
// before RB reconcile completes), the RB and its child Works are stranded.
// This reconciler detects and removes those stranded RBs so that Works are
// deleted and cell copies stop being re-created.
//
// # Tight scope
//
// Only ResourceBindings satisfying ALL three conditions are ever deleted:
//  1. Name ends with "-configmap" or "-secret" (Karmada's kind-suffix for
//     namespace-scoped ConfigMap/Secret RBs — WD RBs end in "-workloaddeployment").
//  2. The propagationpolicy.karmada.io/name annotation starts with "city-" (all
//     referenced-data PropagationPolicies use this prefix; Karmada stores this
//     in annotations, not labels).
//  3. The hub companion derived by stripping the kind suffix does NOT exist
//     in the same namespace (and has no deletionTimestamp — a terminating
//     companion means Karmada cascade is still in progress).
//
// This filter is necessary and sufficient to avoid touching WD RBs and any
// non-compute ResourceBindings that happen to share the hub namespace.
//
// # Cascade
//
// Deleting the RB triggers Karmada's binding-controller to remove all Work
// objects the RB owns. The execution-controller then removes the cell copies.
// Because the Work is gone there is nothing left to re-create the cell copy —
// the deletion is permanent.
type OrphanRBReconciler struct {
	// HubClient is a client pointed at the Karmada hub API server.
	HubClient client.Client
}

// +kubebuilder:rbac:groups=work.karmada.io,resources=resourcebindings,verbs=get;list;watch;delete

// Reconcile is triggered for each ResourceBinding that passes the predicate
// filter. It checks whether the RB is a stranded orphan and deletes it if so.
func (r *OrphanRBReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var rb karmadaworkv1alpha2.ResourceBinding
	if err := r.HubClient.Get(ctx, req.NamespacedName, &rb); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Belt-and-suspenders: re-apply the scope filter in Reconcile so that even
	// if the predicate is bypassed (e.g. during tests) only valid RBs are acted on.
	if !r.isInScope(&rb) {
		return ctrl.Result{}, nil
	}

	companionName, kind, ok := companionFromRBName(rb.Name)
	if !ok {
		return ctrl.Result{}, nil
	}

	orphaned, err := r.isOrphaned(ctx, rb.Namespace, companionName, kind)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !orphaned {
		return ctrl.Result{}, nil
	}

	logger.Info("deleting orphaned companion ResourceBinding",
		"name", rb.Name,
		"namespace", rb.Namespace,
		"companionName", companionName,
		"companionKind", kind,
	)
	if err := r.HubClient.Delete(ctx, &rb); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// isInScope returns true when the ResourceBinding falls within the tight scope
// of referenced-data companion RBs: city-PP annotation AND kind suffix.
func (r *OrphanRBReconciler) isInScope(rb *karmadaworkv1alpha2.ResourceBinding) bool {
	ppName := rb.Annotations[ppNameAnnotationKey]
	if !strings.HasPrefix(ppName, cityPPPrefix) {
		return false
	}
	name := rb.Name
	return strings.HasSuffix(name, "-configmap") || strings.HasSuffix(name, "-secret")
}

// companionFromRBName extracts the companion object name and kind from a
// ResourceBinding name. Karmada names namespace-scoped RBs as
// "{objectName}-{kindLowercase}", so:
//
//	"cm-pristine-configmap" → ("cm-pristine", "ConfigMap", true)
//	"secret-foo-secret"     → ("secret-foo",  "Secret",    true)
//	"wd-foo-workloaddeployment" → ("", "", false)  // not a companion RB
func companionFromRBName(rbName string) (companionName, kind string, ok bool) {
	switch {
	case strings.HasSuffix(rbName, "-configmap"):
		return strings.TrimSuffix(rbName, "-configmap"), kindConfigMap, true
	case strings.HasSuffix(rbName, "-secret"):
		return strings.TrimSuffix(rbName, "-secret"), kindSecret, true
	default:
		return "", "", false
	}
}

// isOrphaned returns true when the hub companion is fully absent. A companion
// with a deletionTimestamp is considered NOT orphaned — the deletion is in
// progress and Karmada's cascade (or Component 3's explicit RB delete) is
// expected to fire shortly. We do not interfere with in-progress deletions.
func (r *OrphanRBReconciler) isOrphaned(ctx context.Context, namespace, companionName, kind string) (bool, error) {
	nsn := types.NamespacedName{Namespace: namespace, Name: companionName}
	switch kind {
	case kindConfigMap:
		var cm corev1.ConfigMap
		err := r.HubClient.Get(ctx, nsn, &cm)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		// Companion exists (including if terminating) — not orphaned yet.
		return false, nil
	case kindSecret:
		var s corev1.Secret
		err := r.HubClient.Get(ctx, nsn, &s)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	default:
		return false, nil
	}
}

// SetupWithManager registers the OrphanRBReconciler with a regular ctrl.Manager
// pointed at the Karmada hub. It is called from SetupOrphanRBWithManager, not
// from the multicluster manager, because ResourceBindings live on the hub only.
//
// The predicate filter restricts the work queue to city-PP companion RBs
// (kind suffix + city- label prefix) before they ever reach Reconcile.
func (r *OrphanRBReconciler) SetupWithManager(mgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&karmadaworkv1alpha2.ResourceBinding{},
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				ppName := obj.GetAnnotations()[ppNameAnnotationKey]
				if !strings.HasPrefix(ppName, cityPPPrefix) {
					return false
				}
				name := obj.GetName()
				return strings.HasSuffix(name, "-configmap") || strings.HasSuffix(name, "-secret")
			})),
		).
		Named("orphan-rb").
		Complete(r)
}

// orphanRBPeriodicSweep is a manager.Runnable that periodically lists all in-
// scope companion ResourceBindings on the hub and calls the reconciler directly
// for each. This covers RBs that became orphaned before the controller started,
// when no new RB events are expected to fire.
//
// The sweep calls Reconcile directly (bypassing the controller's work queue)
// because the manager.Runnable interface is simpler than wiring a synthetic
// event source. The reconciler is idempotent, so direct calls are safe.
type orphanRBPeriodicSweep struct {
	hubClient  client.Client
	reconciler *OrphanRBReconciler
	interval   time.Duration
}

// Start runs the periodic sweep loop until ctx is cancelled.
func (s *orphanRBPeriodicSweep) Start(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

// sweep lists all ResourceBindings on the hub, filters to in-scope companion
// RBs, and calls Reconcile directly for each candidate.
func (s *orphanRBPeriodicSweep) sweep(ctx context.Context) {
	logger := log.FromContext(ctx).WithValues("component", "orphan-rb-periodic-sweep")

	var rbList karmadaworkv1alpha2.ResourceBindingList
	// List across all namespaces; label filtering is applied client-side because
	// a HasPrefix predicate is not expressible as a label selector. The total RB
	// count in the hub is bounded (O(companions × cells)), so a full list is safe.
	if err := s.hubClient.List(ctx, &rbList); err != nil {
		logger.Error(err, "periodic sweep: list ResourceBindings failed")
		return
	}

	for i := range rbList.Items {
		rb := &rbList.Items[i]
		if !s.reconciler.isInScope(rb) {
			continue
		}
		if _, err := s.reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{
				Namespace: rb.Namespace,
				Name:      rb.Name,
			},
		}); err != nil {
			logger.Error(err, "periodic sweep: reconcile failed",
				"name", rb.Name, "namespace", rb.Namespace)
		}
	}
}

// SetupOrphanRBWithManager wires the OrphanRBReconciler and its periodic sweep
// backstop onto the provided ctrl.Manager pointed at the Karmada hub. Called
// from setupManagementControllers in cmd/main.go.
func SetupOrphanRBWithManager(mgr manager.Manager, hubClient client.Client) error {
	r := &OrphanRBReconciler{HubClient: hubClient}
	if err := r.SetupWithManager(mgr); err != nil {
		return err
	}
	return mgr.Add(&orphanRBPeriodicSweep{
		hubClient:  hubClient,
		reconciler: r,
		interval:   orphanRBSweepInterval,
	})
}
