// SPDX-License-Identifier: AGPL-3.0-only

package consoleclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
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

const testSessionUID = "0b6e4c8e-3a6f-4c55-9d7e-7f4c1d2a9e10"

type fakeAgent struct {
	t         *testing.T
	publicKey string
	refuse    int
	ending    string
	output    string

	mu      sync.Mutex
	stdin   bytes.Buffer
	resizes []string
}

func (a *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a.refuse != 0 {
		http.Error(w, "refused", a.refuse)
		return
	}
	if r.URL.Path != consolesession.ExecPath(testSessionUID) {
		http.NotFound(w, r)
		return
	}
	err := consolesession.Verify(a.publicKey, testSessionUID,
		r.Header.Get(consolesession.HeaderTimestamp), r.Header.Get(consolesession.HeaderSignature),
		time.Now(), consolesession.ClockSkew)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	up := websocket.Upgrader{Subprotocols: []string{consolesession.SubProtocol}}
	ws, err := up.Upgrade(w, r, nil)
	if err != nil {
		a.t.Errorf("upgrade: %v", err)
		return
	}
	defer func() { _ = ws.Close() }()

	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			a.t.Errorf("read client frame: %v", err)
			return
		}
		if len(msg) == 0 {
			continue
		}
		a.mu.Lock()
		switch msg[0] {
		case channelStdin:
			a.stdin.Write(msg[1:])
		case channelResize:
			a.resizes = append(a.resizes, string(msg[1:]))
		}
		a.mu.Unlock()
		if msg[0] == channelClose && len(msg) == 2 && msg[1] == channelStdin {
			break
		}
	}

	_ = ws.WriteMessage(websocket.BinaryMessage, append([]byte{channelStdout}, a.output...))
	_ = ws.WriteMessage(websocket.BinaryMessage, append([]byte{channelStderr}, "warn"...))
	_ = ws.WriteMessage(websocket.BinaryMessage, append([]byte{channelStatus}, a.ending...))
	_ = ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func dialAgent(t *testing.T, a *fakeAgent) (net.Conn, string) {
	t.Helper()
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	target := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.Dial("tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	return conn, target
}

func TestStreamEndings(t *testing.T) {
	cases := []struct {
		name   string
		ending string
		want   Result
	}{
		{
			name:   "exit zero",
			ending: `{"status":"Success"}`,
			want:   Result{},
		},
		{
			name: "non-zero exit",
			ending: `{"status":"Failure","reason":"NonZeroExitCode",` +
				`"details":{"causes":[{"reason":"ExitCode","message":"42"}]}}`,
			want: Result{ExitCode: 42},
		},
		{
			name:   "platform ending",
			ending: `{"status":"Failure","reason":"Expired","message":"The session expired."}`,
			want:   Result{ExitCode: 1, Reason: "Expired", Message: "The session expired."},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := newKey(t)
			agent := &fakeAgent{t: t, publicKey: consolesession.PublicKey(key), ending: tc.ending, output: "hello"}
			conn, target := dialAgent(t, agent)

			s, err := Start(context.Background(), conn, key, testSessionUID, target)
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			if err := s.Resize(120, 40); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Write([]byte("input")); err != nil {
				t.Fatal(err)
			}
			if err := s.CloseStdin(); err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer
			got, err := s.Wait(&stdout, &stderr)
			if err != nil {
				t.Fatalf("Wait() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("Wait() = %+v, want %+v", got, tc.want)
			}
			if got.PlatformEnded() != (tc.want.Reason != "") {
				t.Errorf("PlatformEnded() = %v", got.PlatformEnded())
			}
			if stdout.String() != "hello" || stderr.String() != "warn" {
				t.Errorf("output = %q, %q; want %q, %q", stdout.String(), stderr.String(), "hello", "warn")
			}
			agent.mu.Lock()
			defer agent.mu.Unlock()
			if agent.stdin.String() != "input" {
				t.Errorf("agent stdin = %q, want %q", agent.stdin.String(), "input")
			}
			if len(agent.resizes) != 1 || agent.resizes[0] != `{"Width":120,"Height":40}` {
				t.Errorf("agent resizes = %q", agent.resizes)
			}
		})
	}
}

func TestStreamWrongKeyIsRefused(t *testing.T) {
	agent := &fakeAgent{t: t, publicKey: consolesession.PublicKey(newKey(t))}
	conn, target := dialAgent(t, agent)

	_, err := Start(context.Background(), conn, newKey(t), testSessionUID, target)
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.StatusCode != http.StatusForbidden {
		t.Fatalf("Start() error = %v, want a 403 refusal", err)
	}
}

func TestRefusals(t *testing.T) {
	for code, want := range map[int]string{
		http.StatusNotFound:           "not served here",
		http.StatusConflict:           "already in use",
		http.StatusGone:               "expired",
		http.StatusServiceUnavailable: "start a new session",
	} {
		key := newKey(t)
		agent := &fakeAgent{t: t, publicKey: consolesession.PublicKey(key), refuse: code}
		conn, target := dialAgent(t, agent)

		_, err := Start(context.Background(), conn, key, testSessionUID, target)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Start() against %d error = %v, want it to mention %q", code, err, want)
		}
	}
}

func TestStreamBrokenBeforeEnding(t *testing.T) {
	key := newKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{Subprotocols: []string{consolesession.SubProtocol}}
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = ws.Close()
	}))
	t.Cleanup(srv.Close)
	target := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.Dial("tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Start(context.Background(), conn, key, testSessionUID, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Wait(&bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("Wait() = nil error, want the broken stream reported")
	}
}

func TestParseStatusRejectsUnknownShapes(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`{"status":"Maybe"}`,
		`{"status":"Failure","reason":"NonZeroExitCode"}`,
		`{"status":"Failure"}`,
	} {
		if _, err := parseStatus([]byte(body)); err == nil {
			t.Errorf("parseStatus(%s) = nil error", body)
		}
	}
}

func TestConnectThrough(t *testing.T) {
	cases := []struct {
		name    string
		reply   string
		wantErr bool
	}{
		{"accepted with early bytes", "HTTP/1.1 200 OK\r\n\r\nearly", false},
		{"refused", "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 4\r\n\r\ngone", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			go func() {
				defer func() { _ = server.Close() }()
				req, err := http.ReadRequest(bufio.NewReader(server))
				if err != nil || req.Method != http.MethodConnect || req.Host != "agent:7777" {
					t.Errorf("proxy got %v, %v", req, err)
					return
				}
				_, _ = server.Write([]byte(tc.reply))
			}()

			conn, err := connectThrough(client, "agent:7777")
			if (err != nil) != tc.wantErr {
				t.Fatalf("connectThrough() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			buf := make([]byte, 5)
			if _, err := conn.Read(buf); err != nil || string(buf) != "early" {
				t.Errorf("Read() = %q, %v; want the bytes after the response", buf, err)
			}
		})
	}
}
