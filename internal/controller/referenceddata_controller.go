// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/referenceddata"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

const (
	// referencedDataFinalizer is stamped on WorkloadDeployments that reference
	// ConfigMaps or Secrets. The controller removes it after the ref-count cleanup
	// of all companion objects owned by this WD is complete.
	referencedDataFinalizer = "compute.datumapis.com/referenced-data-controller"

	// companionRefCountAnnotation is stamped on companion ConfigMaps/Secrets to
	// track which WorkloadDeployments currently reference them. The value is a
	// JSON array of "namespace/name" strings, sorted deterministically.
	//
	// Ref-counting allows a companion to be shared across multiple WDs that
	// happen to reference the same source object, and deleted only when the last
	// WD drops its reference.
	companionRefCountAnnotation = "compute.datumapis.com/referenced-by"

	// defaultPerObjectLimitBytes is the default maximum byte size of a single
	// companion object (ConfigMap or Secret Data + BinaryData). 256 KiB.
	defaultPerObjectLimitBytes = 256 * 1024

	// defaultAggregateLimitBytes is the default maximum aggregate byte size of
	// all companion objects for a single WorkloadDeployment. 1 MiB.
	defaultAggregateLimitBytes = 1024 * 1024

	// kindConfigMap and kindSecret are the literal kind strings used in
	// referenceddata.ObjectRef to avoid repeated string literals.
	kindConfigMap = "ConfigMap"
	kindSecret    = "Secret"
)

// companionWriter is the abstraction that the controller uses to materialise
// companion ConfigMaps and Secrets on the target namespace/cluster.
//
// localCompanionWriter: writes companions to the same cluster and namespace
// that the WorkloadDeployment lives in. This path is selected when
// FederationClient is nil. NOTE: on the federation branch the management
// controllers always set FederationClient, so localCompanionWriter is
// effectively unreachable in production. It is retained for single-cluster
// dev/test environments and is NOT used in any management-plane federation
// wiring on this branch.
//
// downstreamCompanionWriter: uses Milo's MappedNamespaceResourceStrategy to
// write companions into the `ns-{project-uid}` namespace on the Karmada hub
// so they are propagated to cells alongside the WorkloadDeployment. The
// federator's PropagationPolicy always includes ConfigMap/Secret selectors
// matching the referenced-data label.
type companionWriter interface {
	// ApplyConfigMap creates or updates the companion ConfigMap.
	//
	// existing is the object previously returned by GetConfigMap for the same
	// companion in the same RetryOnConflict iteration (nil when the companion
	// does not yet exist). When existing is non-nil the implementation MUST
	// write to that exact object — it must NOT perform an additional independent
	// GET, which would introduce a second resourceVersion read and defeat the
	// atomic read-modify-write guarantee. The ref-count annotation on desired
	// was computed from existing's annotations; a second GET could advance to a
	// newer resourceVersion that already has a concurrent WD's ref-count entry,
	// and merging desired's annotations over it would silently drop that entry.
	ApplyConfigMap(ctx context.Context, existing *corev1.ConfigMap, desired *corev1.ConfigMap) error

	// ApplySecret creates or updates the companion Secret.
	//
	// existing follows the same contract as ApplyConfigMap.existing.
	ApplySecret(ctx context.Context, existing *corev1.Secret, desired *corev1.Secret) error

	// DeleteConfigMap deletes the companion ConfigMap if it exists.
	DeleteConfigMap(ctx context.Context, namespace, name string) error

	// DeleteSecret deletes the companion Secret if it exists.
	DeleteSecret(ctx context.Context, namespace, name string) error

	// GetConfigMap returns the existing companion ConfigMap, or nil if absent.
	GetConfigMap(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error)

	// GetSecret returns the existing companion Secret, or nil if absent.
	GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error)
}

// localCompanionWriter implements companionWriter using a single cluster-runtime
// client. Companions land in the same cluster and namespace as the WD.
type localCompanionWriter struct {
	cl client.Client
}

func (w *localCompanionWriter) ApplyConfigMap(ctx context.Context, existing *corev1.ConfigMap, desired *corev1.ConfigMap) error {
	if existing == nil {
		return w.cl.Create(ctx, desired)
	}
	// Write desired fields onto the already-fetched existing object so the
	// Update carries the same resourceVersion we read. A concurrent change
	// will raise a conflict, which bubbles up to RetryOnConflict.
	mergeLabels(existing, desired.Labels)
	mergeAnnotations(existing, desired.Annotations)
	existing.Data = desired.Data
	existing.BinaryData = desired.BinaryData
	return w.cl.Update(ctx, existing)
}

func (w *localCompanionWriter) ApplySecret(ctx context.Context, existing *corev1.Secret, desired *corev1.Secret) error {
	if existing == nil {
		return w.cl.Create(ctx, desired)
	}
	mergeLabels(existing, desired.Labels)
	mergeAnnotations(existing, desired.Annotations)
	existing.Data = desired.Data
	existing.Type = desired.Type
	return w.cl.Update(ctx, existing)
}

func (w *localCompanionWriter) DeleteConfigMap(ctx context.Context, namespace, name string) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	return client.IgnoreNotFound(w.cl.Delete(ctx, cm))
}

func (w *localCompanionWriter) DeleteSecret(ctx context.Context, namespace, name string) error {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	return client.IgnoreNotFound(w.cl.Delete(ctx, s))
}

func (w *localCompanionWriter) GetConfigMap(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	err := w.cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cm)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cm, nil
}

func (w *localCompanionWriter) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	var s corev1.Secret
	err := w.cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &s)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// downstreamCompanionWriter implements companionWriter by materialising
// companions into the `ns-{project-uid}` namespace on the Karmada hub using
// MappedNamespaceResourceStrategy. Companions written here are propagated to
// cells via the always-on referenced-data ResourceSelectors in the city-code
// PropagationPolicy.
//
// The downstreamNamespace field is pre-computed by the controller from the
// strategy so that every CRUD call uses the same stable name without needing
// to resolve it repeatedly.
type downstreamCompanionWriter struct {
	// hubClient is a client.Client pointed at the Karmada federation control
	// plane (the same client used by WorkloadDeploymentFederator).
	hubClient client.Client
	// downstreamNamespace is the resolved ns-{project-uid} name on the hub.
	downstreamNamespace string
}

