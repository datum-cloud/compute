// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// CompanionGCReconciler runs on each cell cluster and deletes orphaned companion
// ConfigMaps and Secrets whose WorkloadDeployment referrers are gone from that
// cell. It is the authoritative GC path that does not depend on Karmada's
// ResourceBinding cascade completing correctly.
//
// The reconciler is triggered by WorkloadDeployment events. On each trigger it
// lists all companions in the WD's namespace (by the referenced-data=true
// label), parses the referenced-by annotation on each companion, and deletes
// any companion for which every listed WD name is absent from the local cell
// namespace.
//
// Per-cell multi-referrer safety: the referenced-by annotation is written by
// the hub-side ReferencedDataController using PROJECT-plane namespace keys
// (e.g. "default/mount-pristine-default-dfw"). On the cell the WD lives in
// ns-{project-uid}, not "default". hasLiveReferrer therefore looks up WDs by
// name only, in the companion's own namespace. This means:
//
//   - A WD on a different cell is never present locally → counted absent
//     → companion on that cell is correctly deleted when its own local WD is
//     also gone.
//   - A WD on this cell is present → companion preserved → correct.
type CompanionGCReconciler struct {
	mgr mcmanager.Manager
}

// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments,verbs=get;list;watch

// Reconcile is invoked for each companion ConfigMap or Secret that carries the
// referenced-data=true label. It deletes the companion when every WD listed in
// its referenced-by annotation is absent from the same namespace on this cell.
func (r *CompanionGCReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues(
		"namespace", req.Namespace,
		"name", req.Name,
	)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	ctx = mccontext.WithCluster(ctx, req.ClusterName)
	cellClient := cl.GetClient()

	return ctrl.Result{}, r.reconcileCompanion(ctx, cellClient, req.NamespacedName, logger)
}

// reconcileCompanion checks one ConfigMap-or-Secret companion and deletes it
// when safe. The function tries ConfigMap first; if not found it tries Secret.
func (r *CompanionGCReconciler) reconcileCompanion(
	ctx context.Context,
	cellClient client.Client,
	key types.NamespacedName,
	logger interface{ Info(string, ...any) },
) error {
	// Try ConfigMap.
	var cm corev1.ConfigMap
	err := cellClient.Get(ctx, key, &cm)
	if err == nil {
		return r.maybeDeleteConfigMap(ctx, cellClient, &cm, logger)
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get ConfigMap %s: %w", key, err)
	}

	// Not a ConfigMap — try Secret.
	var secret corev1.Secret
	err = cellClient.Get(ctx, key, &secret)
	if err == nil {
		return r.maybeDeleteSecret(ctx, cellClient, &secret, logger)
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get Secret %s: %w", key, err)
	}

	// Object already gone — nothing to do.
	return nil
}

func (r *CompanionGCReconciler) maybeDeleteConfigMap(
	ctx context.Context,
	cellClient client.Client,
	cm *corev1.ConfigMap,
	logger interface{ Info(string, ...any) },
) error {
	if !isCompanion(cm) {
		return nil
	}
	alive, err := r.hasLiveReferrer(ctx, cellClient, cm.Namespace, cm.Annotations)
	if err != nil {
		return err
	}
	if alive {
		return nil
	}
	logger.Info("deleting orphaned companion ConfigMap", "name", cm.Name, "namespace", cm.Namespace)
	if err := cellClient.Delete(ctx, cm); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete companion ConfigMap %s/%s: %w", cm.Namespace, cm.Name, err)
	}
	return nil
}

func (r *CompanionGCReconciler) maybeDeleteSecret(
	ctx context.Context,
	cellClient client.Client,
	secret *corev1.Secret,
	logger interface{ Info(string, ...any) },
) error {
	if !isCompanion(secret) {
		return nil
	}
	alive, err := r.hasLiveReferrer(ctx, cellClient, secret.Namespace, secret.Annotations)
	if err != nil {
		return err
	}
	if alive {
		return nil
	}
	logger.Info("deleting orphaned companion Secret", "name", secret.Name, "namespace", secret.Namespace)
	if err := cellClient.Delete(ctx, secret); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete companion Secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	return nil
}

// isCompanion reports whether the object carries the referenced-data=true label.
// The label is the authoritative signal that the companion was created by the
// referenced-data controller. Objects without it are not touched by GC.
func isCompanion(obj metav1.Object) bool {
	labels := obj.GetLabels()
	return labels[computev1alpha.ReferencedDataLabel] == computev1alpha.ReferencedDataLabelValue
}

