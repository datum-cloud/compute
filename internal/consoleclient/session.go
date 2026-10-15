// SPDX-License-Identifier: AGPL-3.0-only

package consoleclient

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
)

// ErrClosed is how a session ends when its host closed it.
var ErrClosed = errors.New("closed")

// Handlers are a host's callbacks for one session. Output and Connected may be
// nil.
type Handlers struct {
	// Connected is called once the stream is established, before any output,
	// and never after End.
	Connected func()
	// Output receives the command's output; fd is 1 for standard output and 2
	// for standard error.
	Output func(p []byte, fd int)
	// End is called exactly once, after the session's private key has been
	// zeroed and its tunnel shut down.
	End func(Result, error)
}

type op func(*Stream) error

// Session runs one connection for a host that must never block on the
// network, such as a browser's event loop. Write, Resize and Close return at
// once; the session sends input in order in the background.
type Session struct {
	key        ed25519.PrivateKey
	sessionUID string
	connection Connection
	handlers   Handlers
	connect    func(context.Context, ed25519.PrivateKey, string, Connection) (*Stream, error)

	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	queue []op
	wake  chan struct{}
}

// NewSession prepares a session that uses key and zeroes it when it ends.
// Call Run to connect.
func NewSession(key ed25519.PrivateKey, sessionUID string, c Connection, h Handlers) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		key:        key,
		sessionUID: sessionUID,
		connection: c,
		handlers:   h,
		connect:    Connect,
		ctx:        ctx,
		cancel:     cancel,
		wake:       make(chan struct{}, 1),
	}
}

// Write queues p for the command's standard input.
func (s *Session) Write(p []byte) {
	data := append([]byte(nil), p...)
	s.enqueue(func(st *Stream) error {
		_, err := st.Write(data)
		return err
	})
}

// Resize queues a terminal size change.
func (s *Session) Resize(cols, rows int) {
	s.enqueue(func(st *Stream) error {
		return st.Resize(uint16(cols), uint16(rows))
	})
}

// Close ends the session. The platform stops the command, and End follows
// once the key is zeroed.
func (s *Session) Close() {
	s.cancel()
}

func (s *Session) enqueue(o op) {
	s.mu.Lock()
	s.queue = append(s.queue, o)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run connects, relays until the session ends, and then calls End.
func (s *Session) Run() {
	res, err := s.run()
	clear(s.key)
	if err != nil && s.ctx.Err() != nil {
		err = ErrClosed
	}
	s.cancel()
	if s.handlers.End != nil {
		s.handlers.End(res, err)
	}
}

func (s *Session) run() (Result, error) {
	if len(s.key) == 0 {
		return Result{}, errors.New("no session key")
	}
	st, err := s.connect(s.ctx, s.key, s.sessionUID, s.connection)
	if err != nil {
		return Result{}, err
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-s.ctx.Done():
			_ = st.Close()
		case <-stopped:
		}
	}()
	defer func() {
		close(stopped)
		_ = st.Close()
	}()

	if s.handlers.Connected != nil {
		s.handlers.Connected()
	}
	go s.drain(st)
	return st.Wait(outputWriter{s.handlers.Output, 1}, outputWriter{s.handlers.Output, 2})
}

func (s *Session) drain(st *Stream) {
	for {
		s.mu.Lock()
		ops := s.queue
		s.queue = nil
		s.mu.Unlock()
		for _, o := range ops {
			if err := o(st); err != nil {
				return
			}
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		}
	}
}

type outputWriter struct {
	f  func([]byte, int)
	fd int
}

func (w outputWriter) Write(p []byte) (int, error) {
	if w.f != nil {
		w.f(p, w.fd)
	}
	return len(p), nil
}