func (w *downstreamCompanionWriter) ApplyConfigMap(ctx context.Context, existing *corev1.ConfigMap, desired *corev1.ConfigMap) error {
	// Redirect the desired object into the downstream namespace. The caller
	// already holds the existing object (from GetConfigMap, which redirects
	// the lookup to the same downstream namespace), so no second GET is needed.
	desired = desired.DeepCopy()
	desired.Namespace = w.downstreamNamespace

	if existing == nil {
		return w.hubClient.Create(ctx, desired)
	}
	// Merge controller-owned labels/annotations into the already-fetched existing
	// object rather than replacing them wholesale. This preserves Karmada
	// bookkeeping annotations that the federation hub stamps on propagated objects.
	// Writing to existing (same resourceVersion) ensures a concurrent change raises
	// a conflict that RetryOnConflict will catch.
	mergeLabels(existing, desired.Labels)
	mergeAnnotations(existing, desired.Annotations)
	existing.Data = desired.Data
	existing.BinaryData = desired.BinaryData
	return w.hubClient.Update(ctx, existing)
}

func (w *downstreamCompanionWriter) ApplySecret(ctx context.Context, existing *corev1.Secret, desired *corev1.Secret) error {
	desired = desired.DeepCopy()
	desired.Namespace = w.downstreamNamespace

	if existing == nil {
		return w.hubClient.Create(ctx, desired)
	}
	// Same merge semantics as ApplyConfigMap — preserve Karmada bookkeeping.
	mergeLabels(existing, desired.Labels)
	mergeAnnotations(existing, desired.Annotations)
	existing.Data = desired.Data
	existing.Type = desired.Type
	return w.hubClient.Update(ctx, existing)
}

func (w *downstreamCompanionWriter) DeleteConfigMap(ctx context.Context, _, name string) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: w.downstreamNamespace, Name: name}}
	return client.IgnoreNotFound(w.hubClient.Delete(ctx, cm))
}

func (w *downstreamCompanionWriter) DeleteSecret(ctx context.Context, _, name string) error {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: w.downstreamNamespace, Name: name}}
	return client.IgnoreNotFound(w.hubClient.Delete(ctx, s))
}

func (w *downstreamCompanionWriter) GetConfigMap(ctx context.Context, _, name string) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	err := w.hubClient.Get(ctx, types.NamespacedName{Namespace: w.downstreamNamespace, Name: name}, &cm)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cm, nil
}

