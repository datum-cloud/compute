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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/shelltrace"
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
	sessionEventDurationAnnotation  = computev1alpha.AnnotationNamespace + "/duration"
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
	placement := placementOf(session)
	logger := log.FromContext(ctx).WithValues("instance", session.Spec.InstanceRef.Name,
		"container", session.Spec.ContainerName)

	if sessionStarted(session) {
		note := fmt.Sprintf("Shell session started in container %q of instance %q.",
			session.Spec.ContainerName, session.Spec.InstanceRef.Name)
		recorded, err := r.recordOnce(ctx, projectClient, session, EventReasonSessionStarted, func() error {
			return r.recordSessionEvent(ctx, projectClient, session, corev1.EventTypeNormal, EventReasonSessionStarted, note, nil)
		})
		if err != nil {
			return err
		}
		if recorded {
			// Recording the event is the once-per-session transition the
			// metrics follow, so a repeated reconcile cannot observe twice.
			connect := sessionConnectTime(session)
			if connect >= 0 {
				sessionConnectSeconds.WithLabelValues(placement.location).Observe(connect.Seconds())
			}
			logger.Info("session started", "startedAt", session.Status.StartedAt, "connectTime", connect)
			shelltrace.RecordConnection(session, "")
		}
	}

	if endReason == "" && sessionEnded(session) {
		endReason = sessionReadyReason(session)
	}
	if endReason == "" {
		return nil
	}

	duration := sessionDuration(session, r.now())
	annotations := map[string]string{sessionEventReasonAnnotation: endReason}
	if duration >= 0 {
		annotations[sessionEventDurationAnnotation] = duration.String()
	}
	note := fmt.Sprintf("Shell session in container %q of instance %q ended: %s.",
		session.Spec.ContainerName, session.Spec.InstanceRef.Name, endReason)
	if code := session.Status.ExitCode; code != nil {
		annotations[sessionEventExitCodeAnnotation] = strconv.Itoa(int(*code))
		note = fmt.Sprintf("Shell session in container %q of instance %q ended: %s with exit code %d.",
			session.Spec.ContainerName, session.Spec.InstanceRef.Name, endReason, *code)
	}
	recorded, err := r.recordOnce(ctx, projectClient, session, EventReasonSessionEnded, func() error {
		return r.recordSessionEvent(ctx, projectClient, session, corev1.EventTypeNormal, EventReasonSessionEnded, note, annotations)
	})
	if err != nil || !recorded {
		return err
	}
	sessionsEnded.WithLabelValues(placement.location, endReason).Inc()
	if session.Status.StartedAt == nil {
		shelltrace.RecordConnection(session, endReason)
	}
	if duration >= 0 {
		sessionDurationSeconds.WithLabelValues(placement.location, endReason).Observe(duration.Seconds())
	}
	logger.Info("session ended", "reason", endReason, "duration", duration,
		"exitCode", session.Status.ExitCode, "startedAt", session.Status.StartedAt, "endedAt", session.Status.EndedAt)
	return nil
}

// sessionConnectTime is how long the session's client took to connect after
// the session was created, or -1 for a session no client connected to.
func sessionConnectTime(session *computev1alpha.InstanceConsoleSession) time.Duration {
	if session.Status.StartedAt == nil {
		return -1
	}
	return session.Status.StartedAt.Sub(session.CreationTimestamp.Time)
}

// sessionDuration is how long the session's client was connected, or -1 for
// a session no client connected to. The end is the cell's confirmed end when
// it reported one, otherwise when the session's Ready condition turned false,
// and for a session the control plane is ending right now, now.
func sessionDuration(session *computev1alpha.InstanceConsoleSession, now time.Time) time.Duration {
	if session.Status.StartedAt == nil {
		return -1
	}
	end := now
	switch {
	case session.Status.EndedAt != nil:
		end = session.Status.EndedAt.Time
	case sessionEnded(session):
		if cond := apimeta.FindStatusCondition(session.Status.Conditions, computev1alpha.InstanceConsoleSessionReady); cond != nil &&
			!cond.LastTransitionTime.IsZero() {
			end = cond.LastTransitionTime.Time
		}
	}
	return max(end.Sub(session.Status.StartedAt.Time), 0)
}

func (r *InstanceConsoleSessionReconciler) recordCleanupUnconfirmed(
	ctx context.Context,
	projectClient client.Client,
	session *computev1alpha.InstanceConsoleSession,
) error {
	note := fmt.Sprintf("The cell did not confirm within %s that the session's command stopped.", r.cleanupTimeout())
	_, err := r.recordOnce(ctx, projectClient, session, EventReasonCleanupUnconfirmed, func() error {
		return r.recordSessionEvent(ctx, projectClient, session, corev1.EventTypeWarning, EventReasonCleanupUnconfirmed, note, nil)
	})
	return err
}

// recordOnce records a session's event of the given reason at most once, even
// though the project events store accepts two events with the same name and
// the session is reconciled many times around its end. It reports whether
// this call put the event on record, which happens once per session and so
// is when the session's metrics are observed.
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
) (bool, error) {
	if entry := sessionRecordedEvent(session, reason); entry != nil && entry.Recorded {
		return false, nil
	}

	setRecordedEvent(session, reason, r.now(), false)
	if err := projectClient.Status().Update(ctx, session); err != nil {
		return false, fmt.Errorf("failed claiming the %s event: %w", reason, err)
	}

	if err := record(); err != nil {
		return false, err
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
		return false, fmt.Errorf("failed marking the %s event recorded: %w", reason, err)
	}
	return true, nil
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
	// The cell and location let support go from the activity event to the
	// cell's logs, and the session UID correlates the event with them.
	placement := placementOf(session)
	annotations := map[string]string{
		computev1alpha.InstanceConsoleSessionRequesterAnnotation: session.Annotations[computev1alpha.InstanceConsoleSessionRequesterAnnotation],
		computev1alpha.InstanceConsoleSessionCellAnnotation:      placement.cell,
		computev1alpha.InstanceConsoleSessionLocationAnnotation:  placement.location,
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
