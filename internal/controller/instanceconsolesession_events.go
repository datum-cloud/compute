// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
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
// Each event is recorded once per session: see recordOnce.
func (r *InstanceConsoleSessionReconciler) recordLifecycleEvents(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
	endReason string,
) error {
	if sessionStarted(session) {
		note := fmt.Sprintf("Shell session started in container %q of instance %q.",
			session.Spec.ContainerName, session.Spec.InstanceRef.Name)
		if err := r.recordOnce(ctx, projectClient, session, EventReasonSessionStarted, func() error {
			return r.recordSessionEvent(ctx, projectClient, session, corev1.EventTypeNormal, EventReasonSessionStarted, note, nil)
		}); err != nil {
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
	return r.recordOnce(ctx, projectClient, session, EventReasonSessionEnded, func() error {
		return r.recordSessionEvent(ctx, projectClient, session, corev1.EventTypeNormal, EventReasonSessionEnded, note, annotations)
	})
}

func (r *InstanceConsoleSessionReconciler) recordCleanupUnconfirmed(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
) error {
	note := fmt.Sprintf("The cell did not confirm within %s that the session's command stopped.", r.cleanupTimeout())
	return r.recordOnce(ctx, projectClient, session, EventReasonCleanupUnconfirmed, func() error {
		return r.recordSessionEvent(ctx, projectClient, session, corev1.EventTypeWarning, EventReasonCleanupUnconfirmed, note, nil)
	})
}

// recordOnce records a session's event of the given reason at most once, even
// though the project events store accepts two events with the same name and
// the session is reconciled many times around its end.
//
// Before recording, it claims the event in the session's status with
// optimistic concurrency, so only a reconcile that saw the session's latest
// state can record it; one working from a stale cache conflicts and retries.
// Once the event is written the status says so. An attempt that did not
// finish is claimed and recorded again. The claim lives in status because
// requesters cannot write status, so they cannot mark an event recorded and
// keep it out of the project's activity.
func (r *InstanceConsoleSessionReconciler) recordOnce(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
	reason string,
	record func() error,
) error {
	if entry := sessionRecordedEvent(session, reason); entry != nil && entry.Recorded {
		return nil
	}

	setRecordedEvent(session, reason, r.now(), false)
	if err := projectClient.Status().Update(ctx, session); err != nil {
		return fmt.Errorf("failed claiming the %s event: %w", reason, err)
	}

	if err := record(); err != nil {
		return err
	}

	attemptedAt := r.now()
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		setRecordedEvent(session, reason, attemptedAt, true)
		err := projectClient.Status().Update(ctx, session)
		if apierrors.IsConflict(err) {
			if getErr := projectClient.Get(ctx, client.ObjectKeyFromObject(session), session); getErr != nil {
				return getErr
			}
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("failed marking the %s event recorded: %w", reason, err)
	}
	return nil
}

func sessionRecordedEvent(session *computev1alpha.InstanceConsoleSession, reason string) *computev1alpha.InstanceConsoleSessionRecordedEvent {
	for i := range session.Status.RecordedEvents {
		if session.Status.RecordedEvents[i].Reason == reason {
			return &session.Status.RecordedEvents[i]
		}
	}
	return nil
}

func setRecordedEvent(session *computev1alpha.InstanceConsoleSession, reason string, attemptedAt time.Time, recorded bool) {
	entry := sessionRecordedEvent(session, reason)
	if entry == nil {
		session.Status.RecordedEvents = append(session.Status.RecordedEvents,
			computev1alpha.InstanceConsoleSessionRecordedEvent{Reason: reason})
		entry = &session.Status.RecordedEvents[len(session.Status.RecordedEvents)-1]
	}
	if !recorded {
		entry.AttemptedAt = metav1.NewTime(attemptedAt)
	}
	entry.Recorded = recorded
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
