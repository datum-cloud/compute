// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

var errStreamEnded = errors.New("stream ended")

// clientStream owns writes to a client's exec WebSocket. Frames from the
// apiserver are relayed whole, so the agent can write its own messages between
// them without splitting a fragmented message.
type clientStream struct {
	conn net.Conn

	mu         sync.Mutex
	inFragment bool
	ended      bool
	endOnce    sync.Once
}

func (c *clientStream) relayData(raw []byte, fin bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return errStreamEnded
	}
	c.inFragment = !fin
	_, err := c.conn.Write(raw)
	return err
}

func (c *clientStream) relayControl(raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return errStreamEnded
	}
	_, err := c.conn.Write(raw)
	return err
}

// writeBetweenMessages writes frames once no relayed message is half sent,
// waiting at most two seconds for one to finish.
func (c *clientStream) writeBetweenMessages(frames ...[]byte) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		if c.ended {
			c.mu.Unlock()
			return errStreamEnded
		}
		if !c.inFragment || time.Now().After(deadline) {
			var err error
			for _, f := range frames {
				if _, err = c.conn.Write(f); err != nil {
					break
				}
			}
			c.mu.Unlock()
			return err
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
}

// Notify writes a notice to the session's standard output. Only sessions with
// a terminal get notices.
func (c *clientStream) Notify(text string) error {
	return c.writeBetweenMessages(encodeFrame(opBinary, append([]byte{channelStdout}, text...), false))
}

// End sends the session's one status message and closes the stream with
// code 1000.
func (c *clientStream) End(status metav1.Status) {
	c.endOnce.Do(func() {
		body, _ := json.Marshal(status)
		_ = c.writeBetweenMessages(
			encodeFrame(opBinary, append([]byte{channelStatus}, body...), false),
			encodeFrame(opClose, closePayload(closeNormal), false),
		)
		c.mu.Lock()
		c.ended = true
		c.mu.Unlock()
		_ = c.conn.Close()
	})
}

// outcome is how a connected session ended.
type outcome struct {
	reason   string
	exitCode *int32
	message  string
}

var (
	outcomeClientGone = outcome{
		reason:  computev1alpha.InstanceConsoleSessionReasonDisconnected,
		message: endMessage(computev1alpha.InstanceConsoleSessionReasonDisconnected),
	}
	outcomeStreamLost = outcome{
		reason:  computev1alpha.InstanceConsoleSessionReasonInstanceNotRunning,
		message: "The instance stopped the command's stream.",
	}
	outcomeFrameTooLarge = outcome{
		reason:  computev1alpha.InstanceConsoleSessionReasonInvalid,
		message: "The client sent a message larger than 16 MiB.",
	}
	outcomeMalformed = outcome{
		reason:  computev1alpha.InstanceConsoleSessionReasonInvalid,
		message: "The client sent a malformed WebSocket message.",
	}
)

// pingPayload marks the agent's own pings, so the client's pongs to them are
// not relayed to the apiserver.
var pingPayload = []byte("shell-agent")

// relay copies frames both ways until the command exits, either side goes
// away, or ctx ends. It returns nil when ctx ended first. The client is pinged
// every pingInterval and counts as gone once it has sent nothing, not even a
// pong, for pongTimeout.
func relay(ctx context.Context, c *clientStream, fromClient *bufio.Reader, backend net.Conn, fromBackend *bufio.Reader,
	pingInterval, pongTimeout time.Duration) *outcome {
	results := make(chan outcome, 2)
	done := make(chan struct{})
	defer close(done)
	go func() { results <- c.pump(fromBackend) }()
	go func() { results <- c.forward(fromClient, backend, pongTimeout) }()
	go c.ping(pingInterval, done)
	select {
	case o := <-results:
		return &o
	case <-ctx.Done():
		return nil
	}
}

func (c *clientStream) ping(interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := c.relayControl(encodeFrame(opPing, pingPayload, false)); err != nil {
				return
			}
		}
	}
}

// pump relays the apiserver's frames to the client, holding back the status
// message so the agent sends the stream's only one.
func (c *clientStream) pump(r *bufio.Reader) outcome {
	var status []byte
	intercept := false
	for {
		f, err := readFrame(r)
		if err != nil || f.opcode == opClose {
			return outcomeStreamLost
		}
		if f.control() {
			if err := c.relayControl(f.raw); err != nil {
				return outcomeClientGone
			}
			continue
		}
		if f.opcode != opContinuation {
			intercept = len(f.payload) > 0 && f.payload[0] == channelStatus
			if intercept {
				status = append(status[:0], f.payload[1:]...)
			}
		} else if intercept {
			status = append(status, f.payload...)
		}
		if !intercept {
			if err := c.relayData(f.raw, f.fin); err != nil {
				return outcomeClientGone
			}
			continue
		}
		if !f.fin {
			continue
		}
		code, message, ok := exitCodeFromStatus(status)
		if !ok {
			return outcome{
				reason:  computev1alpha.InstanceConsoleSessionReasonInstanceNotRunning,
				message: "The instance could not run the command: " + message,
			}
		}
		return outcome{reason: computev1alpha.InstanceConsoleSessionReasonCompleted, exitCode: &code}
	}
}

// forward relays the client's frames to the apiserver, except pongs to the
// agent's own pings. The apiserver reads only the first frame of a fragmented
// message, so fragmented messages are reassembled and sent as one frame. Any
// frame, including an unsolicited pong, shows the client is still there.
func (c *clientStream) forward(r *bufio.Reader, backend net.Conn, idle time.Duration) outcome {
	var message []byte
	var messageOp byte
	fragmented := false
	for {
		if err := c.conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
			return outcomeClientGone
		}
		f, err := readFrame(r)
		var tooLarge errFrameTooLarge
		switch {
		case errors.As(err, &tooLarge):
			return outcomeFrameTooLarge
		case err != nil, f.opcode == opClose:
			return outcomeClientGone
		case f.opcode == opPong && bytes.Equal(f.payload, pingPayload):
			continue
		}
		out := f.raw
		switch {
		case f.control():
		case f.opcode == opContinuation:
			if !fragmented {
				return outcomeMalformed
			}
			if len(message)+len(f.payload) > MaxFrameSize {
				return outcomeFrameTooLarge
			}
			message = append(message, f.payload...)
			if !f.fin {
				continue
			}
			out = encodeFrame(messageOp, message, true)
			message, fragmented = nil, false
		case fragmented:
			return outcomeMalformed
		case !f.fin:
			message, messageOp, fragmented = append([]byte(nil), f.payload...), f.opcode, true
			continue
		}
		if _, err := backend.Write(out); err != nil {
			return outcomeStreamLost
		}
	}
}
