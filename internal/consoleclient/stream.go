// SPDX-License-Identifier: AGPL-3.0-only

// Package consoleclient connects to an instance console session: it opens the
// tunnel to the session's endpoint and runs the command's stream through it.
// The CLI and the browser build share it, so it must stay free of Kubernetes
// client dependencies.
package consoleclient

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"go.datum.net/compute/internal/consolesession"
)

const (
	channelStdin  = 0
	channelStdout = 1
	channelStderr = 2
	channelStatus = 3
	channelResize = 4
	channelClose  = 255

	maxFrameSize = 16 << 20

	// maxStdinMessage is the most standard input one message carries. The
	// Kubernetes apiserver reads each WebSocket frame as a whole message, so a
	// fragmented message loses every frame after its first. Messages up to this
	// size fit the write buffer and go out as a single frame.
	maxStdinMessage = 32 << 10

	outputQueue = 64

	statusSuccess         = "Success"
	statusFailure         = "Failure"
	reasonNonZeroExitCode = "NonZeroExitCode"
	causeExitCode         = "ExitCode"
)

var keepaliveInterval = 5 * time.Second

// Result is how a session ended. Reason is empty when the command exited on its
// own, and ExitCode then carries its code. Otherwise the platform ended the
// session: Reason is one of the session's terminal condition reasons and
// Message explains it to the user.
type Result struct {
	ExitCode int
	Reason   string
	Message  string
}

// PlatformEnded reports whether the platform ended the session rather than the
// command exiting.
func (r Result) PlatformEnded() bool {
	return r.Reason != ""
}

// RefusedError is the agent turning the connection down before the stream
// starts.
type RefusedError struct {
	StatusCode int
	Body       string
}

func (e *RefusedError) Error() string {
	var why string
	switch e.StatusCode {
	case http.StatusForbidden:
		why = "the session does not accept this client's key"
	case http.StatusNotFound:
		why = "the session is not served here"
	case http.StatusConflict:
		why = "the session is already in use or not ready yet"
	case http.StatusGone:
		why = "the session expired, was revoked, or was not connected in time"
	case http.StatusServiceUnavailable:
		why = "the platform is under maintenance; start a new session"
	default:
		why = "unexpected response " + strconv.Itoa(e.StatusCode)
	}
	if e.Body != "" {
		return fmt.Sprintf("connection refused: %s: %s", why, e.Body)
	}
	return "connection refused: " + why
}

// Stream is the command's input and output for one session.
type Stream struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
}

// Start requests the session's stream over conn, a connection that already
// reaches the agent at target, proving possession of key.
func Start(ctx context.Context, conn net.Conn, key ed25519.PrivateKey, sessionUID, target string) (*Stream, error) {
	ts, sig := consolesession.Sign(key, sessionUID, time.Now())
	header := http.Header{}
	header.Set(consolesession.HeaderTimestamp, ts)
	header.Set(consolesession.HeaderSignature, sig)

	dialer := websocket.Dialer{
		NetDialContext:   func(context.Context, string, string) (net.Conn, error) { return conn, nil },
		Subprotocols:     []string{consolesession.SubProtocol},
		HandshakeTimeout: 20 * time.Second,
		WriteBufferSize:  maxStdinMessage + 1,
	}
	u := url.URL{Scheme: "ws", Host: target, Path: consolesession.ExecPath(sessionUID)}
	ws, resp, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return nil, &RefusedError{StatusCode: resp.StatusCode, Body: string(body)}
		}
		return nil, fmt.Errorf("open stream: %w", err)
	}
	if ws.Subprotocol() != consolesession.SubProtocol {
		_ = ws.Close()
		return nil, fmt.Errorf("agent speaks %q, want %q", ws.Subprotocol(), consolesession.SubProtocol)
	}
	ws.SetReadLimit(maxFrameSize)
	s := &Stream{ws: ws}
	ws.SetPingHandler(func(data string) error {
		_ = s.pong([]byte(data))
		return nil
	})
	return s, nil
}

