// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/federation"
	"go.miloapis.com/milo/pkg/downstreamclient"
)

const (
	// defaultProjectionGracePeriod bounds how long an absent project
	// WorkloadDeployment counts as an ordering race. Projection can legitimately
	// run before the project WD exists, so early absence is retryable. After this
	// window, the absence is a fact about the project plane rather than a race.
	defaultProjectionGracePeriod = 15 * time.Minute

	// eventActionQuarantining names the controller operation a quarantine event
	// describes; events.k8s.io separates the action from the outcome reason.
	eventActionQuarantining = "QuarantiningProjection"
)

// InstanceProjector watches Instance objects written back to the upstream
// Karmada/management control plane by POP-cell InstanceReconcilers and creates
// read-only projections in the corresponding project namespace within each
// project cluster.
//
// Namespace resolution: an upstream Instance lives in namespace
// `ns-<project-namespace-uid>`. The UID portion is matched against the UID of
// namespaces in the project cluster to find the target namespace.
//
// Ownership: each projected Instance is owned by the project WorkloadDeployment
// so that it is garbage-collected via cascading deletion when the deployment is
// removed from the project cluster.
//
// The projector never deletes anything. It decides only whether it can project,
// and classifies why not. A retryable state keeps the usual error and backoff. A
// terminal state is reported once and quarantined.
//
// Quarantine is a safety check, not a cleanup mechanism. Hub objects are owned
// by their hub WorkloadDeployment and write-back is owner-gated, so a hub
// Instance the projector can never resolve means one of those invariants broke.
// An event, an error-level log, and a latched gauge make that visible without
// pinning the reconcile error ratio at 100%, which would page as an outage for a
// condition no retry can clear.
//
// The controller is registered with a standard manager.Manager pointed at the
// upstream Karmada control plane — NOT the multicluster-runtime manager — so
// informer watches are scoped to the upstream control plane.
type InstanceProjector struct {
	// FederationClient reads Instance objects from the Karmada federation control
	// plane (configured via --federation-kubeconfig). Must be set before
	// SetupWithManager is called.
	FederationClient client.Client

	// MCManager provides access to project cluster clients via GetCluster.
	MCManager mcmanager.Manager

	// GracePeriod bounds how long an absent project WorkloadDeployment counts as
	// an ordering race. Zero selects defaultProjectionGracePeriod.
	GracePeriod time.Duration

	// Recorder emits one quarantine event per object. Optional; when nil, the
	// quarantine is reported in the log and metrics only.
	Recorder events.EventRecorder

	quarantined *quarantineTracker
}

// terminalOutcome describes a projection failure no retry can change. The
// projector reports it once and quarantines the object.
type terminalOutcome struct {
	// reason is the metric/annotation label for the failure class.
	reason string
	// message explains the failure to an operator.
	message string
}

// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.datumapis.com,resources=instances/status,verbs=get;update;patch

