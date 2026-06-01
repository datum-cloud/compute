// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// companionGCPeriodicInterval is the frequency at which the backstop sweeper
// scans for companions in namespaces that may have no live WorkloadDeployments.
// In steady state orphans are covered immediately by WD-delete events; the
// periodic sweep is a backstop for namespaces whose last WD was deleted before
// this controller started (or between restarts).
const companionGCPeriodicInterval = 5 * time.Minute

// companionGCChannelBuffer is the number of events the backstop ticker channel
// can hold without blocking. Sized to accommodate one sweep's worth of
// namespace events across a typical cell (expected O(10s) of namespaces).
const companionGCChannelBuffer = 256

// CompanionGCReconciler runs on each cell cluster and deletes orphaned companion
// ConfigMaps and Secrets whose WorkloadDeployment referrers are gone from that
// cell. It is the authoritative GC path that does not depend on Karmada's
// ResourceBinding cascade completing correctly.
//
// The reconciler is triggered by WorkloadDeployment events (including deletion).
// On each trigger it lists all companions in the WD's namespace (by the
// referenced-data=true label), parses the referenced-by annotation on each
// companion, and deletes any companion for which every listed WD name is absent
// from the local cell namespace.
//
// # No cluster-wide ConfigMap/Secret informer
//
// The For type is WorkloadDeployment, not ConfigMap. WorkloadDeployments are
// already cached on the cell by the sibling WorkloadDeploymentReconciler, so
// this adds NO new cluster-wide informer. All companion reads (ConfigMap/Secret
// List) go through the UNCACHED APIReader. A one-shot List via APIReader does
// NOT establish a persistent informer, so it does not cause the OOM that
// For(ConfigMap) with an unscoped cache would.
//
// # Periodic backstop for WD-less namespaces
//
// A WD delete event covers the common case. Namespaces whose LAST WD was
// deleted before the controller started (or during a restart window) would
// never get a reconcile event. The companionGCBackstop Runnable fires
// namespace-keyed Reconcile requests on a ticker, independently of any WD
// object presence, to cover that gap.
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

// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments,verbs=get;list;watch

