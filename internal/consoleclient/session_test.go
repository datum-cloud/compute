// SPDX-License-Identifier: AGPL-3.0-only

package consoleclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type recorded struct {
	mu        sync.Mutex
	events    []string
	output    bytes.Buffer
	keyZeroed bool
	res       Result
	err       error
	ended     chan struct{}
}

func record(key ed25519.PrivateKey) (*recorded, Handlers) {
	r := &recorded{ended: make(chan struct{})}
	return r, Handlers{
		Connected: func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.events = append(r.events, "connected")
		},
		Output: func(p []byte, fd int) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if len(r.events) == 0 || r.events[len(r.events)-1] != "output" {
				r.events = append(r.events, "output")
			}
			r.output.Write(p)
		},
		End: func(res Result, err error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.events = append(r.events, "end")
			r.keyZeroed = bytes.Equal(key, make([]byte, len(key)))
			r.res, r.err = res, err
			close(r.ended)
		},
	}
}

func (r *recorded) wait(t *testing.T) {
	t.Helper()
	select {
	case <-r.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("End never called")
	}
}

func (r *recorded) check(t *testing.T, events string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if got := strings.Join(r.events, ","); got != events {
		t.Errorf("events = %s, want %s", got, events)
	}
	if !r.keyZeroed {
		t.Error("key was not zeroed before End")
	}
}

func scriptedSession(t *testing.T, script func(*websocket.Conn, <-chan struct{}, <-chan []byte)) (*Session, *recorded) {
	t.Helper()
	key := newKey(t)
	srv := httptest.NewServer(&scriptedAgent{t: t, script: script})
	t.Cleanup(srv.Close)
	target := strings.TrimPrefix(srv.URL, "http://")

	r, h := record(key)
	s := NewSession(key, testSessionUID, Connection{Target: target}, h)
	s.connect = func(ctx context.Context, k ed25519.PrivateKey, uid string, c Connection) (*Stream, error) {
		conn, err := net.Dial("tcp", c.Target)
		if err != nil {
			return nil, err
		}
		return Start(ctx, conn, k, uid, c.Target)
	}
	return s, r
}

func TestSessionExit(t *testing.T) {
	s, r := scriptedSession(t, func(ws *websocket.Conn, _ <-chan struct{}, stdin <-chan []byte) {
		send(t, ws, channelStdout, "ready\n")
		send(t, ws, channelStdout, string(<-stdin))
		send(t, ws, channelStatus, `{"status":"Success"}`)
	})
	s.Resize(80, 24)
	s.Write([]byte("typed"))
	go s.Run()
	r.wait(t)
	r.check(t, "connected,output,end")
	if r.err != nil || r.res != (Result{}) || r.output.String() != "ready\ntyped" {
		t.Errorf("ended with %+v, %v, output %q", r.res, r.err, r.output.String())
	}
}

func TestSessionClose(t *testing.T) {
	s, r := scriptedSession(t, func(ws *websocket.Conn, _ <-chan struct{}, stdin <-chan []byte) {
		send(t, ws, channelStdout, "ready\n")
		for range stdin {
		}
	})
	go s.Run()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		n := r.output.Len()
		r.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Close()
	r.wait(t)
	r.check(t, "connected,output,end")
	if !errors.Is(r.err, ErrClosed) {
		t.Errorf("End error = %v, want ErrClosed", r.err)
	}
}

func TestSessionConnectError(t *testing.T) {
	key := newKey(t)
	r, h := record(key)
	s := NewSession(key, testSessionUID, Connection{RelayURLs: []string{"http://relay"}}, h)
	go s.Run()
	r.wait(t)
	r.check(t, "end")
	if r.err == nil || !strings.Contains(r.err.Error(), "not encrypted") {
		t.Errorf("End error = %v, want the plain relay refused", r.err)
	}
}

func TestSessionWithoutConnectedHandler(t *testing.T) {
	s, r := scriptedSession(t, func(ws *websocket.Conn, _ <-chan struct{}, _ <-chan []byte) {
		send(t, ws, channelStatus, `{"status":"Success"}`)
	})
	s.handlers.Connected = nil
	go s.Run()
	r.wait(t)
	r.check(t, "end")
}

// Closing a session tells the agent the user closed it, so the session ends as
// ClosedByUser rather than as a lost connection.
func TestSessionCloseTellsTheAgent(t *testing.T) {
	key := newKey(t)
	closeCodes := make(chan int, 1)
	greeted := make(chan struct{})
	agent := &scriptedAgent{
		t: t,
		script: func(ws *websocket.Conn, _ <-chan struct{}, stdin <-chan []byte) {
			send(t, ws, channelStdout, "ready\n")
			close(greeted)
			for range stdin {
			}
		},
		onClose: func(ws *websocket.Conn, code int) error {
			<-greeted
			closeCodes <- code
			_ = ws.WriteMessage(websocket.BinaryMessage, append([]byte{channelStatus},
				`{"status":"Failure","reason":"ClosedByUser","message":"The session was closed and the command was stopped."}`...))
			return ws.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		},
	}
	srv := httptest.NewServer(agent)
	t.Cleanup(srv.Close)
	target := strings.TrimPrefix(srv.URL, "http://")
	r, h := record(key)
	s := NewSession(key, testSessionUID, Connection{Target: target}, h)
	s.connect = func(ctx context.Context, k ed25519.PrivateKey, uid string, c Connection) (*Stream, error) {
		conn, err := net.Dial("tcp", c.Target)
		if err != nil {
			return nil, err
		}
		return Start(ctx, conn, k, uid, c.Target)
	}

	go s.Run()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		n := r.output.Len()
		r.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Close()
	r.wait(t)

	select {
	case code := <-closeCodes:
		if code != websocket.CloseNormalClosure {
			t.Errorf("close code = %d, want %d", code, websocket.CloseNormalClosure)
		}
	default:
		t.Fatal("the agent never received a close frame, so it would record a lost connection")
	}
	if r.err != nil || r.res.Reason != "ClosedByUser" {
		t.Errorf("ended with %+v, %v; want the agent's ClosedByUser ending", r.res, r.err)
	}
}
