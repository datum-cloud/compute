// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	// companionGCSweepInterval is how often the periodic backstop fires to catch
	// companions stranded before the controller started.
	companionGCSweepInterval = 5 * time.Minute
)

// CompanionGCReconciler is a hub-side level-triggered garbage collector for
// referenced-data companion ConfigMaps and Secrets. It is the backstop for the
// interrupted-finalization failure mode: if the ReferencedDataController's WD
// finalizer is interrupted mid-flight (e.g. pod restart), a companion can be
// left with a referenced-by annotation pointing at WDs that no longer exist.
//
// On each reconcile, the controller:
//  1. Reads the companion's referenced-by annotation (JSON array of "ns/name" WD keys).
//  2. Checks each referenced WD for existence in the hub namespace via the
//     uncached HubClient. WDs live in the same hub namespace as the companion
//     (ns-{project-uid}), federated there by the WorkloadDeploymentFederator.
//     The WD name is parsed from the "ns/name" key (the name portion only —
//     the namespace in the key is the project namespace, not the hub namespace).
//  3. If ALL referrers are absent in the hub, the companion is stranded →
//     deletes it and its Karmada ResourceBinding via the downstreamCompanionWriter
//     path. Deleting the RB triggers Karmada's binding-controller to remove the
//     Work, which drives the execution-controller to remove the cell copy permanently.
//
// Safety invariant: if ANY referrer WD exists in the hub (including terminating),
// the companion is preserved. Terminating WDs count as present because the
// ReferencedDataController's finalizer may still complete teardown.
//
// Watch scope / OOM guard: the federationMgr cache for ConfigMaps and Secrets
// is restricted to objects carrying the ReferencedDataLabel via
// cache.Options.ByObject in cmd/main.go (setupManagementControllers). This is
// the actual OOM guard — predicates filter events, not cache contents; without
// the cache-level label scope the informer would list-and-watch every
// ConfigMap/Secret on the Karmada hub (the same unscoped-informer pattern that
// OOMKilled the cell CompanionGCReconciler). The label predicate on this
// controller is kept as belt-and-suspenders against predicate bypass but is NOT
// the primary memory guard.
//
// WD existence check: uses HubClient.Get (cache-backed). The federation manager
// cache has no ByObject restriction on WorkloadDeployments, so the WD cache
// covers all WDs in ns-{project-uid} namespaces. The check is purely for WD
// absence; normal cache lag cannot produce a false-orphan decision because a
// recently-deleted WD that is still in the cache is treated as present
// (conservative) and will be re-checked on the next reconcile or sweep tick.
type CompanionGCReconciler struct {
	// HubClient is a client pointed at the Karmada hub API server. Used to
	// read companions, check WD existence, delete companions, and delete RBs.
	HubClient client.Client
}