func (w *downstreamCompanionWriter) GetSecret(ctx context.Context, _, name string) (*corev1.Secret, error) {
	var s corev1.Secret
	err := w.hubClient.Get(ctx, types.NamespacedName{Namespace: w.downstreamNamespace, Name: name}, &s)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ReferencedDataControllerOptions configures the ReferencedDataController.
type ReferencedDataControllerOptions struct {
	// Reader is used to read source ConfigMaps and Secrets from the project
	// control plane. When nil, a LocalReader backed by the cluster client is
	// used, which is appropriate for single-cluster and dev environments.
	Reader referenceddata.ProjectConfigSecretReader

	// FederationClient is a client pointed at the Karmada federation control
	// plane (the same client used by WorkloadDeploymentFederator). When
	// non-nil, companions are materialised into the downstream
	// ns-{project-uid} namespace on the hub so that Karmada can propagate
	// them to cells alongside the WorkloadDeployment. When nil, the
	// single-cluster path is used and companions land in the project namespace.
	FederationClient client.Client

	// PerObjectLimitBytes is the maximum allowed byte size for a single
	// companion object (sum of all Data + BinaryData values). Defaults to
	// defaultPerObjectLimitBytes (256 KiB).
	PerObjectLimitBytes int64

	// AggregateLimitBytes is the maximum allowed aggregate byte size across all
	// companion objects for a single WorkloadDeployment. Defaults to
	// defaultAggregateLimitBytes (1 MiB).
	AggregateLimitBytes int64
}

// ReferencedDataController watches WorkloadDeployments and materialises
// companion ConfigMaps/Secrets in the same namespace so that the cell
// InstanceReconciler can gate-clear once the companions arrive.
//
// Reconcile flow (single-cluster, Phase 1):
//  1. Collect the deduplicated set of ConfigMap/Secret refs from the WD template.
//  2. If empty → clear any finalizer, remove expected-set annotation, done.
//  3. Stamp the finalizer.
//  4. Read each source via the ProjectConfigSecretReader (falling back to a
//     LocalReader when none is configured).
//  5. Enforce per-object (256 KiB) and aggregate (1 MiB) size limits. On
//     breach set ReferencedDataReady=False/SourceTooLarge and return.
//  6. Materialise one shared companion per (kind, source-name) in the WD's
//     namespace using a companionWriter. Track referencing WDs in a companion
//     annotation (ref-count).
//  7. Stamp the expected-set annotation on the WD (sorted companion names).
//  8. Delete companions that are no longer referenced by this WD (and have no
//     other referencing WDs).
//  9. Set ReferencedDataReady=True/Ready on the WD status.
//
// Rotation: watches source ConfigMaps/Secrets and re-queues referencing WDs so
// companions are refreshed when sources change.
//
// Deletion: the finalizer prevents WD deletion until companions this WD owns
// have been released (ref-count decremented / companion deleted).

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=workloaddeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// sourceResult pairs an ObjectRef with its resolved source object. Exactly one
// of cm or secret is non-nil, depending on ref.Kind.
type sourceResult struct {
	ref    referenceddata.ObjectRef
	cm     *corev1.ConfigMap
	secret *corev1.Secret
}

type ReferencedDataController struct {
	mgr  clusterGetter
	opts ReferencedDataControllerOptions
}

func (r *ReferencedDataController) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	ctx = mccontext.WithCluster(ctx, req.ClusterName)

	var wd computev1alpha.WorkloadDeployment
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &wd); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("referenceddata: get WorkloadDeployment: %w", err)
	}

	logger.Info("reconciling referenced data", "workloaddeployment", req.NamespacedName)
	defer logger.Info("reconcile complete")

	writer, err := r.writerFor(ctx, string(req.ClusterName), cl.GetClient(), &wd)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("referenceddata: build companion writer: %w", err)
	}
	reader := r.readerFor(cl.GetClient())

	// Handle deletion first: release companions this WD holds a reference to.
	if !wd.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.reconcileDeleted(ctx, cl.GetClient(), writer, &wd)
	}

	refs := referenceddata.CollectFromTemplate(wd.Namespace, wd.Spec.Template)
	if len(refs) == 0 {
		return ctrl.Result{}, r.reconcileEmpty(ctx, cl.GetClient(), writer, &wd)
	}

	// Stamp finalizer so we can clean up companions on WD deletion.
	// Use RetryOnConflict so that a concurrent federator finalizer update on the
	// same object does not produce a noisy optimistic-lock error — we simply
	// re-read and re-apply our own finalizer addition.
	if !controllerutil.ContainsFinalizer(&wd, referencedDataFinalizer) {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := cl.GetClient().Get(ctx, req.NamespacedName, &wd); err != nil {
				return err
			}
			if controllerutil.ContainsFinalizer(&wd, referencedDataFinalizer) {
				return nil // already present from a previous attempt
			}
			controllerutil.AddFinalizer(&wd, referencedDataFinalizer)
			return cl.GetClient().Update(ctx, &wd)
		}); err != nil {
			return ctrl.Result{}, fmt.Errorf("referenceddata: add finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Read each source, enforcing size limits.
	// Cluster name = project ID in Milo mode; ignored by LocalReader.
	sources, condErr := r.resolveAndValidateSources(ctx, reader, string(req.ClusterName), refs, wd.Spec.Template)
	if condErr != nil {
		// A condition error signals a transient or permanent source problem —
		// surface it on the WD and return without requeueing (source watch re-triggers).
		return ctrl.Result{}, r.setConditionAndReturn(ctx, cl.GetClient(), &wd, condErr.reason, condErr.message)
	}

	// expectedTokens is a kind-qualified JSON array written to the
	// expected-referenced-data annotation, e.g. ["ConfigMap/app-config","Secret/db-creds"].
	// Using "Kind/name" tokens lets the cell disambiguate which companion is a
	// ConfigMap and which is a Secret without probing both resource types.
	expectedTokens := make([]string, 0, len(sources))
	for _, src := range sources {
		expectedTokens = append(expectedTokens, referenceddata.CompanionToken(src.ref.Kind, referenceddata.CompanionNameForRef(src.ref)))
	}
	// expectedNames is the flat list of companion object names (source names) used
	// for ref-count bookkeeping and release.
	expectedNames := make([]string, 0, len(sources))
	for _, src := range sources {
		expectedNames = append(expectedNames, referenceddata.CompanionNameForRef(src.ref))
	}

	// wdKey is used both as the ref-count entry key (string form) and for GET/Patch retries.
	wdNSN := types.NamespacedName{Namespace: wd.Namespace, Name: wd.Name}
	wdKey := wdNSN.String()

	if err := r.materialiseCompanions(ctx, writer, wd.Namespace, wdKey, sources); err != nil {
		return ctrl.Result{}, err
	}

	// Drop companions previously owned by this WD that are no longer in the desired set.
	if err := r.releaseRemovedCompanions(ctx, cl.GetClient(), writer, &wd, expectedNames); err != nil {
		return ctrl.Result{}, fmt.Errorf("referenceddata: release removed companions: %w", err)
	}

	// Stamp the expected-set annotation on the WD so the cell can gate-clear.
	// The annotation is a kind-qualified JSON array, e.g.
	// ["ConfigMap/app-config","Secret/db-creds"], so the cell can match
	// companions by kind without probing both resource types.
	// Wrap in RetryOnConflict: the federator may concurrently update the same WD,
	// producing a conflict on the Patch. The annotation write is idempotent, so
	// retrying with a fresh GET is safe.
	annoVal, err := json.Marshal(expectedTokens)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("referenceddata: marshal expected-referenced-data annotation: %w", err)
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := cl.GetClient().Get(ctx, wdNSN, &wd); err != nil {
			return err
		}
		patch := client.MergeFrom(wd.DeepCopy())
		if wd.Annotations == nil {
			wd.Annotations = make(map[string]string)
		}
		wd.Annotations[computev1alpha.ExpectedReferencedDataAnnotation] = string(annoVal)
		return cl.GetClient().Patch(ctx, &wd, patch)
	}); err != nil {
		if apierrors.IsConflict(err) {
			// Conflict after retries — requeue for the next cycle.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("referenceddata: patch expected-referenced-data annotation: %w", err)
	}

	// Update ReferencedDataReady=True on the WD status.
	// Tolerate conflict: the federator may have updated the WD status concurrently.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := cl.GetClient().Get(ctx, wdNSN, &wd); err != nil {
			return err
		}
		changed := apimeta.SetStatusCondition(&wd.Status.Conditions, metav1.Condition{
			Type:               computev1alpha.ReferencedDataReady,
			Status:             metav1.ConditionTrue,
			Reason:             computev1alpha.ReferencedDataReasonReady,
			Message:            fmt.Sprintf("All %d referenced companion(s) are materialised", len(expectedTokens)),
			ObservedGeneration: wd.Generation,
		})
		if !changed {
			return nil
		}
		return cl.GetClient().Status().Update(ctx, &wd)
	}); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("referenceddata: update WD status (ready): %w", err)
	}

	return ctrl.Result{}, nil
}