// Reconcile is invoked for each WorkloadDeployment event (including deletion)
// and by the periodic backstop for namespaces with no live WDs.
//
// It sweeps req.Namespace: any companion (ConfigMap or Secret carrying the
// referenced-data=true label) whose every listed WD referrer is absent from the
// local cell is deleted. req.Name is unused — the sweep covers all companions
// in the namespace regardless of which WD or backstop event triggered it.
//
// All companion reads use the uncached APIReader so no persistent CM/Secret
// informer is ever established. WD liveness checks use the cached client because
// WorkloadDeployments are already in the cell manager's cache.
func (r *CompanionGCReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("namespace", req.Namespace)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	ctx = mccontext.WithCluster(ctx, req.ClusterName)

	// Uncached reader for all ConfigMap/Secret reads — prevents establishing a
	// cluster-wide informer that would exhaust 128Mi on a cell cluster.
	apiReader := cl.GetAPIReader()
	// Cached client for WD liveness checks — WDs are already in the cell cache.
	cellClient := cl.GetClient()

	if err := r.sweepNamespace(ctx, apiReader, cellClient, req.Namespace, logger); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// sweepNamespace lists all companion ConfigMaps and Secrets in the given
// namespace (via the uncached apiReader) and deletes any whose every WD
// referrer is absent from the cell.
//
// Writes (Delete) always use cellClient because deletion does not require
// the object to be in the cache.
func (r *CompanionGCReconciler) sweepNamespace(
	ctx context.Context,
	apiReader client.Reader,
	cellClient client.Client,
	namespace string,
	logger interface{ Info(string, ...any) },
) error {
	companionLabel := client.MatchingLabels{
		computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
	}
	nsSelector := client.InNamespace(namespace)

	// List companion ConfigMaps via the uncached reader.
	var cms corev1.ConfigMapList
	if err := apiReader.List(ctx, &cms, nsSelector, companionLabel); err != nil {
		return fmt.Errorf("list companion ConfigMaps in %s: %w", namespace, err)
	}
	for i := range cms.Items {
		if err := r.maybeDeleteConfigMap(ctx, cellClient, &cms.Items[i], logger); err != nil {
			return err
		}
	}

	// List companion Secrets via the uncached reader.
	var secrets corev1.SecretList
	if err := apiReader.List(ctx, &secrets, nsSelector, companionLabel); err != nil {
		return fmt.Errorf("list companion Secrets in %s: %w", namespace, err)
	}
	for i := range secrets.Items {
		if err := r.maybeDeleteSecret(ctx, cellClient, &secrets.Items[i], logger); err != nil {
			return err
		}
	}

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
func isCompanion(obj interface{ GetLabels() map[string]string }) bool {
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
// WD reads use the CACHED cellClient because WorkloadDeployments are already
// held in the cell manager's cache (the sibling WorkloadDeploymentReconciler
// watches them). This is safe and cheap.
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

// ─── Periodic backstop ────────────────────────────────────────────────────────

// companionGCBackstop is an mcmanager.Runnable that fires periodic full-cluster
// companion sweeps, independent of any WorkloadDeployment event. It closes the
// coverage gap where a namespace's LAST WD was deleted before this controller
// started: the per-WD For() watch would never enqueue that namespace, so without
// a backstop those orphans would persist until the next controller restart.
//
// On each tick the backstop lists ALL companions (by label, ALL namespaces) via
// the uncached APIReader on each engaged cell cluster, collects the distinct
// namespaces, and sends one namespace-keyed event per namespace into ch. The
// controller's WatchesRawSource picks those events up and routes them to
// Reconcile via backstopEventHandler.
//
// A one-shot APIReader.List does NOT establish a persistent informer, so this
// never causes the OOM the original For(ConfigMap) design had.
type companionGCBackstop struct {
	ch chan event.GenericEvent

	mu       sync.Mutex
	clusters map[multicluster.ClusterName]cluster.Cluster
}

// Engage is called by the mcmanager coordinator when a cell cluster becomes
// active. We record the cluster so the ticker goroutine can include it in
// each sweep.
func (b *companionGCBackstop) Engage(_ context.Context, name multicluster.ClusterName, cl cluster.Cluster) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clusters[name] = cl
	return nil
}

// Start runs the periodic ticker loop. It blocks until ctx is cancelled.
func (b *companionGCBackstop) Start(ctx context.Context) error {
	ticker := time.NewTicker(companionGCPeriodicInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			b.sweep(ctx)
		}
	}
}

// sweep enumerates all engaged cell clusters and, for each, lists companion
// ConfigMaps and Secrets via the uncached APIReader across all namespaces. For
// each distinct namespace found it sends a GenericEvent into ch so the GC
// controller will sweep that namespace.
//
// Each event carries a *corev1.Namespace whose ObjectMeta.Namespace is the
// target sweep namespace and ObjectMeta.Name encodes the ClusterName. The
// backstopEventHandler lifts these into an mcreconcile.Request.
//
// Errors are logged and skipped: a failed sweep on one cluster does not block
// others, and the next tick will retry.
func (b *companionGCBackstop) sweep(ctx context.Context) {
	logger := log.FromContext(ctx).WithValues("component", "companion-gc-backstop")

	b.mu.Lock()
	clusters := make(map[multicluster.ClusterName]cluster.Cluster, len(b.clusters))
	for k, v := range b.clusters {
		clusters[k] = v
	}
	b.mu.Unlock()

	companionLabel := client.MatchingLabels{
		computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
	}

	for clusterName, cl := range clusters {
		apiReader := cl.GetAPIReader()
		namespaces := make(map[string]struct{})

		var cms corev1.ConfigMapList
		if err := apiReader.List(ctx, &cms, companionLabel); err != nil {
			logger.Error(err, "backstop: list companion ConfigMaps", "cluster", clusterName)
		} else {
			for i := range cms.Items {
				namespaces[cms.Items[i].Namespace] = struct{}{}
			}
		}

		var secrets corev1.SecretList
		if err := apiReader.List(ctx, &secrets, companionLabel); err != nil {
			logger.Error(err, "backstop: list companion Secrets", "cluster", clusterName)
		} else {
			for i := range secrets.Items {
				namespaces[secrets.Items[i].Namespace] = struct{}{}
			}
		}

		// Emit one GenericEvent per distinct namespace. The carrier object is a
		// *corev1.Namespace with Namespace=<target ns> and Name=<cluster name>.
		// backstopEventHandler lifts these fields into an mcreconcile.Request.
		for ns := range namespaces {
			obj := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					// Name carries the ClusterName so backstopEventHandler can
					// stamp it onto the mcreconcile.Request.
					Name: string(clusterName),
					// Namespace carries the target cell namespace to sweep.
					Namespace: ns,
				},
			}
			select {
			case b.ch <- event.GenericEvent{Object: obj}:
			case <-ctx.Done():
				return
			default:
				// Channel full — drop this namespace; the next tick will cover it.
				logger.V(1).Info("backstop: channel full, dropping namespace sweep event",
					"cluster", clusterName, "namespace", ns)
			}
		}
	}
}