// Write sends p to the command's standard input.
func (s *Stream) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), maxStdinMessage)]
		if err := s.send(channelStdin, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

// CloseStdin tells the command its standard input has ended.
func (s *Stream) CloseStdin() error {
	return s.send(channelClose, []byte{channelStdin})
}

// Resize sets the command's terminal size.
func (s *Stream) Resize(width, height uint16) error {
	b, err := json.Marshal(struct{ Width, Height uint16 }{width, height})
	if err != nil {
		return err
	}
	return s.send(channelResize, b)
}

// Close drops the stream. The agent stops the command.
func (s *Stream) Close() error {
	return s.ws.Close()
}

func (s *Stream) send(channel byte, data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.ws.WriteMessage(websocket.BinaryMessage, append([]byte{channel}, data...))
}

// Wait copies the command's output until the session ends and reports how it
// ended. An error means the stream broke before the agent reported an ending.
//
// Wait keeps reading while stdout or stderr is slow, so the agent's pings are
// answered and a paused terminal does not end the session.
func (s *Stream) Wait(stdout, stderr io.Writer) (Result, error) {
	out := make(chan []byte, outputQueue)
	delivered := make(chan error, 1)
	go func() { delivered <- s.deliver(out, stdout, stderr) }()
	res, err := s.read(out)
	close(out)
	if werr := <-delivered; werr != nil {
		return Result{}, werr
	}
	return res, err
}

func (s *Stream) read(out chan<- []byte) (Result, error) {
	for {
		_, msg, err := s.ws.ReadMessage()
		if err != nil {
			return Result{}, fmt.Errorf("stream closed before the session ended: %w", err)
		}
		if len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case channelStdout, channelStderr:
			s.enqueue(out, msg)
		case channelStatus:
			res, err := parseStatus(msg[1:])
			s.closeNormally()
			return res, err
		}
	}
}

// enqueue hands msg to the output writer. While the writer is blocked, reading
// stops, so unsolicited pongs tell the agent this client is still here.
func (s *Stream) enqueue(out chan<- []byte, msg []byte) {
	select {
	case out <- msg:
		return
	default:
	}
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	for {
		select {
		case out <- msg:
			return
		case <-t.C:
			_ = s.pong(nil)
		}
	}
}

func (s *Stream) deliver(out <-chan []byte, stdout, stderr io.Writer) error {
	var failed error
	for msg := range out {
		if failed != nil {
			continue
		}
		w := stdout
		if msg[0] == channelStderr {
			w = stderr
		}
		if _, err := w.Write(msg[1:]); err != nil {
			failed = err
			_ = s.ws.Close()
		}
	}
	return failed
}

// pong answers a ping, or tells the agent the client is still here when data
// is nil. It shares the write lock so it never interleaves with a message.
func (s *Stream) pong(data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.ws.WriteControl(websocket.PongMessage, data, time.Now().Add(time.Second))
}

func (s *Stream) closeNormally() {
	s.writeMu.Lock()
	_ = s.ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	s.writeMu.Unlock()
	_ = s.ws.Close()
}

type status struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Details *struct {
		Causes []struct {
			Type    string `json:"reason"`
			Message string `json:"message"`
		} `json:"causes"`
	} `json:"details"`
}

func parseStatus(b []byte) (Result, error) {
	var st status
	if err := json.Unmarshal(b, &st); err != nil {
		return Result{}, fmt.Errorf("malformed session ending: %w", err)
	}
	switch {
	case st.Status == statusSuccess:
		return Result{}, nil
	case st.Status != statusFailure:
		return Result{}, fmt.Errorf("unknown session ending status %q", st.Status)
	case st.Reason == reasonNonZeroExitCode:
		if st.Details != nil {
			for _, c := range st.Details.Causes {
				if c.Type != causeExitCode {
					continue
				}
				code, err := strconv.Atoi(c.Message)
				if err != nil {
					return Result{}, fmt.Errorf("malformed exit code %q", c.Message)
				}
				return Result{ExitCode: code}, nil
			}
		}
		return Result{}, errors.New("command failed without an exit code")
	case st.Reason == "":
		return Result{}, fmt.Errorf("session ended without a reason: %s", st.Message)
	default:
		return Result{ExitCode: 1, Reason: st.Reason, Message: st.Message}, nil
	}
}