// reconcileDeleted handles a WorkloadDeployment that is being deleted by
// releasing its companion references and removing the finalizer.
func (r *ReferencedDataController) reconcileDeleted(
	ctx context.Context,
	c client.Client,
	writer companionWriter,
	wd *computev1alpha.WorkloadDeployment,
) error {
	if !controllerutil.ContainsFinalizer(wd, referencedDataFinalizer) {
		return nil
	}
	if err := r.releaseCompanions(ctx, c, writer, wd); err != nil {
		return fmt.Errorf("referenceddata: release companions on deletion: %w", err)
	}
	// Use RetryOnConflict so that a concurrent federator finalizer update does
	// not produce an optimistic-lock error. We re-read the object each attempt
	// and only proceed if our finalizer is still present.
	key := types.NamespacedName{Namespace: wd.Namespace, Name: wd.Name}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, key, wd); err != nil {
			return err
		}
		if !controllerutil.ContainsFinalizer(wd, referencedDataFinalizer) {
			return nil // already removed by a previous attempt
		}
		controllerutil.RemoveFinalizer(wd, referencedDataFinalizer)
		return c.Update(ctx, wd)
	}); err != nil {
		return fmt.Errorf("referenceddata: remove finalizer: %w", err)
	}
	return nil
}

// reconcileEmpty handles a WorkloadDeployment whose template no longer
// references any ConfigMaps or Secrets — clean up any stale state.
func (r *ReferencedDataController) reconcileEmpty(
	ctx context.Context,
	c client.Client,
	writer companionWriter,
	wd *computev1alpha.WorkloadDeployment,
) error {
	if controllerutil.ContainsFinalizer(wd, referencedDataFinalizer) {
		if err := r.releaseCompanions(ctx, c, writer, wd); err != nil {
			return fmt.Errorf("referenceddata: release companions (empty refs): %w", err)
		}
		// Use RetryOnConflict so that a concurrent federator finalizer update does
		// not produce an optimistic-lock error.
		key := types.NamespacedName{Namespace: wd.Namespace, Name: wd.Name}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := c.Get(ctx, key, wd); err != nil {
				return err
			}
			if !controllerutil.ContainsFinalizer(wd, referencedDataFinalizer) {
				return nil // already removed by a previous attempt
			}
			controllerutil.RemoveFinalizer(wd, referencedDataFinalizer)
			return c.Update(ctx, wd)
		}); err != nil {
			return fmt.Errorf("referenceddata: remove finalizer (empty refs): %w", err)
		}
	}
	if _, hasAnno := wd.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]; hasAnno {
		patch := client.MergeFrom(wd.DeepCopy())
		delete(wd.Annotations, computev1alpha.ExpectedReferencedDataAnnotation)
		if err := c.Patch(ctx, wd, patch); err != nil {
			return fmt.Errorf("referenceddata: remove annotation: %w", err)
		}
	}
	return nil
}

// conditionError packages a (reason, message) pair for a False
// ReferencedDataReady condition. It is used as a typed return value so the
// caller can distinguish a condition error from a transient reconcile error.
type conditionError struct {
	reason  string
	message string
}

// resolveAndValidateSources reads each source ConfigMap/Secret via the reader,
// enforces per-object and aggregate size limits, and returns the resolved set.
// On the first validation failure it returns a conditionError; the caller
// surfaces this as a False condition on the WD.
//
// Optional sources: when a source has optional=true in the WD template and is
// missing (SourceNotFound) or oversized (SourceTooLarge), it is silently
// skipped rather than aborting the whole WD. The WD may proceed with the
// remaining non-optional companions.
func (r *ReferencedDataController) resolveAndValidateSources(
	ctx context.Context,
	reader referenceddata.ProjectConfigSecretReader,
	projectID string,
	refs referenceddata.ReferencedSet,
	tmpl computev1alpha.InstanceTemplateSpec,
) ([]sourceResult, *conditionError) {
	perObjLimit := r.opts.PerObjectLimitBytes
	if perObjLimit <= 0 {
		perObjLimit = defaultPerObjectLimitBytes
	}
	aggLimit := r.opts.AggregateLimitBytes
	if aggLimit <= 0 {
		aggLimit = defaultAggregateLimitBytes
	}

	sources := make([]sourceResult, 0, len(refs))
	var aggregateBytes int64

	for _, ref := range refs {
		optional := isOptionalRef(ref, tmpl)
		src, sz, cerr := r.resolveOneSource(ctx, reader, projectID, ref)
		if cerr != nil {
			// Skip optional sources that are missing or unauthorized.
			if optional && (cerr.reason == computev1alpha.ReferencedDataReasonSourceNotFound ||
				cerr.reason == computev1alpha.ReferencedDataReasonSourceUnauthorized) {
				continue
			}
			return nil, cerr
		}
		if sz > perObjLimit {
			if optional {
				continue // skip oversized optional sources
			}
			return nil, &conditionError{
				reason:  computev1alpha.ReferencedDataReasonSourceTooLarge,
				message: fmt.Sprintf("%s %q in namespace %q exceeds per-object size limit (%d bytes > %d bytes)", ref.Kind, ref.Name, ref.Namespace, sz, perObjLimit),
			}
		}
		aggregateBytes += sz
		if aggregateBytes > aggLimit {
			return nil, &conditionError{
				reason:  computev1alpha.ReferencedDataReasonSourceTooLarge,
				message: fmt.Sprintf("aggregate referenced data for WorkloadDeployment exceeds limit (%d bytes > %d bytes)", aggregateBytes, aggLimit),
			}
		}
		sources = append(sources, *src)
	}
	return sources, nil
}

// isOptionalRef returns true when the given ObjectRef corresponds to a source
// that was marked optional=true anywhere in the instance template spec.
// It checks all volume mounts and env/envFrom sources that match (kind, name).
func isOptionalRef(ref referenceddata.ObjectRef, tmpl computev1alpha.InstanceTemplateSpec) bool {
	if isOptionalInVolumes(ref, tmpl.Spec.Volumes) {
		return true
	}
	if sb := tmpl.Spec.Runtime.Sandbox; sb != nil {
		return isOptionalInContainers(ref, sb.Containers)
	}
	return false
}