// Reconcile is triggered for each labeled companion (ConfigMap or Secret) that
// passes the predicate filter. It checks whether the companion is stranded and
// deletes it (plus its RB) if all referrers are absent from the hub.
func (r *CompanionGCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Determine whether this is a ConfigMap or Secret by probing both.
	// The controller watches both resource types; req carries no kind info.
	companionKind, err := r.resolveKind(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if companionKind == "" {
		// Object is already gone — nothing to do.
		return ctrl.Result{}, nil
	}

	// Belt-and-suspenders: re-verify the referenced-data label is present even
	// when Reconcile is called directly (bypassing the predicate). This prevents
	// any non-companion from being touched regardless of annotation content.
	if !r.isCompanion(ctx, req.NamespacedName, companionKind) {
		return ctrl.Result{}, nil
	}

	refKeys, skip, err := r.readRefCountKeys(ctx, req.NamespacedName, companionKind)
	if err != nil {
		return ctrl.Result{}, err
	}
	if skip {
		// Absent annotation, empty annotation, or corrupt annotation — not a
		// managed companion or conservatively skip.
		return ctrl.Result{}, nil
	}

	allGone, err := r.allReferrersAbsent(ctx, req.Namespace, refKeys)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !allGone {
		return ctrl.Result{}, nil
	}

	// All referrer WDs are absent from the hub — companion is stranded.
	logger.Info("deleting stranded companion",
		"namespace", req.Namespace,
		"name", req.Name,
		"kind", companionKind,
	)

	writer := &downstreamCompanionWriter{
		hubClient:           r.HubClient,
		downstreamNamespace: req.Namespace,
	}

	switch companionKind {
	case kindConfigMap:
		if err := writer.DeleteConfigMap(ctx, req.Namespace, req.Name); err != nil {
			return ctrl.Result{}, fmt.Errorf("companion-gc: delete ConfigMap %q: %w", req.Name, err)
		}
		if err := writer.DeleteResourceBinding(ctx, req.Namespace, req.Name+"-configmap"); err != nil {
			return ctrl.Result{}, fmt.Errorf("companion-gc: delete ConfigMap RB %q: %w", req.Name, err)
		}
	case kindSecret:
		if err := writer.DeleteSecret(ctx, req.Namespace, req.Name); err != nil {
			return ctrl.Result{}, fmt.Errorf("companion-gc: delete Secret %q: %w", req.Name, err)
		}
		if err := writer.DeleteResourceBinding(ctx, req.Namespace, req.Name+"-secret"); err != nil {
			return ctrl.Result{}, fmt.Errorf("companion-gc: delete Secret RB %q: %w", req.Name, err)
		}
	}

	return ctrl.Result{}, nil
}

// isCompanion returns true when the object at nsn/kind carries the
// referenced-data label. Used as a belt-and-suspenders guard inside Reconcile
// to prevent touching objects that bypassed the predicate filter.
func (r *CompanionGCReconciler) isCompanion(ctx context.Context, nsn types.NamespacedName, kind string) bool {
	switch kind {
	case kindConfigMap:
		var cm corev1.ConfigMap
		if err := r.HubClient.Get(ctx, nsn, &cm); err != nil {
			return false
		}
		return cm.Labels[computev1alpha.ReferencedDataLabel] == computev1alpha.ReferencedDataLabelValue
	case kindSecret:
		var s corev1.Secret
		if err := r.HubClient.Get(ctx, nsn, &s); err != nil {
			return false
		}
		return s.Labels[computev1alpha.ReferencedDataLabel] == computev1alpha.ReferencedDataLabelValue
	}
	return false
}

// resolveKind returns kindConfigMap or kindSecret depending on which resource
// exists at the given namespace/name. Returns ("", nil) when neither exists.
func (r *CompanionGCReconciler) resolveKind(ctx context.Context, nsn types.NamespacedName) (string, error) {
	var cm corev1.ConfigMap
	switch err := r.HubClient.Get(ctx, nsn, &cm); {
	case err == nil:
		return kindConfigMap, nil
	case !apierrors.IsNotFound(err):
		return "", err
	}

	var s corev1.Secret
	switch err := r.HubClient.Get(ctx, nsn, &s); {
	case err == nil:
		return kindSecret, nil
	case !apierrors.IsNotFound(err):
		return "", err
	}

	return "", nil
}

// readRefCountKeys returns the WD key slice from the companion's referenced-by
// annotation.
//
// Returns (nil, true, nil) — skip=true — in the following cases:
//   - Annotation is absent or empty: the object was not created by
//     ReferencedDataController (or was created before the annotation existed).
//   - Annotation is present but unparseable: corrupt state, conservatively
//     preserve the companion to avoid incorrectly deleting a still-in-use object.
//
// Returns (keys, false, nil) — skip=false — when the annotation is valid.
func (r *CompanionGCReconciler) readRefCountKeys(
	ctx context.Context,
	nsn types.NamespacedName,
	kind string,
) (keys []string, skip bool, err error) {
	var annotations map[string]string

	switch kind {
	case kindConfigMap:
		var cm corev1.ConfigMap
		if err := r.HubClient.Get(ctx, nsn, &cm); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, true, nil
			}
			return nil, false, err
		}
		annotations = cm.Annotations
	case kindSecret:
		var s corev1.Secret
		if err := r.HubClient.Get(ctx, nsn, &s); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, true, nil
			}
			return nil, false, err
		}
		annotations = s.Annotations
	}

	// decodeRefCount returns (nil, nil) for absent/empty annotation and
	// (nil, err) for corrupt annotation — both map to skip=true.
	decoded, decodeErr := decodeRefCount(annotations)
	if decodeErr != nil {
		log.FromContext(ctx).Info("companion has unparseable referenced-by annotation; preserving",
			"namespace", nsn.Namespace,
			"name", nsn.Name,
			"kind", kind,
		)
		return nil, true, nil
	}
	if decoded == nil {
		// Absent or empty annotation — not a managed companion, skip.
		return nil, true, nil
	}
	return decoded, false, nil
}

