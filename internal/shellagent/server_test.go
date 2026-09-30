// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/consolesession"
)

// serve starts the agent's exec endpoint on real time, so the connection
// deadline and the signature window follow the wall clock.
func serve(t *testing.T, h *harness, a *Agent) string {
	t.Helper()
	a.now = time.Now
	h.clock.t = time.Now()
	srv := httptest.NewServer(a.Handler(h.ctx))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// readySession creates a session and has the agent claim it.
func readySession(t *testing.T, h *harness, a *Agent, mutate ...func(*computev1alpha.InstanceConsoleSession)) ed25519.PrivateKey {
	t.Helper()
	key := h.session(testSession, testUID, mutate...)
	h.reconcile(a, testSession)
	requireReason(t, h.cellSession(testSession), computev1alpha.InstanceConsoleSessionReasonSessionReady)
	return key
}

func TestRefusals(t *testing.T) {
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	cases := []struct {
		name  string
		setup func(t *testing.T, h *harness, a *Agent) (uid string, key ed25519.PrivateKey, at time.Time)
		want  int
	}{
		{"unknown session", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			return "no-such-uid", otherKey, time.Now()
		}, http.StatusNotFound},
		{"wrong key", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			readySession(t, h, a)
			return testUID, otherKey, time.Now()
		}, http.StatusForbidden},
		{"stale signature", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			key := readySession(t, h, a)
			return testUID, key, time.Now().Add(-consolesession.ClockSkew - 5*time.Second)
		}, http.StatusForbidden},
		{"no signature", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			readySession(t, h, a)
			return testUID, nil, time.Now()
		}, http.StatusForbidden},
		{"claimed by another agent", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			key := readySession(t, h, h.agent())
			return testUID, key, time.Now()
		}, http.StatusNotFound},
		{"not yet ready", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			key := h.session(testSession, testUID)
			return testUID, key, time.Now()
		}, http.StatusConflict},
		{"already connected", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			key := readySession(t, h, a)
			markConnected(h, testSession, time.Now().Add(time.Hour))
			return testUID, key, time.Now()
		}, http.StatusConflict},
		{"completed", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			key := readySession(t, h, a)
			endAs(h, computev1alpha.InstanceConsoleSessionReasonCompleted)
			return testUID, key, time.Now()
		}, http.StatusConflict},
		{"revoked", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			key := readySession(t, h, a)
			endAs(h, computev1alpha.InstanceConsoleSessionReasonRevoked)
			return testUID, key, time.Now()
		}, http.StatusGone},
		{"revoke requested", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			key := readySession(t, h, a)
			revokeSession(t, h)
			return testUID, key, time.Now()
		}, http.StatusGone},
		{"being deleted", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			key := readySession(t, h, a)
			if err := h.cell.Delete(h.ctx, h.cellSession(testSession)); err != nil {
				t.Fatal(err)
			}
			return testUID, key, time.Now()
		}, http.StatusGone},
		{"expired", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			key := readySession(t, h, a)
			endAs(h, computev1alpha.InstanceConsoleSessionReasonExpired)
			return testUID, key, time.Now()
		}, http.StatusGone},
		{"past connectBefore", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			key := readySession(t, h, a)
			s := h.cellSession(testSession)
			s.Status.ConnectBefore = &metav1.Time{Time: time.Now().Add(-time.Second)}
			if err := h.cell.Status().Update(h.ctx, s); err != nil {
				t.Fatal(err)
			}
			return testUID, key, time.Now()
		}, http.StatusGone},
		{"draining", func(t *testing.T, h *harness, a *Agent) (string, ed25519.PrivateKey, time.Time) {
			key := readySession(t, h, a)
			a.draining.Store(true)
			return testUID, key, time.Now()
		}, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			a := h.agent()
			addr := serve(t, h, a)
			uid, key, at := tc.setup(t, h, a)

			c := dialSession(t, addr, uid, key, at)

			if c.status != tc.want {
				t.Fatalf("status = %d (%s), want %d", c.status, c.body, tc.want)
			}
		})
	}
}