// isOptionalInVolumes returns true when ref is an optional volume source in volumes.
func isOptionalInVolumes(ref referenceddata.ObjectRef, volumes []computev1alpha.InstanceVolume) bool {
	boolTrue := func(b *bool) bool { return b != nil && *b }
	for _, v := range volumes {
		switch ref.Kind {
		case kindConfigMap:
			if v.ConfigMap != nil && v.ConfigMap.Name == ref.Name && boolTrue(v.ConfigMap.Optional) {
				return true
			}
		case kindSecret:
			if v.Secret != nil && v.Secret.SecretName == ref.Name && boolTrue(v.Secret.Optional) {
				return true
			}
		}
	}
	return false
}

// isOptionalInContainers returns true when ref is optional in any container's
// env or envFrom sources.
func isOptionalInContainers(ref referenceddata.ObjectRef, containers []computev1alpha.SandboxContainer) bool {
	boolTrue := func(b *bool) bool { return b != nil && *b }
	for _, c := range containers {
		for _, ef := range c.EnvFrom {
			switch ref.Kind {
			case kindConfigMap:
				if ef.ConfigMapRef != nil && ef.ConfigMapRef.Name == ref.Name && boolTrue(ef.ConfigMapRef.Optional) {
					return true
				}
			case kindSecret:
				if ef.SecretRef != nil && ef.SecretRef.Name == ref.Name && boolTrue(ef.SecretRef.Optional) {
					return true
				}
			}
		}
		for _, e := range c.Env {
			if e.ValueFrom == nil {
				continue
			}
			switch ref.Kind {
			case kindConfigMap:
				if e.ValueFrom.ConfigMapKeyRef != nil && e.ValueFrom.ConfigMapKeyRef.Name == ref.Name && boolTrue(e.ValueFrom.ConfigMapKeyRef.Optional) {
					return true
				}
			case kindSecret:
				if e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == ref.Name && boolTrue(e.ValueFrom.SecretKeyRef.Optional) {
					return true
				}
			}
		}
	}
	return false
}

// resolveOneSource reads a single ConfigMap or Secret from the project. It
// returns the sourceResult, its byte size, and any condition error.
// A (nil, nil) return from the reader is treated as SourceNotFound.
func (r *ReferencedDataController) resolveOneSource(
	ctx context.Context,
	reader referenceddata.ProjectConfigSecretReader,
	projectID string,
	ref referenceddata.ObjectRef,
) (*sourceResult, int64, *conditionError) {
	switch ref.Kind {
	case kindConfigMap:
		cm, err := reader.GetConfigMap(ctx, projectID, ref.Namespace, ref.Name)
		if err != nil {
			reason, msg := classifyReaderError(err, ref)
			return nil, 0, &conditionError{reason: reason, message: msg}
		}
		if cm == nil {
			return nil, 0, &conditionError{
				reason:  computev1alpha.ReferencedDataReasonSourceNotFound,
				message: fmt.Sprintf("ConfigMap %q not found in namespace %q", ref.Name, ref.Namespace),
			}
		}
		return &sourceResult{ref: ref, cm: cm}, configMapSize(cm), nil

	case kindSecret:
		secret, err := reader.GetSecret(ctx, projectID, ref.Namespace, ref.Name)
		if err != nil {
			reason, msg := classifyReaderError(err, ref)
			return nil, 0, &conditionError{reason: reason, message: msg}
		}
		if secret == nil {
			return nil, 0, &conditionError{
				reason:  computev1alpha.ReferencedDataReasonSourceNotFound,
				message: fmt.Sprintf("Secret %q not found in namespace %q", ref.Name, ref.Namespace),
			}
		}
		return &sourceResult{ref: ref, secret: secret}, secretSize(secret), nil

	default:
		// Unreachable: CollectFromTemplate only emits ConfigMap/Secret.
		return nil, 0, nil
	}
}

// materialiseCompanions creates or updates companion objects for all resolved
// sources, updating ref-count annotations as it goes.
func (r *ReferencedDataController) materialiseCompanions(
	ctx context.Context,
	writer companionWriter,
	namespace, wdKey string,
	sources []sourceResult,
) error {
	for _, src := range sources {
		companionName := referenceddata.CompanionNameForRef(src.ref)
		if err := r.materialiseOne(ctx, writer, namespace, companionName, wdKey, src); err != nil {
			return err
		}
	}
	return nil
}

// materialiseOne applies a single companion ConfigMap or Secret.
//
// The ref-count annotation is read-modify-written atomically inside a single
// RetryOnConflict loop. The same GET result that the ref-count is computed from
// is passed directly to ApplyConfigMap/ApplySecret, which must NOT issue a
// second independent GET. This ensures the Update carries the same
// resourceVersion that was used to compute the ref-count, so a concurrent WD
// that commits its own ref-count entry between the outer GET and the Update
// will cause a conflict error that RetryOnConflict re-reads and retries from.
func (r *ReferencedDataController) materialiseOne(
	ctx context.Context,
	writer companionWriter,
	namespace, companionName, wdKey string,
	src sourceResult,
) error {
	switch src.ref.Kind {
	case kindConfigMap:
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Single GET: the ref-count is computed from this exact snapshot and
			// the same snapshot is passed to ApplyConfigMap so the Update targets
			// the same resourceVersion. A concurrent ref-count write will conflict.
			existing, err := writer.GetConfigMap(ctx, namespace, companionName)
			if err != nil {
				return fmt.Errorf("referenceddata: get companion ConfigMap %q: %w", companionName, err)
			}
			var existingAnnots map[string]string
			if existing != nil {
				existingAnnots = existing.Annotations
			}
			refs, err := refCountAdd(existingAnnots, wdKey)
			if err != nil {
				return fmt.Errorf("referenceddata: ref-count add for ConfigMap %q: %w", companionName, err)
			}
			// Guard: src.cm may be nil if the reader returned (nil, nil).
			if src.cm == nil {
				return fmt.Errorf("referenceddata: source ConfigMap for companion %q is nil", companionName)
			}
			desired := buildCompanionConfigMap(namespace, companionName, src.cm, refs)
			// Pass existing (the same GET snapshot) so ApplyConfigMap can Update
			// it without a second GET, preserving the atomicity guarantee.
			if err := writer.ApplyConfigMap(ctx, existing, desired); err != nil {
				return fmt.Errorf("referenceddata: apply companion ConfigMap %q: %w", companionName, err)
			}
			return nil
		})

	case kindSecret:
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			existing, err := writer.GetSecret(ctx, namespace, companionName)
			if err != nil {
				return fmt.Errorf("referenceddata: get companion Secret %q: %w", companionName, err)
			}
			var existingAnnots map[string]string
			if existing != nil {
				existingAnnots = existing.Annotations
			}
			refs, err := refCountAdd(existingAnnots, wdKey)
			if err != nil {
				return fmt.Errorf("referenceddata: ref-count add for Secret %q: %w", companionName, err)
			}
			// Guard: src.secret may be nil if the reader returned (nil, nil).
			if src.secret == nil {
				return fmt.Errorf("referenceddata: source Secret for companion %q is nil", companionName)
			}
			desired := buildCompanionSecret(namespace, companionName, src.secret, refs)
			if err := writer.ApplySecret(ctx, existing, desired); err != nil {
				return fmt.Errorf("referenceddata: apply companion Secret %q: %w", companionName, err)
			}
			return nil
		})
	}
	return nil
}