// backstopEventHandler maps a backstop GenericEvent (carrier: *corev1.Namespace
// with Namespace=target sweep namespace, Name=ClusterName) into an
// mcreconcile.Request. It implements handler.TypedEventHandler[client.Object, mcreconcile.Request].
type backstopEventHandler struct{}

var _ handler.TypedEventHandler[client.Object, mcreconcile.Request] = backstopEventHandler{}

func (backstopEventHandler) Create(_ context.Context, _ event.TypedCreateEvent[client.Object], _ workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
}
func (backstopEventHandler) Update(_ context.Context, _ event.TypedUpdateEvent[client.Object], _ workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
}
func (backstopEventHandler) Delete(_ context.Context, _ event.TypedDeleteEvent[client.Object], _ workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
}
func (backstopEventHandler) Generic(_ context.Context, ev event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	obj := ev.Object
	q.Add(mcreconcile.Request{
		ClusterName: multicluster.ClusterName(obj.GetName()),
		Request: ctrl.Request{
			NamespacedName: types.NamespacedName{
				Namespace: obj.GetNamespace(),
				// Name is intentionally empty — Reconcile uses only req.Namespace.
			},
		},
	})
}

// SetupWithManager registers the CompanionGCReconciler with the multicluster
// manager. It should only be called when cell controllers are enabled
// (--enable-cell-controllers).
//
// The For type is WorkloadDeployment (NOT ConfigMap or Secret). WorkloadDeployments
// are already cached on the cell by the sibling WorkloadDeploymentReconciler, so
// this registration adds NO new cluster-wide informer. A WD delete event enqueues
// the WD's namespace/name and fires Reconcile — that is the deletion trigger.
//
// A companionGCBackstop Runnable is registered on the manager and wired via
// WatchesRawSource. On each tick it lists companions across all namespaces via
// the uncached APIReader and enqueues namespace-keyed reconcile requests. This
// covers namespaces whose last WD was deleted before the controller started.
//
// All companion reads inside Reconcile and the backstop go through the UNCACHED
// APIReader so that no persistent ConfigMap/Secret informer is ever established.
func (r *CompanionGCReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.mgr = mgr

	// backstopCh carries namespace-keyed GenericEvents from companionGCBackstop
	// to the controller's WatchesRawSource. Typed as event.GenericEvent (alias for
	// TypedGenericEvent[client.Object]) so source.TypedChannel can consume it.
	backstopCh := make(chan event.GenericEvent, companionGCChannelBuffer)

	backstop := &companionGCBackstop{
		ch:       backstopCh,
		clusters: make(map[multicluster.ClusterName]cluster.Cluster),
	}
	if err := mgr.Add(backstop); err != nil {
		return fmt.Errorf("companion-gc: add backstop runnable: %w", err)
	}

	return mcbuilder.ControllerManagedBy(mgr).
		// Drive GC off WorkloadDeployment events. WDs are already in the cell cache;
		// this adds no new cluster-wide informer. A WD deletion still triggers
		// Reconcile so we sweep the namespace immediately after the WD disappears.
		For(&computev1alpha.WorkloadDeployment{}, mcbuilder.WithEngageWithLocalCluster(false)).
		// Backstop: periodic namespace-keyed events from companionGCBackstop.
		// TypedChannel[client.Object, mcreconcile.Request] produces a
		// TypedSource[mcreconcile.Request] as required by WatchesRawSource.
		WatchesRawSource(source.TypedChannel(
			backstopCh,
			backstopEventHandler{},
		)).
		Named("companion-gc").
		Complete(r)
}