func TestRefusesOtherSubprotocols(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	addr := serve(t, h, a)
	key := readySession(t, h, a)
	ts, sig := consolesession.Sign(key, testUID, time.Now())

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+consolesession.ExecPath(testUID), nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Protocol", "v4.channel.k8s.io")
	req.Header.Set(consolesession.HeaderTimestamp, ts)
	req.Header.Set(consolesession.HeaderSignature, sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// scriptedExit has the fake apiserver echo stdin and exit with code.
func scriptedExit(code int) func(conn net.Conn, r *bufio.Reader) {
	return func(conn net.Conn, r *bufio.Reader) {
		writeServerFrame(conn, channelStdout, []byte("hello"))
		f, err := readFrame(r)
		if err != nil {
			return
		}
		writeServerFrame(conn, channelStdout, f.payload[1:])
		writeExitStatus(conn, code)
	}
}

func TestClosingStatus(t *testing.T) {
	cases := []struct {
		name      string
		exit      int
		wantState string
		wantCause string
	}{
		{"normal exit", 0, metav1.StatusSuccess, ""},
		{"non-zero exit", 3, metav1.StatusFailure, "3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			a := h.agent()
			addr := serve(t, h, a)
			key := readySession(t, h, a)
			h.exec.onSession = scriptedExit(tc.exit)

			c := dialSession(t, addr, testUID, key, time.Now())
			if c.status != http.StatusSwitchingProtocols {
				t.Fatalf("status = %d (%s)", c.status, c.body)
			}
			c.send(t, opBinary, append([]byte{channelStdin}, " world"...))
			out, status, code := c.readUntilClose(t)

			if out[channelStdout] != "hello world" {
				t.Fatalf("stdout = %q", out[channelStdout])
			}
			if status == nil || status.Status != tc.wantState || code != closeNormal {
				t.Fatalf("status = %+v, close code %d", status, code)
			}
			if tc.wantCause != "" {
				if status.Reason != ReasonNonZeroExitCode || status.Details == nil ||
					status.Details.Causes[0].Type != "ExitCode" || status.Details.Causes[0].Message != tc.wantCause {
					t.Fatalf("status = %+v, want NonZeroExitCode with cause %s", status, tc.wantCause)
				}
			}
			s := waitForEnd(t, h)
			requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonCompleted)
			if s.Status.ExitCode == nil || int(*s.Status.ExitCode) != tc.exit || s.Status.StartedAt == nil ||
				s.Status.ExpiresAt == nil {
				t.Fatalf("status = %+v", s.Status)
			}
			if h.exec.ran(killScript) != 1 || len(h.slots()) != 0 {
				t.Fatal("process group was not stopped, or the slot was kept")
			}
		})
	}
}

func TestPlatformEndings(t *testing.T) {
	cases := []struct {
		name   string
		ttl    time.Duration
		end    func(a *Agent)
		reason string
	}{
		{"revoked", time.Hour, func(a *Agent) {
			a.stop(testUID, computev1alpha.InstanceConsoleSessionReasonRevoked)
		}, computev1alpha.InstanceConsoleSessionReasonRevoked},
		{"shutdown", time.Hour, func(a *Agent) {
			a.stop(testUID, computev1alpha.InstanceConsoleSessionReasonAgentShutdown)
		}, computev1alpha.InstanceConsoleSessionReasonAgentShutdown},
		{"expired", 500 * time.Millisecond, func(*Agent) {}, computev1alpha.InstanceConsoleSessionReasonExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			a := h.agent()
			addr := serve(t, h, a)
			key := readySession(t, h, a, func(s *computev1alpha.InstanceConsoleSession) {
				s.Spec.TTL = &metav1.Duration{Duration: tc.ttl}
			})
			h.exec.onSession = func(conn net.Conn, r *bufio.Reader) {
				writeServerFrame(conn, channelStdout, []byte("$ "))
				for {
					if _, err := readFrame(r); err != nil {
						return
					}
				}
			}

			c := dialSession(t, addr, testUID, key, time.Now())
			waitForConnected(t, h)
			_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			first, err := readFrame(c.reader)
			if err != nil || len(first.payload) == 0 || first.payload[0] != channelStdout || string(first.payload[1:]) != "$ " {
				t.Fatalf("first frame = %+v, %v", first, err)
			}
			go tc.end(a)
			out, status, code := c.readUntilClose(t)

			if out[channelStdout] != "" {
				t.Fatalf("stdout after the end = %q", out[channelStdout])
			}
			if status == nil || status.Status != metav1.StatusFailure || string(status.Reason) != tc.reason ||
				status.Message == "" || code != closeNormal {
				t.Fatalf("status = %+v, close code %d", status, code)
			}
			s := waitForEnd(t, h)
			requireReason(t, s, tc.reason)
			if s.Status.ExitCode != nil || h.exec.ran(killScript) != 1 {
				t.Fatalf("exit code %v, kills %d", s.Status.ExitCode, h.exec.ran(killScript))
			}
		})
	}
}