func (r *InstanceProjector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("instance", req.NamespacedName)

	var downstreamInstance computev1alpha.Instance
	if err := r.FederationClient.Get(ctx, req.NamespacedName, &downstreamInstance); err != nil {
		if apierrors.IsNotFound(err) {
			// Instance was deleted from the upstream control plane. Projections
			// are owned by the project WorkloadDeployment, so cascading deletion
			// handles cleanup.
			r.tracker().forget(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed getting upstream instance: %w", err)
	}

	// An object that is being deleted needs neither projection nor
	// classification.
	if !downstreamInstance.DeletionTimestamp.IsZero() {
		r.tracker().forget(req.NamespacedName)
		return ctrl.Result{}, nil
	}

	// A quarantine applies only to the exact state that produced it. When the
	// object changes and the fingerprint stops matching, discard the quarantine
	// and evaluate the object again.
	if reason, held := r.heldQuarantine(&downstreamInstance); held {
		r.tracker().hold(req.NamespacedName, reason)
		logger.V(1).Info("skipping quarantined instance", "reason", reason)
		return ctrl.Result{}, nil
	}
	if err := r.clearQuarantine(ctx, &downstreamInstance); err != nil {
		return ctrl.Result{}, err
	}
	r.tracker().forget(req.NamespacedName)

	terminal, err := r.project(ctx, logger, &downstreamInstance)
	if err != nil {
		return ctrl.Result{}, err
	}
	if terminal != nil {
		if err := r.quarantine(ctx, logger, &downstreamInstance, terminal); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// project projects the instance into its project cluster. It returns a terminal
// outcome for a state no retry can change, an error for a retryable one, and
// (nil, nil) on success.
//
//nolint:gocyclo // the classification is a flat sequence of identity checks; splitting it would hide the taxonomy
func (r *InstanceProjector) project(
	ctx context.Context,
	logger logr.Logger,
	downstreamInstance *computev1alpha.Instance,
) (*terminalOutcome, error) {
	// Federation-plane Instances exist only as write-back copies, and the
	// InstanceReconciler stamps both upstream-owner labels when it writes the
	// copy. A missing label cannot appear later, so no retry can supply it and no
	// controller can tell what the object is.
	encodedClusterName := downstreamInstance.Labels[downstreamclient.UpstreamOwnerClusterNameLabel]
	if encodedClusterName == "" {
		return &terminalOutcome{
			reason: federation.QuarantineReasonUnidentifiable,
			message: fmt.Sprintf("instance is missing the %s label; the project cluster can never be resolved",
				downstreamclient.UpstreamOwnerClusterNameLabel),
		}, nil
	}

	// Derive the project cluster key the same way the federator does. The label
	// encodes the full "org/project" path, while the multicluster provider keys
	// clusters by bare project name.
	clusterName := projectClusterNameFromLabel(encodedClusterName)
	if clusterName == "" {
		return &terminalOutcome{
			reason: federation.QuarantineReasonUnidentifiable,
			message: fmt.Sprintf("instance carries an undecodable %s label (%q)",
				downstreamclient.UpstreamOwnerClusterNameLabel, encodedClusterName),
		}, nil
	}

	// Both upstream-owner labels are stamped together with non-empty values, so a
	// cluster label without a namespace label is the same invariant violation.
	targetNamespace := downstreamInstance.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
	if targetNamespace == "" {
		return &terminalOutcome{
			reason: federation.QuarantineReasonMissingNamespaceLabel,
			message: fmt.Sprintf("instance carries %s but is missing the %s label; the project namespace can never be resolved",
				downstreamclient.UpstreamOwnerClusterNameLabel, downstreamclient.UpstreamOwnerNamespaceLabel),
		}, nil
	}

	// Resolve the owning WorkloadDeployment by NAME in the project cluster. Core
	// invariant: build the ownerReference from a project-cluster object fetched
	// with projectClient.Get, never from an edge or Karmada identity. The WD name
	// is stable across all planes and is carried by WorkloadDeploymentNameLabel,
	// stamped by the edge stateful control strategy.
	wdName := downstreamInstance.Labels[computev1alpha.WorkloadDeploymentNameLabel]
	if wdName == "" {
		return &terminalOutcome{
			reason: federation.QuarantineReasonMissingDeploymentName,
			message: fmt.Sprintf("instance is missing the %s label; its WorkloadDeployment can never be resolved",
				computev1alpha.WorkloadDeploymentNameLabel),
		}, nil
	}

	// An unresolvable project cluster is always retryable. Engagement is
	// asynchronous, and a project that is really gone takes its hub objects with
	// it: the federator finalizer removes the hub WorkloadDeployment before the
	// project control plane finishes deleting, and the hub garbage collector then
	// reclaims every write-back copy it owns.
	projectCluster, err := r.MCManager.GetCluster(ctx, multicluster.ClusterName(clusterName))
	if err != nil {
		return nil, fmt.Errorf("failed getting project cluster %q: %w", clusterName, err)
	}
	projectClient := projectCluster.GetClient()

	// Fetch the project-cluster WD directly by name. The returned object carries
	// the project-cluster metadata.uid — the only UID that GC in the project
	// cluster can act on.
	var ownerWD computev1alpha.WorkloadDeployment
	if err := projectClient.Get(ctx, client.ObjectKey{Namespace: targetNamespace, Name: wdName}, &ownerWD); err != nil {
		if apierrors.IsNotFound(err) {
			// Never create an ownerless projection. Early in an object's life, an
			// absent WD is an ordering race that the retry resolves. Past the grace
			// window it is a fact about the project plane, and retrying forever only
			// turns a leak into a permanent error ratio.
			if r.withinGracePeriod(downstreamInstance) {
				return nil, fmt.Errorf("workload deployment %q not found in project cluster %q for instance %s/%s",
					wdName, clusterName, downstreamInstance.Namespace, downstreamInstance.Name)
			}
			return &terminalOutcome{
				reason: federation.QuarantineReasonDeploymentAbsent,
				message: fmt.Sprintf("workload deployment %q is absent from project cluster %q past the projection grace period; "+
					"a hub Instance that outlives its project deployment means hub ownership or owner-gated write-back has been "+
					"violated and needs investigation", wdName, clusterName),
			}, nil
		}
		return nil, fmt.Errorf("failed getting WorkloadDeployment %s/%s in project cluster %s: %w",
			targetNamespace, wdName, clusterName, err)
	}

	projection := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      downstreamInstance.Name,
			Namespace: targetNamespace,
		},
	}

	operationResult, err := controllerutil.CreateOrUpdate(ctx, projectClient, projection, func() error {
		// Propagate upstream tracking labels so consumers can filter by origin.
		if projection.Labels == nil {
			projection.Labels = make(map[string]string)
		}
		for k, v := range downstreamInstance.Labels {
			projection.Labels[k] = v
		}

		projection.Spec = downstreamInstance.Spec

		// Attach an owner reference using the live project-cluster WD object.
		// controllerutil.SetOwnerReference reads UID and GVK from ownerWD, which
		// was fetched from projectClient — satisfying the core invariant.
		return controllerutil.SetOwnerReference(&ownerWD, projection, projectCluster.GetScheme())
	})
	if err != nil {
		return nil, fmt.Errorf("failed upserting Instance projection in %s/%s: %w", clusterName, targetNamespace, err)
	}

	logger.Info("reconciled Instance projection", "operation", operationResult, "namespace", targetNamespace, "cluster", clusterName)

	// Status is a separate subresource.
	projection.Status = downstreamInstance.Status
	if err := projectClient.Status().Update(ctx, projection); err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed updating Instance projection status: %w", err)
	}

	return nil, nil
}

// withinGracePeriod reports whether the object is young enough that an absent
// project WorkloadDeployment could still be a creation-ordering race.
func (r *InstanceProjector) withinGracePeriod(obj client.Object) bool {
	grace := r.GracePeriod
	if grace <= 0 {
		grace = defaultProjectionGracePeriod
	}
	created := obj.GetCreationTimestamp().Time
	// Treat an object with no readable age as young. Never quarantine based on a
	// fact that could not be established.
	return created.IsZero() || time.Since(created) < grace
}

// quarantine records the terminal outcome on the object, reports it once in the
// log and as an event, and latches the quarantine gauge.
func (r *InstanceProjector) quarantine(
	ctx context.Context,
	logger logr.Logger,
	instance *computev1alpha.Instance,
	outcome *terminalOutcome,
) error {
	patch := client.MergeFrom(instance.DeepCopy())
	annotations := instance.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[computev1alpha.QuarantineReasonAnnotation] = outcome.reason
	annotations[computev1alpha.QuarantineMessageAnnotation] = outcome.message
	annotations[computev1alpha.QuarantineFingerprintAnnotation] = quarantineFingerprint(instance)
	annotations[computev1alpha.QuarantinedAtAnnotation] = time.Now().UTC().Format(time.RFC3339)
	instance.SetAnnotations(annotations)

	if err := r.FederationClient.Patch(ctx, instance, patch); err != nil {
		return fmt.Errorf("failed recording quarantine on instance %s/%s: %w",
			instance.Namespace, instance.Name, err)
	}

	// Report once, at error level: a quarantine means either a compute stamping
	// bug or an object only a human can explain.
	logger.Error(nil, "quarantined federation-plane instance; it will not be retried",
		"reason", outcome.reason, "message", outcome.message)
	if r.Recorder != nil {
		r.Recorder.Eventf(instance, nil, corev1.EventTypeWarning,
			quarantineEventReason(outcome.reason), eventActionQuarantining, "%s", outcome.message)
	}
	r.tracker().hold(client.ObjectKeyFromObject(instance), outcome.reason)
	return nil
}

// heldQuarantine reports why an object is quarantined and whether that
// quarantine still applies to the object's live state.
func (r *InstanceProjector) heldQuarantine(instance *computev1alpha.Instance) (string, bool) {
	reason := instance.Annotations[computev1alpha.QuarantineReasonAnnotation]
	if reason == "" {
		return "", false
	}
	return reason, instance.Annotations[computev1alpha.QuarantineFingerprintAnnotation] == quarantineFingerprint(instance)
}

// clearQuarantine removes a stale quarantine record so the object is evaluated
// from scratch. It is a no-op for an object that carries none.
func (r *InstanceProjector) clearQuarantine(ctx context.Context, instance *computev1alpha.Instance) error {
	if instance.Annotations[computev1alpha.QuarantineReasonAnnotation] == "" {
		return nil
	}
	patch := client.MergeFrom(instance.DeepCopy())
	for _, key := range []string{
		computev1alpha.QuarantineReasonAnnotation,
		computev1alpha.QuarantineMessageAnnotation,
		computev1alpha.QuarantineFingerprintAnnotation,
		computev1alpha.QuarantinedAtAnnotation,
	} {
		delete(instance.Annotations, key)
	}
	if err := r.FederationClient.Patch(ctx, instance, patch); err != nil {
		return fmt.Errorf("failed clearing stale quarantine on instance %s/%s: %w",
			instance.Namespace, instance.Name, err)
	}
	return nil
}

func (r *InstanceProjector) tracker() *quarantineTracker {
	if r.quarantined == nil {
		r.quarantined = newQuarantineTracker()
	}
	return r.quarantined
}

// quarantineFingerprint digests the object state a quarantine is based on: the
// identity labels and nothing else. Repairing a label changes the digest and
// invalidates the quarantine. Ordinary status churn does not.
func quarantineFingerprint(instance *computev1alpha.Instance) string {
	keys := []string{
		downstreamclient.UpstreamOwnerClusterNameLabel,
		downstreamclient.UpstreamOwnerNamespaceLabel,
		computev1alpha.WorkloadDeploymentNameLabel,
	}
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(instance.Labels[key])
		b.WriteString(";")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

// quarantineEventReason renders a metric reason as a Kubernetes event reason.
func quarantineEventReason(reason string) string {
	parts := strings.Split(reason, "_")
	var b strings.Builder
	b.WriteString("Quarantined")
	for _, part := range parts {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}

// quarantineTracker keeps the quarantine gauge in step with the set of objects
// currently held, so the gauge counts how many objects are quarantined rather
// than how many reconciles hit one.
type quarantineTracker struct {
	mu    sync.Mutex
	byKey map[types.NamespacedName]string
}

func newQuarantineTracker() *quarantineTracker {
	return &quarantineTracker{byKey: map[types.NamespacedName]string{}}
}

func (t *quarantineTracker) hold(key types.NamespacedName, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if previous, ok := t.byKey[key]; ok {
		if previous == reason {
			return
		}
		federation.QuarantinedObjects.WithLabelValues(previous).Dec()
	}
	t.byKey[key] = reason
	federation.QuarantinedObjects.WithLabelValues(reason).Inc()
}

func (t *quarantineTracker) forget(key types.NamespacedName) {
	t.mu.Lock()
	defer t.mu.Unlock()
	reason, ok := t.byKey[key]
	if !ok {
		return
	}
	delete(t.byKey, key)
	federation.QuarantinedObjects.WithLabelValues(reason).Dec()
}

func (t *quarantineTracker) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byKey)
}

// SetupWithManager registers the InstanceProjector with upstreamMgr, a standard
// manager.Manager configured against the upstream Karmada/federation control plane
// REST config. FederationClient and MCManager must be set before calling this method.
func (r *InstanceProjector) SetupWithManager(upstreamMgr manager.Manager) error {
	r.quarantined = newQuarantineTracker()
	if r.Recorder == nil {
		r.Recorder = upstreamMgr.GetEventRecorder("instance-projector")
	}
	return ctrl.NewControllerManagedBy(upstreamMgr).
		For(&computev1alpha.Instance{}).
		Named("instance-projector").
		Complete(r)
}
