// SPDX-License-Identifier: AGPL-3.0-only

package consoleclient

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go.datum.net/compute/internal/consolesession"
)

// scriptedAgent upgrades the stream, keeps reading the client's frames so its
// pongs are seen, and hands the connection to script.
type scriptedAgent struct {
	t      *testing.T
	script func(ws *websocket.Conn, pongs <-chan struct{}, stdin <-chan []byte)
	// onClose, when set, answers the client's close frame in place of the
	// library's default reply.
	onClose func(ws *websocket.Conn, code int) error
}

func (a *scriptedAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{Subprotocols: []string{consolesession.SubProtocol}}
	ws, err := up.Upgrade(w, r, nil)
	if err != nil {
		a.t.Errorf("upgrade: %v", err)
		return
	}
	defer func() { _ = ws.Close() }()

	pongs := make(chan struct{}, 16)
	ws.SetPongHandler(func(string) error {
		select {
		case pongs <- struct{}{}:
		default:
		}
		return nil
	})
	if a.onClose != nil {
		ws.SetCloseHandler(func(code int, _ string) error { return a.onClose(ws, code) })
	}
	stdin := make(chan []byte, 64)
	go func() {
		defer close(stdin)
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if len(msg) > 0 && msg[0] == channelStdin {
				stdin <- msg[1:]
			}
		}
	}()
	a.script(ws, pongs, stdin)
}

func startScripted(t *testing.T, script func(*websocket.Conn, <-chan struct{}, <-chan []byte)) *Stream {
	t.Helper()
	key := newKey(t)
	srv := httptest.NewServer(&scriptedAgent{t: t, script: script})
	t.Cleanup(srv.Close)
	target := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.Dial("tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Start(context.Background(), conn, key, testSessionUID, target)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return s
}

func send(t *testing.T, ws *websocket.Conn, channel byte, data string) {
	t.Helper()
	if err := ws.WriteMessage(websocket.BinaryMessage, append([]byte{channel}, data...)); err != nil {
		t.Errorf("write channel %d: %v", channel, err)
	}
}

func ping(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	if err := ws.WriteControl(websocket.PingMessage, []byte("alive?"), time.Now().Add(time.Second)); err != nil {
		t.Errorf("ping: %v", err)
	}
}

func awaitPong(t *testing.T, pongs <-chan struct{}, when string) {
	t.Helper()
	select {
	case <-pongs:
	case <-time.After(3 * time.Second):
		t.Errorf("no pong %s", when)
	}
}

func TestStreamAnswersPings(t *testing.T) {
	keepaliveInterval = 50 * time.Millisecond
	t.Cleanup(func() { keepaliveInterval = 5 * time.Second })

	release := make(chan struct{})
	stopTyping := make(chan struct{})
	s := startScripted(t, func(ws *websocket.Conn, pongs <-chan struct{}, stdin <-chan []byte) {
		ping(t, ws)
		awaitPong(t, pongs, "while the client waits for output")

		for i := 0; i < outputQueue+4; i++ {
			send(t, ws, channelStdout, "x")
		}
		ping(t, ws)
		awaitPong(t, pongs, "while the client's output is blocked")
		close(release)

		<-stdin
		ping(t, ws)
		awaitPong(t, pongs, "while the client writes input")
		close(stopTyping)

		send(t, ws, channelStatus, `{"status":"Success"}`)
	})

	go func() {
		for {
			select {
			case <-stopTyping:
				return
			default:
			}
			if _, err := s.Write([]byte("typing")); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	stdout := &blockingWriter{release: release}
	got, err := s.Wait(stdout, io.Discard)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if got != (Result{}) {
		t.Errorf("Wait() = %+v, want a clean exit", got)
	}
	if n := stdout.Len(); n != outputQueue+4 {
		t.Errorf("stdout got %d bytes, want %d", n, outputQueue+4)
	}
}

type blockingWriter struct {
	release <-chan struct{}
	mu      sync.Mutex
	buf     bytes.Buffer
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *blockingWriter) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Len()
}

// A session with a terminal sends everything on stdout, and output arrives
// while standard input is still open.
func TestStreamTerminalOutput(t *testing.T) {
	s := startScripted(t, func(ws *websocket.Conn, _ <-chan struct{}, stdin <-chan []byte) {
		send(t, ws, channelStdout, "/ # ")
		var line []byte
		for b := range stdin {
			send(t, ws, channelStdout, string(b))
			line = append(line, b...)
			if bytes.HasSuffix(line, []byte("\r")) {
				break
			}
		}
		send(t, ws, channelStdout, "\r\nhi\r\n/ # ")
		send(t, ws, channelStatus, `{"status":"Success"}`)
	})

	var stdout, stderr lockedBuffer
	done := make(chan struct{})
	var got Result
	var err error
	go func() {
		got, err = s.Wait(&stdout, &stderr)
		close(done)
	}()

	waitFor(t, &stdout, "/ # ")
	for _, key := range []string{"e", "c", "h", "o", " ", "h", "i", "\r"} {
		if _, err := s.Write([]byte(key)); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	if err != nil || got != (Result{}) {
		t.Fatalf("Wait() = %+v, %v", got, err)
	}
	if want := "/ # echo hi\r\r\nhi\r\n/ # "; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want nothing", stderr.String())
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitFor(t *testing.T, b *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(b.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("output %q never showed %q", b.String(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Input, resizes and keepalive pongs sent at once while output is backed up
// must each arrive whole.
func TestKeepaliveInterleavesWithInput(t *testing.T) {
	keepaliveInterval = time.Millisecond
	t.Cleanup(func() { keepaliveInterval = 5 * time.Second })

	const writes = 300
	chunk := strings.Repeat("k", 1000)
	release := make(chan struct{})
	sent := make(chan struct{})
	s := startScripted(t, func(ws *websocket.Conn, pongs <-chan struct{}, stdin <-chan []byte) {
		for i := 0; i < outputQueue+4; i++ {
			send(t, ws, channelStdout, "x")
		}
		got := 0
		for b := range stdin {
			if string(b) != chunk {
				t.Errorf("stdin message %d = %d bytes, want %d", got, len(b), len(chunk))
				return
			}
			if got++; got == writes {
				break
			}
		}
		awaitPong(t, pongs, "while output is backed up")
		<-sent
		close(release)
		send(t, ws, channelStatus, `{"status":"Success"}`)
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			if _, err := s.Write([]byte(chunk)); err != nil {
				t.Errorf("Write() error = %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			if err := s.Resize(uint16(80+i%10), 24); err != nil {
				t.Errorf("Resize() error = %v", err)
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(sent)
	}()

	if _, err := s.Wait(&blockingWriter{release: release}, io.Discard); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}