func TestClientDisconnectStopsCommand(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	addr := serve(t, h, a)
	key := readySession(t, h, a)
	h.exec.onSession = func(conn net.Conn, r *bufio.Reader) {
		for {
			if _, err := readFrame(r); err != nil {
				return
			}
		}
	}

	c := dialSession(t, addr, testUID, key, time.Now())
	waitForConnected(t, h)
	_ = c.conn.Close()

	s := waitForEnd(t, h)
	requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonDisconnected)
	if s.Status.ExitCode != nil || h.exec.ran(killScript) != 1 {
		t.Fatal("a dropped connection must stop the command")
	}
}

func TestFailedStopLeavesEndUnconfirmed(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	addr := serve(t, h, a)
	key := readySession(t, h, a)
	h.exec.onSession = func(conn net.Conn, r *bufio.Reader) {
		for {
			if _, err := readFrame(r); err != nil {
				return
			}
		}
	}
	h.exec.mu.Lock()
	h.exec.killExit = 1
	h.exec.mu.Unlock()

	c := dialSession(t, addr, testUID, key, time.Now())
	waitForConnected(t, h)
	_ = c.conn.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !isTerminal(h.cellSession(testSession)) {
		time.Sleep(10 * time.Millisecond)
	}
	s := h.cellSession(testSession)
	requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonDisconnected)
	if s.Status.EndedAt != nil {
		t.Fatal("endedAt set although the command could not be confirmed stopped")
	}
	if len(h.slots()) != 1 {
		t.Fatal("slot released although the command could not be confirmed stopped")
	}
}

func TestOversizedFrameEndsSession(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	addr := serve(t, h, a)
	key := readySession(t, h, a)
	h.exec.onSession = func(conn net.Conn, r *bufio.Reader) {
		for {
			if _, err := readFrame(r); err != nil {
				return
			}
		}
	}

	c := dialSession(t, addr, testUID, key, time.Now())
	head := []byte{0x80 | opBinary, 0x80 | 127, 0, 0, 0, 0, 0x01, 0, 0, 1, 0, 0, 0, 0}
	if _, err := c.conn.Write(head); err != nil {
		t.Fatal(err)
	}
	_, status, _ := c.readUntilClose(t)

	if status == nil || string(status.Reason) != computev1alpha.InstanceConsoleSessionReasonInvalid {
		t.Fatalf("status = %+v", status)
	}
	requireReason(t, waitForEnd(t, h), computev1alpha.InstanceConsoleSessionReasonInvalid)
}

func TestSecondConnectionIsRefused(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	addr := serve(t, h, a)
	key := readySession(t, h, a)
	h.exec.onSession = func(conn net.Conn, r *bufio.Reader) {
		for {
			if _, err := readFrame(r); err != nil {
				return
			}
		}
	}

	first := dialSession(t, addr, testUID, key, time.Now())
	second := dialSession(t, addr, testUID, key, time.Now())

	if first.status != http.StatusSwitchingProtocols || second.status != http.StatusConflict {
		t.Fatalf("statuses = %d, %d; want 101, 409", first.status, second.status)
	}
}

func waitForConnected(t *testing.T, h *harness) {
	t.Helper()
	reason := computev1alpha.InstanceConsoleSessionReasonConnected
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if readyReason(h.cellSession(testSession)) == reason {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	requireReason(t, h.cellSession(testSession), reason)
}

func waitForEnd(t *testing.T, h *harness) *computev1alpha.InstanceConsoleSession {
	name := testSession
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := h.cellSession(name); isTerminal(s) && len(h.slots()) == 0 {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s did not end: %+v", name, h.cellSession(name).Status)
	return nil
}
