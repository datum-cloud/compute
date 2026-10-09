// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/codes"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/consolesession"
	"go.datum.net/compute/internal/shelltrace"
)

// Handler serves the connection protocol's exec endpoint.
func (a *Agent) Handler(ctx context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+consolesession.ExecPath("{uid}"), func(w http.ResponseWriter, r *http.Request) {
		a.serveExec(log.IntoContext(r.Context(), log.FromContext(ctx)), w, r)
	})
	return mux
}

func wantsExecStream(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, value := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, protocol := range strings.Split(value, ",") {
			if strings.TrimSpace(protocol) == consolesession.SubProtocol {
				return true
			}
		}
	}
	return false
}

// findSession returns the cell copy of the session with a project UID.
func (a *Agent) findSession(ctx context.Context, uid string) (*computev1alpha.InstanceConsoleSession, error) {
	var sessions computev1alpha.InstanceConsoleSessionList
	if err := a.sessions.List(ctx, &sessions, client.MatchingLabels{computev1alpha.InstanceConsoleSessionUIDLabel: uid}); err != nil {
		return nil, err
	}
	if len(sessions.Items) == 0 {
		return nil, nil
	}
	return &sessions.Items[0], nil
}

//nolint:gocyclo // each refusal is one branch of the protocol, in its order
func (a *Agent) serveExec(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	logger := log.FromContext(ctx).WithValues("session", uid)

	if a.draining.Load() {
		http.Error(w, "The platform is restarting this cell's shell service. Create a new session.", http.StatusServiceUnavailable)
		return
	}
	if !wantsExecStream(r) {
		http.Error(w, "Only "+consolesession.SubProtocol+" WebSocket upgrades are supported.", http.StatusBadRequest)
		return
	}
	cell, err := a.findSession(ctx, uid)
	if err != nil {
		logger.Error(err, "look up session")
		http.Error(w, "The session could not be looked up.", http.StatusInternalServerError)
		return
	}
	if cell == nil {
		http.Error(w, "Session not found.", http.StatusNotFound)
		return
	}
	now := a.now()
	if err := consolesession.Verify(cell.Spec.ClientPublicKey, uid, r.Header.Get(consolesession.HeaderTimestamp),
		r.Header.Get(consolesession.HeaderSignature), now, consolesession.ClockSkew); err != nil {
		logger.Info("refused connection", "reason", err.Error())
		http.Error(w, "Not authorized for this session.", http.StatusForbidden)
		return
	}

	var current computev1alpha.InstanceConsoleSession
	err = a.cell.Get(ctx, client.ObjectKeyFromObject(cell), &current)
	if err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "read session")
		http.Error(w, "The session could not be read.", http.StatusInternalServerError)
		return
	}
	if err != nil || sessionUID(&current) != uid || endpointOf(&current) != "" && endpointOf(&current) != a.EndpointID() {
		http.Error(w, "Session not found.", http.StatusNotFound)
		return
	}
	ctx, connectSpan := shelltrace.StartAgentSpan(ctx, &current, "shell.session.agent.connect")
	defer connectSpan.End()
	if !current.DeletionTimestamp.IsZero() || revoked(&current) {
		http.Error(w, endMessage(computev1alpha.InstanceConsoleSessionReasonRevoked), http.StatusGone)
		return
	}
	if readyReason(&current) != computev1alpha.InstanceConsoleSessionReasonSessionReady || isTerminal(&current) {
		code, msg := refusal(&current)
		http.Error(w, msg, code)
		return
	}
	if current.Status.ConnectBefore != nil && !now.Before(current.Status.ConnectBefore.Time) {
		http.Error(w, endMessage(computev1alpha.InstanceConsoleSessionReasonNotConnected), http.StatusGone)
		return
	}
	held, err := a.slotFor(ctx, uid)
	if err != nil || held == nil {
		http.Error(w, "The session is not ready for a connection.", http.StatusConflict)
		return
	}

	started := metav1.NewTime(now)
	expires := metav1.NewTime(now.Add(sessionTTL(&current)))
	runCtx, cancel := context.WithDeadline(context.Background(), expires.Time)
	defer cancel()
	live, ok := a.register(uid, cancel, cell.Spec.Terminal)
	if !ok {
		http.Error(w, "The session already has a connection.", http.StatusConflict)
		return
	}
	defer func() {
		a.unregister(uid)
		close(live.done)
	}()

	current.Status.StartedAt = &started
	current.Status.ExpiresAt = &expires
	setReady(&current, metav1.ConditionTrue, computev1alpha.InstanceConsoleSessionReasonConnected, "A client is connected.")
	if err := a.cell.Status().Update(ctx, &current); err != nil {
		connectSpan.SetStatus(codes.Error, "session status update failed")
		if apierrors.IsConflict(err) {
			http.Error(w, "The session already has a connection or has ended.", http.StatusConflict)
			return
		}
		logger.Error(err, "record connection")
		http.Error(w, "The session could not be started.", http.StatusInternalServerError)
		return
	}
	connectSpan.End()
	logger.Info("session connected", "pod", held.pod.String(), "container", held.container)

	result := a.runSession(runCtx, w, r, &current, held, live)
	a.finishSession(uid, client.ObjectKeyFromObject(&current), held, result)
	logger.Info("session ended", "reason", result.reason, "exitCode", result.exitCode,
		"duration", a.now().Sub(started.Time).Round(time.Millisecond))
}