// setConditionAndReturn updates the ReferencedDataReady condition on the WD
// with the given (False) reason and message and returns nil so the controller
// reconcile returns (no requeue error). The WD will be re-triggered by the
// source watch when the source changes.
func (r *ReferencedDataController) setConditionAndReturn(
	ctx context.Context,
	c client.Client,
	wd *computev1alpha.WorkloadDeployment,
	reason, message string,
) error {
	apimeta.SetStatusCondition(&wd.Status.Conditions, metav1.Condition{
		Type:               computev1alpha.ReferencedDataReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: wd.Generation,
	})
	if err := c.Status().Update(ctx, wd); err != nil {
		return fmt.Errorf("referenceddata: update WD status (%s): %w", reason, err)
	}
	return nil
}

// classifyReaderError maps a ProjectConfigSecretReader error to a
// (reason, message) pair suitable for the ReferencedDataReady condition.
func classifyReaderError(err error, ref referenceddata.ObjectRef) (reason, message string) {
	switch {
	case errors.Is(err, referenceddata.ErrSourceNotFound):
		return computev1alpha.ReferencedDataReasonSourceNotFound,
			fmt.Sprintf("%s %q not found in namespace %q", ref.Kind, ref.Name, ref.Namespace)
	case errors.Is(err, referenceddata.ErrSourceUnauthorized):
		return computev1alpha.ReferencedDataReasonSourceUnauthorized,
			fmt.Sprintf("not authorized to read %s %q in namespace %q", ref.Kind, ref.Name, ref.Namespace)
	default:
		return computev1alpha.ReferencedDataReasonResolving,
			fmt.Sprintf("failed to read %s %q: %v", ref.Kind, ref.Name, err)
	}
}

// parseAnnotationTokens parses the expected-referenced-data annotation value
// (a kind-qualified JSON array such as ["ConfigMap/app-config","Secret/db-creds"])
// and returns a slice of (kind, companionName) pairs.
//
// For backwards-compatibility, tokens that do not contain a '/' are treated as
// plain companion names with an unknown kind (both ConfigMap and Secret will be
// probed during release).
func parseAnnotationTokens(raw string) ([]struct{ kind, name string }, error) {
	var tokens []string
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return nil, err
	}
	out := make([]struct{ kind, name string }, 0, len(tokens))
	for _, tok := range tokens {
		if idx := strings.Index(tok, "/"); idx >= 0 {
			out = append(out, struct{ kind, name string }{tok[:idx], tok[idx+1:]})
		} else {
			// Legacy plain name — kind unknown, will probe both types.
			out = append(out, struct{ kind, name string }{"", tok})
		}
	}
	return out, nil
}

// releaseCompanions removes this WD's entry from all companion objects it owns.
// Companions with an empty ref-count after removal are deleted.
func (r *ReferencedDataController) releaseCompanions(
	ctx context.Context,
	c client.Client,
	writer companionWriter,
	wd *computev1alpha.WorkloadDeployment,
) error {
	// Determine which companions this WD currently claims from the annotation.
	anno, ok := wd.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]
	if !ok || anno == "" {
		return nil
	}
	entries, err := parseAnnotationTokens(anno)
	if err != nil || len(entries) == 0 {
		// Annotation is malformed or empty; nothing to release.
		return nil
	}

	wdKey := types.NamespacedName{Namespace: wd.Namespace, Name: wd.Name}.String()

	for _, entry := range entries {
		if err := r.releaseOneCompanion(ctx, c, writer, wd.Namespace, entry.kind, entry.name, wdKey); err != nil {
			return err
		}
	}
	return nil
}

// releaseRemovedCompanions removes this WD's ref-count entry from companions
// that were previously expected but are no longer in the current desired set.
// currentNames is the flat list of current companion object names (source names).
func (r *ReferencedDataController) releaseRemovedCompanions(
	ctx context.Context,
	c client.Client,
	writer companionWriter,
	wd *computev1alpha.WorkloadDeployment,
	currentNames []string,
) error {
	anno, ok := wd.Annotations[computev1alpha.ExpectedReferencedDataAnnotation]
	if !ok || anno == "" {
		return nil
	}
	prevEntries, err := parseAnnotationTokens(anno)
	if err != nil || len(prevEntries) == 0 {
		return nil
	}

	wdKey := types.NamespacedName{Namespace: wd.Namespace, Name: wd.Name}.String()

	for _, entry := range prevEntries {
		if slices.Contains(currentNames, entry.name) {
			continue
		}
		if err := r.releaseOneCompanion(ctx, c, writer, wd.Namespace, entry.kind, entry.name, wdKey); err != nil {
			return err
		}
	}
	return nil
}