// hasLiveReferrer returns true when at least one WD listed in the referenced-by
// annotation still exists in the companion's namespace on this cell.
//
// The annotation is written by the hub-side ReferencedDataController as
// "projectNamespace/wdName" (e.g. "default/mount-pristine-default-dfw"). On
// the cell the WD lives in ns-{project-uid}, not in the project namespace. To
// find it we look up by NAME ONLY in the companion's own namespace — the cell
// WD name is always equal to the project WD name (set by
// upsertDownstreamDeployment). This also gives us the correct per-cell
// semantics: a WD that runs on a different cell is never present locally, so it
// contributes nothing to liveness on this cell.
//
// A companion is considered still needed if any referrer is present in any
// state (including terminating) to avoid premature deletion during the WD
// teardown window.
//
// Returns (false, nil) when the annotation is absent or empty.
// Returns (true, nil) when at least one referrer is found.
// Returns (true, err) when the annotation is corrupt or an API call fails so
// that the caller does NOT delete the companion on transient faults.
func (r *CompanionGCReconciler) hasLiveReferrer(
	ctx context.Context,
	cellClient client.Client,
	companionNamespace string,
	annotations map[string]string,
) (bool, error) {
	wdKeys, err := decodeCompanionRefCount(annotations)
	if err != nil {
		// Corrupt annotation: treat as "has live referrer" to avoid accidental GC.
		return true, err
	}
	if len(wdKeys) == 0 {
		return false, nil
	}

	for _, key := range wdKeys {
		wdName := wdNameFromKey(key)
		if wdName == "" {
			// Malformed key: conservatively assume the referrer is alive.
			return true, nil
		}

		// Look up by name in the companion's namespace. The annotation carries the
		// PROJECT namespace as the key prefix, but the cell WD lives in
		// ns-{project-uid} — the same namespace the companion is in.
		var wd computev1alpha.WorkloadDeployment
		err := cellClient.Get(ctx, types.NamespacedName{Namespace: companionNamespace, Name: wdName}, &wd)
		if err == nil {
			// Referrer exists (any state — including terminating). Companion stays.
			return true, nil
		}
		if !apierrors.IsNotFound(err) {
			return true, fmt.Errorf("get WorkloadDeployment %s/%s: %w", companionNamespace, wdName, err)
		}
		// Not found — this referrer is gone; continue checking the rest.
	}

	// Every listed referrer is absent from this cell.
	return false, nil
}

// wdNameFromKey extracts the WD name from a "namespace/name" annotation key.
// The referenced-by annotation always uses "projectNamespace/wdName" format;
// we want only the name portion, which is the part after the last slash.
// Returns "" for an empty key (caller should treat as malformed).
func wdNameFromKey(key string) string {
	if key == "" {
		return ""
	}
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '/' {
			return key[i+1:]
		}
	}
	// No slash — the whole string is the name.
	return key
}

// decodeCompanionRefCount parses the companionRefCountAnnotation value into a
// slice of WD keys. It is a cell-local copy of the hub-side decodeRefCount so
// the GC reconciler does not share internal state with the
// ReferencedDataController. The annotation format is identical: a JSON array.
//
// Returns (nil, nil) when the annotation is absent or empty.
// Returns an error when the annotation value is present but cannot be parsed.
func decodeCompanionRefCount(annotations map[string]string) ([]string, error) {
	raw, ok := annotations[companionRefCountAnnotation]
	if !ok || raw == "" {
		return nil, nil
	}
	var keys []string
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return nil, fmt.Errorf("companion-gc: corrupt ref-count annotation %q: %w", raw, err)
	}
	return keys, nil
}

// SetupWithManager registers the CompanionGCReconciler with the multicluster
// manager. It should only be called when cell controllers are enabled
// (--enable-cell-controllers). The reconciler watches ConfigMaps (its primary
// For type) and uses WorkloadDeployment events to enqueue companion objects in
// the same namespace. Secret companions are discovered at reconcile time via
// Get, not through a separate For/Watches registration, because both kinds map
// to the same reconcile loop.
func (r *CompanionGCReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr

	return mcbuilder.ControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, mcbuilder.WithEngageWithLocalCluster(false)).
		// When a WorkloadDeployment changes (including deletion), re-enqueue all
		// companion ConfigMaps and Secrets in the same namespace so the GC
		// reconciler can decide whether they are still referenced.
		Watches(&computev1alpha.WorkloadDeployment{},
			func(_ multicluster.ClusterName, cl cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
				return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []mcreconcile.Request {
					return enqueueCompanionsForNamespace(ctx, cl.GetClient(), obj.GetNamespace())
				})
			},
			mcbuilder.WithEngageWithLocalCluster(false),
		).
		Named("companion-gc").
		Complete(r)
}

// enqueueCompanionsForNamespace returns mcreconcile.Requests for every companion
// ConfigMap and Secret in the given namespace. It is called from the WD watch
// handler so the GC reconciler is triggered whenever a WD changes in a namespace
// that may contain companions.
func enqueueCompanionsForNamespace(ctx context.Context, cellClient client.Client, namespace string) []mcreconcile.Request {
	companionLabel := client.MatchingLabels{
		computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
	}
	nsSelector := client.InNamespace(namespace)

	var reqs []mcreconcile.Request

	var cms corev1.ConfigMapList
	if err := cellClient.List(ctx, &cms, nsSelector, companionLabel); err == nil {
		for i := range cms.Items {
			reqs = append(reqs, mcreconcile.Request{
				Request: reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: cms.Items[i].Namespace,
						Name:      cms.Items[i].Name,
					},
				},
			})
		}
	}

	var secrets corev1.SecretList
	if err := cellClient.List(ctx, &secrets, nsSelector, companionLabel); err == nil {
		for i := range secrets.Items {
			reqs = append(reqs, mcreconcile.Request{
				Request: reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: secrets.Items[i].Namespace,
						Name:      secrets.Items[i].Name,
					},
				},
			})
		}
	}

	return reqs
}
