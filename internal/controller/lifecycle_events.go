// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.miloapis.com/milo/pkg/downstreamclient"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller/instancecontrol"
)

// Lifecycle events feed the project activity timeline from the cell. Each
// names the project's Workload as its related object, so activity can tie it
// to the user's own actions on that workload.
const (
	eventReasonInstanceTerminating = "Terminating"
	eventReasonDeploymentScaled    = "Scaled"

	lifecycleEventTimeout = 5 * time.Second

	// Activity shows the reporting controller as the event's actor.
	instanceLifecycleReportingController           = "compute.datumapis.com/instance-controller"
	workloadDeploymentLifecycleReportingController = "compute.datumapis.com/workload-deployment-controller"

	// API limits on an event's name and reporting instance.
	maxEventNameLen         = 253
	maxReportingInstanceLen = 128
)

// instanceLifecycleReadyReasons are the Ready reasons worth a timeline entry.
// Suspension is left out because ProjectPaused already records it.
var instanceLifecycleReadyReasons = map[string]string{
	computev1alpha.InstanceReadyReasonAvailable:          corev1.EventTypeNormal,
	computev1alpha.InstanceAvailableReasonStopping:       corev1.EventTypeNormal,
	computev1alpha.InstanceReadyReasonImageUnavailable:   corev1.EventTypeWarning,
	computev1alpha.InstanceReadyReasonConfigurationError: corev1.EventTypeWarning,
	computev1alpha.InstanceReadyReasonInstanceCrashing:   corev1.EventTypeWarning,
	// The provider's reason for an instance whose process exited with an error.
	"Failed": corev1.EventTypeWarning,
}

// lifecycleEventWriter creates Events directly rather than through an
// EventRecorder, which folds events differing only in their note into one
// series, losing e.g. the second of two scalings. Failures are logged, never
// returned.
type lifecycleEventWriter struct {
	reportingController string
	reportingInstance   string
	now                 func() time.Time
}

func newLifecycleEventWriter(reportingController string) *lifecycleEventWriter {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "compute-manager"
	}
	if len(host) > maxReportingInstanceLen {
		host = host[:maxReportingInstanceLen]
	}
	return &lifecycleEventWriter{
		reportingController: reportingController,
		reportingInstance:   host,
		now:                 time.Now,
	}
}

func (w *lifecycleEventWriter) record(
	ctx context.Context,
	cl cluster.Cluster,
	regarding corev1.ObjectReference,
	workloadName string,
	workloadUID types.UID,
	eventType, reason, note string,
) {
	if w == nil {
		return
	}
	if len(note) > maxEventNoteLen {
		note = strings.ToValidUTF8(note[:maxEventNoteLen-3], "") + "..."
	}
	ctx, cancel := context.WithTimeout(ctx, lifecycleEventTimeout)
	defer cancel()

	now := w.now()
	name := fmt.Sprintf("%s.%x", regarding.Name, now.UnixNano())
	if len(name) > maxEventNameLen {
		name = name[len(name)-maxEventNameLen:]
	}
	event := &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: regarding.Namespace,
		},
		EventTime:           metav1.NewMicroTime(now),
		Action:              reason,
		Reason:              reason,
		Note:                note,
		Type:                eventType,
		ReportingController: w.reportingController,
		ReportingInstance:   w.reportingInstance,
		Regarding:           regarding,
	}
	if workloadName != "" {
		event.Related = &corev1.ObjectReference{
			APIVersion: computev1alpha.GroupVersion.String(),
			Kind:       "Workload",
			Namespace:  projectNamespace(ctx, cl, regarding.Namespace),
			Name:       workloadName,
			UID:        workloadUID,
		}
	}
	if err := cl.GetClient().Create(ctx, event); err != nil {
		log.FromContext(ctx).Error(err, "failed recording lifecycle event",
			"reason", reason, "kind", regarding.Kind, "name", regarding.Name)
	}
}

