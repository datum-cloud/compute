// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const (
	// instanceShellReportingController is the reportingController on every
	// session event, which the project activity policy keys on.
	instanceShellReportingController = "compute.datumapis.com/instance-shell"

	EventReasonSessionStarted     = "SessionStarted"
	EventReasonSessionEnded       = "SessionEnded"
	EventReasonCleanupUnconfirmed = "CleanupUnconfirmed"

	sessionEventInstanceAnnotation  = computev1alpha.AnnotationNamespace + "/instance"
	sessionEventContainerAnnotation = computev1alpha.AnnotationNamespace + "/container"
	sessionEventReasonAnnotation    = computev1alpha.AnnotationNamespace + "/reason"
	sessionEventExitCodeAnnotation  = computev1alpha.AnnotationNamespace + "/exit-code"
)

// recordLifecycleEvents records SessionStarted once the session has started and
// SessionEnded once it has ended. endReason overrides the ending reason for a
// session deleted before the cell reported one.
//
// Each event has a name derived from the session UID, so recording is
// idempotent: an event that already exists is already on record, and a retry
// after a failed write records it exactly once.
func (r *InstanceConsoleSessionReconciler) recordLifecycleEvents(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
	endReason string,
) error {
	if sessionStarted(session) {
		note := fmt.Sprintf("Shell session started in container %q of instance %q.",
			session.Spec.ContainerName, session.Spec.InstanceRef.Name)
		if err := r.recordSessionEvent(ctx, projectClient, session, corev1.EventTypeNormal, EventReasonSessionStarted, note, nil); err != nil {
			return err
		}
	}

	if endReason == "" && sessionEnded(session) {
		endReason = sessionReadyReason(session)
	}
	if endReason == "" {
		return nil
	}

	annotations := map[string]string{sessionEventReasonAnnotation: endReason}
	note := fmt.Sprintf("Shell session in container %q of instance %q ended: %s.",
		session.Spec.ContainerName, session.Spec.InstanceRef.Name, endReason)
	if code := session.Status.ExitCode; code != nil {
		annotations[sessionEventExitCodeAnnotation] = strconv.Itoa(int(*code))
		note = fmt.Sprintf("Shell session in container %q of instance %q ended: %s with exit code %d.",
			session.Spec.ContainerName, session.Spec.InstanceRef.Name, endReason, *code)
	}
	return r.recordSessionEvent(ctx, projectClient, session, corev1.EventTypeNormal, EventReasonSessionEnded, note, annotations)
}

func (r *InstanceConsoleSessionReconciler) recordCleanupUnconfirmed(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
) error {
	note := fmt.Sprintf("The cell did not confirm within %s that the session's command stopped.", r.cleanupTimeout())
	return r.recordSessionEvent(ctx, projectClient, session, corev1.EventTypeWarning, EventReasonCleanupUnconfirmed, note, nil)
}

// recordSessionEvent writes an events.k8s.io/v1 Event directly through the
// project client rather than an EventRecorder, which cannot set annotations.
// Writing through the project control plane is what scopes the event to the
// project, and so to its activity log.
func (r *InstanceConsoleSessionReconciler) recordSessionEvent(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
	eventType, reason, note string,
	extraAnnotations map[string]string,
) error {
	annotations := map[string]string{
		computev1alpha.InstanceConsoleSessionRequesterAnnotation: session.Annotations[computev1alpha.InstanceConsoleSessionRequesterAnnotation],
		sessionEventInstanceAnnotation:                           session.Spec.InstanceRef.Name,
		sessionEventContainerAnnotation:                          session.Spec.ContainerName,
	}
	for k, v := range extraAnnotations {
		annotations[k] = v
	}

	event := &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:        sessionEventName(session, reason),
			Namespace:   session.Namespace,
			Annotations: annotations,
		},
		EventTime:           metav1.NewMicroTime(r.now()),
		Action:              reason,
		Reason:              reason,
		Note:                note,
		Type:                eventType,
		ReportingController: instanceShellReportingController,
		ReportingInstance:   r.ReportingInstance,
		Regarding: corev1.ObjectReference{
			APIVersion: computev1alpha.GroupVersion.String(),
			Kind:       "InstanceConsoleSession",
			Namespace:  session.Namespace,
			Name:       session.Name,
			UID:        session.UID,
		},
		Related: &corev1.ObjectReference{
			APIVersion: computev1alpha.GroupVersion.String(),
			Kind:       "Instance",
			Namespace:  session.Namespace,
			Name:       session.Spec.InstanceRef.Name,
			UID:        session.Spec.InstanceRef.UID,
		},
	}

	if err := projectClient.Create(ctx, event); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed recording %s event: %w", reason, err)
	}
	return nil
}

// sessionEventName names a session's event of the given reason. Session UIDs
// never repeat, so the name identifies exactly one event per session.
func sessionEventName(session *computev1alpha.InstanceConsoleSession, reason string) string {
	return fmt.Sprintf("%s.%s", session.UID, strings.ToLower(reason))
}