// allReferrersAbsent returns true when every WD key in refKeys refers to a WD
// that no longer exists in the hub namespace hubNS. WDs live in the same hub
// namespace as the companion (ns-{project-uid}) after being federated by the
// WorkloadDeploymentFederator.
//
// The WD key format is "projectNamespace/wdName". Only the wdName portion is
// used for the hub lookup — the hub namespace (hubNS) is already known from the
// companion's own namespace.
//
// A WD that exists in the hub — even with a deletionTimestamp — is considered
// present. Terminating referrers still have their finalizers running and may
// complete normal companion teardown; we do not interfere.
//
// An empty refKeys slice means no referrers were recorded — treat as stranded.
func (r *CompanionGCReconciler) allReferrersAbsent(ctx context.Context, hubNS string, refKeys []string) (bool, error) {
	if len(refKeys) == 0 {
		return true, nil
	}

	for _, key := range refKeys {
		wdName, err := wdNameFromRefKey(key)
		if err != nil {
			// Malformed key — treat conservatively: companion is NOT orphaned.
			log.FromContext(ctx).Info("companion has malformed WD key in referenced-by; preserving",
				"key", key,
			)
			return false, nil
		}

		nsn := types.NamespacedName{Namespace: hubNS, Name: wdName}
		var wd computev1alpha.WorkloadDeployment
		switch err := r.HubClient.Get(ctx, nsn, &wd); {
		case err == nil:
			// WD exists in hub (possibly terminating) — live referrer present.
			return false, nil
		case apierrors.IsNotFound(err):
			// This referrer is absent from the hub; check remaining keys.
			continue
		default:
			return false, fmt.Errorf("companion-gc: check hub WD %q existence: %w", key, err)
		}
	}
	return true, nil
}

// wdNameFromRefKey extracts the WD name from a "projectNamespace/wdName" key.
// The namespace portion is discarded — the hub namespace is taken from the
// companion's own namespace, which is passed separately to allReferrersAbsent.
func wdNameFromRefKey(key string) (string, error) {
	idx := strings.Index(key, "/")
	if idx < 0 {
		return "", fmt.Errorf("malformed WD key %q: missing '/' separator", key)
	}
	name := key[idx+1:]
	if name == "" {
		return "", fmt.Errorf("malformed WD key %q: empty name after '/'", key)
	}
	return name, nil
}

// SetupWithManager registers the CompanionGCReconciler with a regular
// ctrl.Manager pointed at the Karmada hub. Both ConfigMap and Secret watches
// are label-scoped so no cluster-wide informer is created.
func (r *CompanionGCReconciler) SetupWithManager(mgr manager.Manager) error {
	labelPredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetLabels()[computev1alpha.ReferencedDataLabel] == computev1alpha.ReferencedDataLabelValue
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, builder.WithPredicates(labelPredicate)).
		Watches(
			&corev1.Secret{},
			&handler.EnqueueRequestForObject{},
			builder.WithPredicates(labelPredicate),
		).
		Named("companion-gc").
		Complete(r)
}

// companionGCPeriodicSweep is a manager.Runnable that periodically lists all
// labeled companion ConfigMaps and Secrets on the hub and calls the reconciler
// directly for each. This backstop covers companions that became stranded before
// the controller started, when no new object events will fire.
type companionGCPeriodicSweep struct {
	hubClient  client.Client
	reconciler *CompanionGCReconciler
	interval   time.Duration
}

// Start runs the periodic sweep loop until ctx is cancelled.
func (s *companionGCPeriodicSweep) Start(ctx context.Context) error {
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

// sweep lists all labeled companion ConfigMaps and Secrets on the hub and
// reconciles each. This catches companions stranded before the controller started.
func (s *companionGCPeriodicSweep) sweep(ctx context.Context) {
	logger := log.FromContext(ctx).WithValues("component", "companion-gc-periodic-sweep")

	labelSel := client.MatchingLabels{
		computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
	}

	var cmList corev1.ConfigMapList
	if err := s.hubClient.List(ctx, &cmList, labelSel); err != nil {
		logger.Error(err, "periodic sweep: list companion ConfigMaps failed")
	} else {
		for i := range cmList.Items {
			cm := &cmList.Items[i]
			if _, err := s.reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name},
			}); err != nil {
				logger.Error(err, "periodic sweep: reconcile ConfigMap companion failed",
					"namespace", cm.Namespace, "name", cm.Name)
			}
		}
	}

	var sList corev1.SecretList
	if err := s.hubClient.List(ctx, &sList, labelSel); err != nil {
		logger.Error(err, "periodic sweep: list companion Secrets failed")
	} else {
		for i := range sList.Items {
			sec := &sList.Items[i]
			if _, err := s.reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: sec.Namespace, Name: sec.Name},
			}); err != nil {
				logger.Error(err, "periodic sweep: reconcile Secret companion failed",
					"namespace", sec.Namespace, "name", sec.Name)
			}
		}
	}
}

// SetupCompanionGCWithManager wires the CompanionGCReconciler and its periodic
// sweep backstop onto the provided ctrl.Manager pointed at the Karmada hub.
// Called from setupManagementControllers in cmd/main.go.
func SetupCompanionGCWithManager(mgr manager.Manager, hubClient client.Client) error {
	r := &CompanionGCReconciler{HubClient: hubClient}
	if err := r.SetupWithManager(mgr); err != nil {
		return err
	}
	return mgr.Add(&companionGCPeriodicSweep{
		hubClient:  hubClient,
		reconciler: r,
		interval:   companionGCSweepInterval,
	})
}