// runSession runs the session's command and relays its stream until it ends,
// then sends the client the session's closing status.
func (a *Agent) runSession(ctx context.Context, w http.ResponseWriter, r *http.Request,
	session *computev1alpha.InstanceConsoleSession, held *slot, live *liveSession) outcome {
	uid := sessionUID(session)
	opts := ExecOptions{
		Container: held.container,
		Command:   wrappedCommand(held.markerDir, uid, session.Spec.Command, session.Spec.Stdin, session.Spec.Terminal),
		Stdin:     session.Spec.Stdin,
		TTY:       session.Spec.Terminal,
	}
	execCtx, execSpan := shelltrace.StartAgentSpan(ctx, session, "shell.session.agent.exec_open")
	resp, backend, fromBackend, err := a.exec.Open(execCtx, held.pod, opts, r.Header)
	if err != nil {
		execSpan.SetStatus(codes.Error, "backend exec open failed")
	}
	execSpan.End()
	if err != nil {
		log.FromContext(ctx).Error(err, "open exec stream", "session", uid)
		http.Error(w, "The instance could not start the command.", http.StatusBadGateway)
		return outcome{
			reason:  computev1alpha.InstanceConsoleSessionReasonInstanceNotRunning,
			message: "The instance could not start the command.",
		}
	}
	defer func() { _ = backend.Close() }()

	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return outcomeClientGone
	}
	if err := resp.Write(conn); err != nil {
		_ = conn.Close()
		return outcomeClientGone
	}
	stream := &clientStream{conn: conn}
	a.attach(live, stream)

	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	exited := make(chan int32, 1)
	go a.watchExit(watchCtx, held, uid, sessionTTL(session)+time.Minute, exited)

	result := relay(ctx, stream, rw.Reader, backend, fromBackend, a.cfg.PingInterval, a.cfg.PongTimeout, exited)
	if reason := a.stopReason(uid); reason != "" {
		result = &outcome{reason: reason, message: endMessage(reason)}
	} else if result == nil {
		reason := computev1alpha.InstanceConsoleSessionReasonExpired
		result = &outcome{reason: reason, message: endMessage(reason)}
	}
	stream.End(closingStatus(result.reason, result.exitCode, result.message))
	return *result
}

// watchExit sends the command's exit code once the wrapper records it. The
// exec stream alone cannot say when the command exited: a background job that
// still holds the terminal keeps it open. In a container without sleep the
// watch cannot wait, so the agent checks on its own timer until ctx ends.
// Nothing is sent when the watch ends any other way, and the stream decides
// how the session ends.
func (a *Agent) watchExit(ctx context.Context, held *slot, uid string, limit time.Duration, exited chan<- int32) {
	out, code, err := run(ctx, a.exec, held.pod, held.container,
		exitWatchCommand(held.markerDir, uid, int(limit.Seconds())))
	if err == nil && code == exitWatchNoSleep {
		out, err = a.pollExit(ctx, held, uid)
		code = 0
	}
	if err != nil || code != 0 {
		return
	}
	exitCode, err := strconv.ParseInt(strings.TrimSpace(out), 10, 32)
	if err != nil {
		return
	}
	exited <- int32(exitCode)
}

func (a *Agent) pollExit(ctx context.Context, held *slot, uid string) (string, error) {
	ticker := time.NewTicker(a.cfg.ExitPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
		out, code, err := a.run(ctx, held.pod, held.container, exitCheckCommand(held.markerDir, uid))
		if err == nil && code == 0 {
			return out, nil
		}
	}
}

// finishSession stops the session's processes, records its end, and frees its
// slot once the processes are known to be stopped.
func (a *Agent) finishSession(uid string, key client.ObjectKey, held *slot, result outcome) {
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.KillGrace+30*time.Second)
	defer cancel()
	logger := log.FromContext(ctx).WithValues("session", uid)

	stopped, err := a.stopProcesses(ctx, held)
	if err != nil {
		logger.Error(err, "stop session processes")
	}
	message := result.message
	if message == "" {
		message = "The command exited."
	}
	me := a.EndpointID()
	if _, err := a.end(ctx, key, result.reason, message, result.exitCode, stopped,
		func(s *computev1alpha.InstanceConsoleSession) bool { return endpointOf(s) == me }); err != nil {
		logger.Error(err, "record session end")
	}
	a.forget(uid)
	if stopped {
		if err := a.releaseSlot(ctx, held); err != nil {
			logger.Error(err, "release session slot")
		}
	}
}