// projectNamespace is the project namespace a cell namespace stands for, or
// the namespace itself where it is not a cell copy, as in single-cluster mode.
func projectNamespace(ctx context.Context, cl cluster.Cluster, namespace string) string {
	var ns corev1.Namespace
	if err := cl.GetAPIReader().Get(ctx, client.ObjectKey{Name: namespace}, &ns); err == nil {
		if upstream := ns.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]; upstream != "" {
			return upstream
		}
	}
	return namespace
}

func computeObjectReference(kind string, obj client.Object) corev1.ObjectReference {
	return corev1.ObjectReference{
		APIVersion: computev1alpha.GroupVersion.String(),
		Kind:       kind,
		Namespace:  obj.GetNamespace(),
		Name:       obj.GetName(),
		UID:        obj.GetUID(),
	}
}

func readyReason(instance *computev1alpha.Instance) string {
	if c := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceReady); c != nil {
		return c.Reason
	}
	return ""
}

func (w *lifecycleEventWriter) recordInstance(
	ctx context.Context, cl cluster.Cluster, instance *computev1alpha.Instance, eventType, reason, note string,
) {
	w.record(ctx, cl, computeObjectReference("Instance", instance),
		instance.Labels[computev1alpha.WorkloadNameLabel], types.UID(instance.Labels[computev1alpha.WorkloadUIDLabel]),
		eventType, reason, note)
}

// recordInstanceReadyTransition must run only after the new status is
// persisted, so a retried reconcile never records a transition twice.
func (w *lifecycleEventWriter) recordInstanceReadyTransition(
	ctx context.Context,
	cl cluster.Cluster,
	instance *computev1alpha.Instance,
	priorReason string,
) {
	ready := apimeta.FindStatusCondition(instance.Status.Conditions, computev1alpha.InstanceReady)
	if ready == nil || ready.Reason == priorReason {
		return
	}
	eventType, ok := instanceLifecycleReadyReasons[ready.Reason]
	if !ok {
		return
	}
	w.recordInstance(ctx, cl, instance, eventType, ready.Reason, ready.Message)
}

// recordInstanceTerminating records only the first deletion: an instance
// being deleted gets a delete action on every pass until it is gone.
func (w *lifecycleEventWriter) recordInstanceTerminating(
	ctx context.Context,
	cl cluster.Cluster,
	action instancecontrol.Action,
	desiredReplicas int32,
) {
	instance, ok := action.Object.(*computev1alpha.Instance)
	if !ok || action.ActionType() != instancecontrol.ActionTypeDelete || !instance.DeletionTimestamp.IsZero() {
		return
	}
	note := "Replacing it with the deployment's current template"
	if index, err := strconv.Atoi(instance.Labels[computev1alpha.InstanceIndexLabel]); err == nil && int32(index) >= desiredReplicas {
		note = fmt.Sprintf("Scaling down to %d replicas", desiredReplicas)
	}
	w.recordInstance(ctx, cl, instance, corev1.EventTypeNormal, eventReasonInstanceTerminating, note)
}

// recordDeploymentScaled leaves replica changes caused by suspension to the
// suspension events.
func (w *lifecycleEventWriter) recordDeploymentScaled(
	ctx context.Context,
	cl cluster.Cluster,
	deployment *computev1alpha.WorkloadDeployment,
	prior computev1alpha.WorkloadDeploymentStatus,
) {
	from, to := prior.DesiredReplicas, deployment.Status.DesiredReplicas
	if from == to || prior.ObservedGeneration == 0 || prior.Suspended || deployment.Status.Suspended {
		return
	}
	w.record(ctx, cl, computeObjectReference("WorkloadDeployment", deployment),
		deployment.Spec.WorkloadRef.Name, deployment.Spec.WorkloadRef.UID,
		corev1.EventTypeNormal, eventReasonDeploymentScaled, fmt.Sprintf("Scaled from %d to %d replicas", from, to))
}
