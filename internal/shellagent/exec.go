// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"go.datum.net/compute/internal/consolesession"
)

// ExecOptions is one pods/exec request.
type ExecOptions struct {
	Container string
	Command   []string
	Stdin     bool
	TTY       bool
}

func (o ExecOptions) query() url.Values {
	q := url.Values{}
	for _, c := range o.Command {
		q.Add("command", c)
	}
	q.Set("container", o.Container)
	q.Set("stdout", "true")
	if o.Stdin {
		q.Set("stdin", "true")
	}
	if o.TTY {
		q.Set("tty", "true")
	} else {
		q.Set("stderr", "true")
	}
	return q
}

// Executor opens exec streams into pods.
type Executor interface {
	// Open upgrades a pods/exec request to a v5.channel.k8s.io WebSocket and
	// returns the apiserver's 101 response with the stream. handshake carries
	// the client's WebSocket key when the stream is relayed to a client, so
	// the apiserver's accept header matches it; a nil handshake gets a fresh
	// key.
	Open(ctx context.Context, pod types.NamespacedName, opts ExecOptions, handshake http.Header) (*http.Response, net.Conn, *bufio.Reader, error)
}

// ExecRefusedError is an exec the apiserver answered with something other
// than an upgrade.
type ExecRefusedError struct {
	StatusCode int
	Message    string
}

func (e *ExecRefusedError) Error() string {
	return fmt.Sprintf("exec refused with HTTP %d: %s", e.StatusCode, e.Message)
}

// APIServer opens exec streams against the cell apiserver.
type APIServer struct {
	base      *url.URL
	tlsConfig *tls.Config
	transport http.RoundTripper
}

// NewAPIServer returns an Executor that authenticates to the apiserver the
// way cfg does.
func NewAPIServer(cfg *rest.Config) (*APIServer, error) {
	base, _, err := rest.DefaultServerUrlFor(cfg)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := rest.TLSConfigFor(cfg)
	if err != nil {
		return nil, err
	}
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tlsConfig = tlsConfig.Clone()
	tlsConfig.NextProtos = []string{"http/1.1"}
	transport, err := rest.HTTPWrappersForConfig(cfg, &capture{})
	if err != nil {
		return nil, err
	}
	return &APIServer{base: base, tlsConfig: tlsConfig, transport: transport}, nil
}

// capture ends a round trip at the transport, after the client-go wrappers
// have added credentials, so the authorized request can be written onto a
// connection the agent owns.
type capture struct{}

type capturedRequest struct {
	req *http.Request
}

func (e *capturedRequest) Error() string { return "request captured" }

func (capture) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, &capturedRequest{req: req}
}

func (a *APIServer) authorize(req *http.Request) (*http.Request, error) {
	_, err := a.transport.RoundTrip(req)
	var captured *capturedRequest
	if errors.As(err, &captured) {
		return captured.req, nil
	}
	if err == nil {
		err = errors.New("credentials transport returned a response")
	}
	return nil, err
}

// Open implements Executor.
func (a *APIServer) Open(ctx context.Context, pod types.NamespacedName, opts ExecOptions, handshake http.Header) (*http.Response, net.Conn, *bufio.Reader, error) {
	target := *a.base
	target.Path = strings.TrimSuffix(target.Path, "/") +
		fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/exec", pod.Namespace, pod.Name)
	target.RawQuery = opts.query().Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, nil, nil, err
	}
	setHandshake(req.Header, handshake)
	req, err = a.authorize(req)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("authorize exec request: %w", err)
	}

	host := target.Host
	if target.Port() == "" {
		port := "443"
		if target.Scheme == "http" {
			port = "80"
		}
		host = net.JoinHostPort(target.Hostname(), port)
	}
	var conn net.Conn
	if target.Scheme == "http" {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", host)
	} else {
		conn, err = (&tls.Dialer{Config: a.tlsConfig}).DialContext(ctx, "tcp", host)
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial apiserver: %w", err)
	}
	return upgrade(conn, req)
}

func setHandshake(h, from http.Header) {
	key := from.Get("Sec-WebSocket-Key")
	if key == "" {
		raw := make([]byte, 16)
		_, _ = rand.Read(raw)
		key = base64.StdEncoding.EncodeToString(raw)
	}
	h.Set("Connection", "Upgrade")
	h.Set("Upgrade", "websocket")
	h.Set("Sec-WebSocket-Version", "13")
	h.Set("Sec-WebSocket-Key", key)
	h.Set("Sec-WebSocket-Protocol", consolesession.SubProtocol)
}

func upgrade(conn net.Conn, req *http.Request) (*http.Response, net.Conn, *bufio.Reader, error) {
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, nil, nil, fmt.Errorf("send exec request: %w", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, fmt.Errorf("read exec response: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, nil, nil, &ExecRefusedError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(body))}
	}
	return resp, conn, reader, nil
}

// ExecFailedError is a command the apiserver could not run, as opposed to a
// command that ran and exited.
type ExecFailedError struct {
	Message string
}

func (e *ExecFailedError) Error() string {
	return "exec failed: " + e.Message
}

// run runs a command to completion without stdin and returns its combined
// output and exit code.
func run(ctx context.Context, exec Executor, pod types.NamespacedName, container string, command []string) (string, int, error) {
	_, conn, reader, err := exec.Open(ctx, pod, ExecOptions{Container: container, Command: command}, nil)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var out strings.Builder
	var status []byte
	var channel byte
	for {
		f, err := readFrame(reader)
		if err != nil {
			if ctx.Err() != nil {
				return out.String(), 0, ctx.Err()
			}
			return out.String(), 0, fmt.Errorf("exec stream ended without a status: %w", err)
		}
		switch {
		case f.opcode == opClose:
			return out.String(), 0, errors.New("exec stream closed without a status")
		case f.control():
			continue
		case f.opcode != opContinuation:
			if len(f.payload) == 0 {
				continue
			}
			channel = f.payload[0]
			f.payload = f.payload[1:]
		}
		switch channel {
		case channelStdout, channelStderr:
			out.Write(f.payload)
		case channelStatus:
			status = append(status, f.payload...)
		}
		if channel != channelStatus || !f.fin {
			continue
		}
		code, message, ok := exitCodeFromStatus(status)
		if !ok {
			return out.String(), 0, &ExecFailedError{Message: message}
		}
		return out.String(), int(code), nil
	}
}