// releaseOneCompanion removes wdKey from the ref-count annotation of the named
// companion. When kind is known ("ConfigMap" or "Secret") only that resource type
// is probed; when kind is empty (legacy annotation without kind qualification)
// both ConfigMap and Secret are probed.
//
// The read-modify-write is wrapped in RetryOnConflict: two WDs concurrently
// releasing the same companion will each re-read and update safely. The GET
// and the subsequent Update target the same resourceVersion so a concurrent
// change causes a conflict that drives a re-read and retry.
//
// If the ref-count annotation is unparseable the whole call returns an error
// (transient). The companion is NOT deleted in that case — it may still be
// referenced by other WDs whose entries are recorded in the corrupt annotation.
func (r *ReferencedDataController) releaseOneCompanion(
	ctx context.Context,
	_ client.Client,
	writer companionWriter,
	namespace, kind, companionName, wdKey string,
) error {
	releaseConfigMap := kind == kindConfigMap || kind == ""
	releaseSecret := kind == kindSecret || kind == ""

	// Try ConfigMap.
	var cmExists bool
	if releaseConfigMap {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cm, err := writer.GetConfigMap(ctx, namespace, companionName)
			if err != nil {
				return fmt.Errorf("get companion ConfigMap %q: %w", companionName, err)
			}
			if cm == nil {
				return nil // not a ConfigMap companion
			}
			cmExists = true
			remaining, err := refCountRemove(cm.Annotations, wdKey)
			if err != nil {
				// Annotation is corrupt — treat as transient to avoid unsafe deletion.
				return fmt.Errorf("referenceddata: corrupt ref-count on ConfigMap %q: %w", companionName, err)
			}
			if len(remaining) == 0 {
				return writer.DeleteConfigMap(ctx, namespace, companionName)
			}
			// Build a desired object that carries the updated ref-count. Pass the
			// already-fetched cm as existing so ApplyConfigMap updates it at the
			// same resourceVersion — a concurrent write will conflict and retry.
			desired := cm.DeepCopy()
			if desired.Annotations == nil {
				desired.Annotations = make(map[string]string)
			}
			desired.Annotations[companionRefCountAnnotation] = encodeRefCount(remaining)
			return writer.ApplyConfigMap(ctx, cm, desired)
		}); err != nil {
			return err
		}
	}
	if cmExists {
		return nil
	}

	if !releaseSecret {
		return nil
	}

	// Try Secret.
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		s, err := writer.GetSecret(ctx, namespace, companionName)
		if err != nil {
			return fmt.Errorf("get companion Secret %q: %w", companionName, err)
		}
		if s == nil {
			return nil
		}
		remaining, err := refCountRemove(s.Annotations, wdKey)
		if err != nil {
			return fmt.Errorf("referenceddata: corrupt ref-count on Secret %q: %w", companionName, err)
		}
		if len(remaining) == 0 {
			return writer.DeleteSecret(ctx, namespace, companionName)
		}
		desired := s.DeepCopy()
		if desired.Annotations == nil {
			desired.Annotations = make(map[string]string)
		}
		desired.Annotations[companionRefCountAnnotation] = encodeRefCount(remaining)
		return writer.ApplySecret(ctx, s, desired)
	})
}

// writerFor returns the companionWriter appropriate for the current mode.
//
// When a FederationClient is configured (management-plane federation mode),
// it returns a downstreamCompanionWriter that materialises companions into the
// ns-{project-uid} namespace on the Karmada hub so they are propagated to
// cells alongside the WorkloadDeployment.
//
// When FederationClient is nil (single-cluster / dev mode), it falls back to a
// localCompanionWriter that writes companions into the same cluster and
// namespace as the WorkloadDeployment.
func (r *ReferencedDataController) writerFor(
	ctx context.Context,
	clusterName string,
	projectClient client.Client,
	wd *computev1alpha.WorkloadDeployment,
) (companionWriter, error) {
	if r.opts.FederationClient == nil {
		return &localCompanionWriter{cl: projectClient}, nil
	}

	// Compute the downstream namespace using the same MappedNamespaceResourceStrategy
	// the WorkloadDeploymentFederator uses, so companions land in the same
	// ns-{project-uid} namespace as the federated WorkloadDeployment.
	strategy := downstreamclient.NewMappedNamespaceResourceStrategy(clusterName, projectClient, r.opts.FederationClient)
	downstreamNS, err := strategy.GetDownstreamNamespaceNameForUpstreamNamespace(ctx, wd.Namespace)
	if err != nil {
		return nil, fmt.Errorf("resolve downstream namespace: %w", err)
	}

	return &downstreamCompanionWriter{
		hubClient:           r.opts.FederationClient,
		downstreamNamespace: downstreamNS,
	}, nil
}

// readerFor returns the ProjectConfigSecretReader to use. When the controller
// was constructed without a reader (nil), it falls back to a LocalReader that
// reads from the same cluster client — appropriate for single-cluster / dev.
func (r *ReferencedDataController) readerFor(c client.Client) referenceddata.ProjectConfigSecretReader {
	if r.opts.Reader != nil {
		return r.opts.Reader
	}
	return referenceddata.NewLocalReader(c)
}

// buildCompanionConfigMap constructs the companion ConfigMap object. It copies
// Data and BinaryData from the source, stamps the referenced-data label, and
// encodes the ref-count annotation.
func buildCompanionConfigMap(namespace, name string, src *corev1.ConfigMap, refs []string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
			Annotations: map[string]string{
				companionRefCountAnnotation: encodeRefCount(refs),
			},
		},
		Data:       src.Data,
		BinaryData: src.BinaryData,
	}
}

// buildCompanionSecret constructs the companion Secret object. It copies Data
// and Type from the source, stamps the referenced-data label, and encodes the
// ref-count annotation.
func buildCompanionSecret(namespace, name string, src *corev1.Secret, refs []string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
			},
			Annotations: map[string]string{
				companionRefCountAnnotation: encodeRefCount(refs),
			},
		},
		Data: src.Data,
		Type: src.Type,
	}
}

