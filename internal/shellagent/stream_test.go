// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/consolesession"
)

func fastPings(c *Config) {
	c.PingInterval = 50 * time.Millisecond
	c.PongTimeout = 300 * time.Millisecond
}

// backendRecorder is a fake command that runs until the stream closes and
// records every frame the agent relays to it.
type backendRecorder struct {
	mu     sync.Mutex
	frames []*frame
}

func (b *backendRecorder) run(conn net.Conn, r *bufio.Reader) {
	writeServerFrame(conn, channelStdout, []byte("$ "))
	for {
		f, err := readFrame(r)
		if err != nil {
			return
		}
		b.mu.Lock()
		b.frames = append(b.frames, f)
		b.mu.Unlock()
	}
}

func (b *backendRecorder) pongs() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, f := range b.frames {
		if f.opcode == opPong {
			n++
		}
	}
	return n
}

func TestSilentClientIsDisconnected(t *testing.T) {
	h := newHarness(t)
	a := h.agent(fastPings)
	addr := serve(t, h, a)
	key := readySession(t, h, a)
	h.exec.onSession = (&backendRecorder{}).run

	c := dialSession(t, addr, testUID, key, time.Now())
	waitForConnected(t, h)
	pinged := false
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !pinged {
		f, err := readFrame(c.reader)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		pinged = f.opcode == opPing
	}

	s := waitForEnd(t, h)
	requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonDisconnected)
	if s.Status.ExitCode != nil || h.exec.ran(killScript) != 1 {
		t.Fatal("a client that stopped answering pings must have its command stopped")
	}
}

func TestClientDisconnectEndsAsDisconnected(t *testing.T) {
	h := newHarness(t)
	a := h.agent()
	addr := serve(t, h, a)
	key := readySession(t, h, a)
	h.exec.onSession = (&backendRecorder{}).run

	c := dialSession(t, addr, testUID, key, time.Now())
	waitForConnected(t, h)
	c.send(t, opClose, closePayload(closeNormal))

	requireReason(t, waitForEnd(t, h), computev1alpha.InstanceConsoleSessionReasonDisconnected)
}

func dialGorilla(t *testing.T, addr, uid string, key ed25519.PrivateKey) *websocket.Conn {
	t.Helper()
	ts, sig := consolesession.Sign(key, uid, time.Now())
	header := http.Header{}
	header.Set(consolesession.HeaderTimestamp, ts)
	header.Set(consolesession.HeaderSignature, sig)
	dialer := websocket.Dialer{Subprotocols: []string{consolesession.SubProtocol}}
	conn, resp, err := dialer.Dial("ws://"+addr+consolesession.ExecPath(uid), header)
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// A gorilla/websocket client answers pings from inside its read loop with the
// library's default handler, as datumctl and the browser client do.
func TestAnsweringClientStaysConnected(t *testing.T) {
	h := newHarness(t)
	a := h.agent(fastPings)
	addr := serve(t, h, a)
	key := readySession(t, h, a)
	backend := &backendRecorder{}
	h.exec.onSession = backend.run

	conn := dialGorilla(t, addr, testUID, key)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	waitForConnected(t, h)
	time.Sleep(4 * a.cfg.PongTimeout)

	requireReason(t, h.hubSession(testSession), computev1alpha.InstanceConsoleSessionReasonConnected)
	if n := backend.pongs(); n != 0 {
		t.Fatalf("relayed %d pongs to the agent's own pings to the apiserver", n)
	}
}

// ttyCommand writes a prompt, echoes the first stdin message, and exits 0.
func ttyCommand(conn net.Conn, r *bufio.Reader) {
	writeServerFrame(conn, channelStdout, []byte("prompt$ "))
	for {
		f, err := readFrame(r)
		if err != nil {
			return
		}
		if f.opcode == opBinary && len(f.payload) > 1 && f.payload[0] == channelStdin {
			writeServerFrame(conn, channelStdout, f.payload[1:])
			writeExitStatus(conn, 0)
			return
		}
	}
}

type signingTransport struct {
	key ed25519.PrivateKey
	uid string
	rt  http.RoundTripper
}

func (s *signingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ts, sig := consolesession.Sign(s.key, s.uid, time.Now())
	req = req.Clone(req.Context())
	req.Header.Set(consolesession.HeaderTimestamp, ts)
	req.Header.Set(consolesession.HeaderSignature, sig)
	return s.rt.RoundTrip(req)
}

// client-go's WebSocket executor, as kubectl uses it, reads a terminal
// session's output, and answers the agent's pings.
func TestClientGoExecutor(t *testing.T) {
	for _, tty := range []bool{true, false} {
		t.Run(map[bool]string{true: "tty", false: "no tty"}[tty], func(t *testing.T) {
			h := newHarness(t)
			a := h.agent(fastPings)
			addr := serve(t, h, a)
			key := readySession(t, h, a, func(s *computev1alpha.InstanceConsoleSession) {
				s.Spec.Terminal = tty
			})
			h.exec.onSession = func(conn net.Conn, r *bufio.Reader) {
				time.Sleep(4 * a.cfg.PongTimeout)
				ttyCommand(conn, r)
			}

			cfg := &rest.Config{
				Host: "http://" + addr,
				WrapTransport: func(rt http.RoundTripper) http.RoundTripper {
					return &signingTransport{key: key, uid: testUID, rt: rt}
				},
			}
			exec, err := remotecommand.NewWebSocketExecutor(cfg, http.MethodGet,
				"http://"+addr+consolesession.ExecPath(testUID))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var stdout, stderr bytes.Buffer
			opts := remotecommand.StreamOptions{
				Stdin:  io.MultiReader(strings.NewReader("hi\n"), blockUntil(ctx)),
				Stdout: &stdout,
				Tty:    tty,
			}
			if !tty {
				opts.Stderr = &stderr
			}
			if err := exec.StreamWithContext(ctx, opts); err != nil {
				t.Fatalf("stream: %v", err)
			}
			if got := stdout.String(); got != "prompt$ hi\n" {
				t.Fatalf("stdout = %q", got)
			}
			s := waitForEnd(t, h)
			requireReason(t, s, computev1alpha.InstanceConsoleSessionReasonCompleted)
			if s.Status.ExitCode == nil || *s.Status.ExitCode != 0 {
				t.Fatalf("exit code = %v", s.Status.ExitCode)
			}
		})
	}
}

type blockingReader struct{ ctx context.Context }

func (b blockingReader) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, io.EOF
}

func blockUntil(ctx context.Context) io.Reader { return blockingReader{ctx: ctx} }