// refCountAdd returns the sorted, deduplicated slice of WD keys after adding
// wdKey. annotations may be nil (companion does not yet exist).
// Returns an error when the existing ref-count annotation cannot be parsed —
// the caller must treat this as a transient error and NOT delete the companion.
func refCountAdd(annotations map[string]string, wdKey string) ([]string, error) {
	current, err := decodeRefCount(annotations)
	if err != nil {
		return nil, err
	}
	if slices.Contains(current, wdKey) {
		return current, nil
	}
	current = append(current, wdKey)
	slices.Sort(current)
	return current, nil
}

// refCountRemove returns the remaining WD keys after removing wdKey.
// Returns an error when the existing ref-count annotation cannot be parsed —
// the caller must treat this as a transient error and NOT delete the companion,
// because other WDs may still hold references recorded in the corrupt entry.
func refCountRemove(annotations map[string]string, wdKey string) ([]string, error) {
	current, err := decodeRefCount(annotations)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(current, func(k string) bool { return k == wdKey }), nil
}

// decodeRefCount parses the ref-count annotation into a slice of WD keys.
// Returns (nil, nil) when the annotation is absent or empty.
// Returns an error when the annotation is present but cannot be parsed — the
// caller must treat this as a transient error rather than an empty ref-count,
// to avoid incorrectly deleting a companion that may still be referenced.
func decodeRefCount(annotations map[string]string) ([]string, error) {
	raw, ok := annotations[companionRefCountAnnotation]
	if !ok || raw == "" {
		return nil, nil
	}
	var keys []string
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return nil, fmt.Errorf("referenceddata: corrupt ref-count annotation %q: %w", raw, err)
	}
	return keys, nil
}

// encodeRefCount serialises WD keys as a JSON array. Returns "[]" on error.
func encodeRefCount(refs []string) string {
	if len(refs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(refs)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// mergeLabels merges wanted labels into obj.Labels, preserving keys not present
// in wanted. This ensures third-party labels (e.g. from Karmada) are not
// discarded on update.
func mergeLabels(obj interface {
	GetLabels() map[string]string
	SetLabels(map[string]string)
}, wanted map[string]string) {
	existing := obj.GetLabels()
	if existing == nil {
		existing = make(map[string]string)
	}
	maps.Copy(existing, wanted)
	obj.SetLabels(existing)
}

// mergeAnnotations merges wanted annotations into obj.Annotations, preserving
// keys not present in wanted. This ensures third-party annotations (e.g. from
// Karmada bookkeeping) are not discarded on update.
func mergeAnnotations(obj interface {
	GetAnnotations() map[string]string
	SetAnnotations(map[string]string)
}, wanted map[string]string) {
	existing := obj.GetAnnotations()
	if existing == nil {
		existing = make(map[string]string)
	}
	maps.Copy(existing, wanted)
	obj.SetAnnotations(existing)
}

// configMapSize returns the total byte size of all Data and BinaryData values
// in a ConfigMap.
func configMapSize(cm *corev1.ConfigMap) int64 {
	var n int64
	for _, v := range cm.Data {
		n += int64(len(v))
	}
	for _, v := range cm.BinaryData {
		n += int64(len(v))
	}
	return n
}

// secretSize returns the total byte size of all Data values in a Secret.
func secretSize(s *corev1.Secret) int64 {
	var n int64
	for _, v := range s.Data {
		n += int64(len(v))
	}
	return n
}

// SetupWithManager registers the controller and its watches with the
// multicluster manager. It is called during management-plane setup.
func (r *ReferencedDataController) SetupWithManager(mgr mcmanager.Manager, opts ReferencedDataControllerOptions) error {
	r.mgr = mgr // mcmanager.Manager satisfies clusterGetter
	r.opts = opts

	return mcbuilder.ControllerManagedBy(mgr).
		For(&computev1alpha.WorkloadDeployment{}, mcbuilder.WithEngageWithLocalCluster(false)).
		// Distinct name so it never collides with the cell's WorkloadDeploymentReconciler.
		Named("referenced-data").
		// Watch source ConfigMaps; re-queue any WD that references them (rotation).
		Watches(&corev1.ConfigMap{}, func(clusterName multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
			return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []mcreconcile.Request {
				return r.enqueueWDsForSource(ctx, r.mgr, clusterName, "ConfigMap", obj)
			})
		}).
		// Watch source Secrets; re-queue any WD that references them (rotation).
		Watches(&corev1.Secret{}, func(clusterName multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
			return handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []mcreconcile.Request {
				return r.enqueueWDsForSource(ctx, r.mgr, clusterName, "Secret", obj)
			})
		}).
		Complete(r)
}

// enqueueWDsForSource looks up all WorkloadDeployments in the cluster that
// reference the changed source ConfigMap or Secret, and returns reconcile
// requests for each.
func (r *ReferencedDataController) enqueueWDsForSource(
	ctx context.Context,
	getter clusterGetter,
	clusterName multicluster.ClusterName,
	kind string,
	obj client.Object,
) []mcreconcile.Request {
	logger := log.FromContext(ctx)

	cl, err := getter.GetCluster(ctx, clusterName)
	if err != nil {
		logger.Error(err, "referenceddata: failed to get cluster for source watch", "cluster", clusterName)
		return nil
	}

	indexKey := wdRefersToConfigMapIndex
	if kind == kindSecret {
		indexKey = wdRefersToSecretIndex
	}

	sourceKey := types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}.String()
	var wdList computev1alpha.WorkloadDeploymentList
	if err := cl.GetClient().List(ctx, &wdList, client.MatchingFields{indexKey: sourceKey}); err != nil {
		logger.Error(err, "referenceddata: failed to list WorkloadDeployments for source", "kind", kind, "source", sourceKey)
		return nil
	}

	requests := make([]mcreconcile.Request, 0, len(wdList.Items))
	for _, wd := range wdList.Items {
		requests = append(requests, mcreconcile.Request{
			Request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: wd.Namespace,
					Name:      wd.Name,
				},
			},
			ClusterName: clusterName,
		})
	}
	return requests
}
