// SPDX-License-Identifier: AGPL-3.0-only

package consoleclient

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/tmc/go-iroh/iroh"
	"github.com/tmc/go-iroh/key"
	"github.com/tmc/go-iroh/netaddr"
	"github.com/tmc/go-iroh/relay"

	"go.datum.net/compute/internal/consolesession"
)

// Connection is where a ready session is served, as published in its
// status.connection.
type Connection struct {
	EndpointID string
	RelayURLs  []string
	Target     string
}

// Dial opens a tunnel to the session's endpoint through its relays, using key
// as the client's identity, and returns a connection that reaches the agent at
// c.Target. Closing it tears the tunnel down.
func Dial(ctx context.Context, clientKey ed25519.PrivateKey, c Connection) (net.Conn, error) {
	if len(c.RelayURLs) == 0 {
		return nil, errors.New("session names no relays")
	}
	sk, err := key.SecretKeyFromEd25519(clientKey)
	if err != nil {
		return nil, err
	}
	id, err := key.ParseEndpointID(c.EndpointID)
	if err != nil {
		return nil, fmt.Errorf("session endpoint ID: %w", err)
	}
	addr := netaddr.NewEndpointAddr(id)
	relays := make([]netaddr.RelayURL, 0, len(c.RelayURLs))
	for _, raw := range c.RelayURLs {
		u, err := netaddr.ParseRelayURL(raw)
		if err != nil {
			return nil, fmt.Errorf("session relay URL %q: %w", raw, err)
		}
		relays = append(relays, u)
		addr = addr.WithRelayURL(u)
	}

	ep, err := iroh.Bind(ctx,
		iroh.WithSecretKey(sk),
		iroh.WithRelayMode(relay.ModeCustom(relayMap(relays))),
		iroh.WithoutIPTransports(),
	)
	if err != nil {
		return nil, fmt.Errorf("start tunnel: %w", err)
	}
	conn, err := ep.Connect(ctx, addr, consolesession.ALPN)
	if err != nil {
		_ = ep.Shutdown(context.Background())
		return nil, fmt.Errorf("reach the session's endpoint: %w", err)
	}
	stream, err := conn.OpenStreamConn(ctx)
	if err != nil {
		_ = conn.CloseWithError(0, "")
		_ = ep.Shutdown(context.Background())
		return nil, fmt.Errorf("open tunnel stream: %w", err)
	}
	proxied, err := connectThrough(stream, c.Target)
	if err != nil {
		_ = stream.Close()
		_ = conn.CloseWithError(0, "")
		_ = ep.Shutdown(context.Background())
		return nil, err
	}
	return &tunnelConn{Conn: proxied, teardown: func() {
		_ = conn.CloseWithError(0, "")
		_ = ep.Shutdown(context.Background())
	}}, nil
}

func connectThrough(raw net.Conn, target string) (net.Conn, error) {
	if _, err := fmt.Fprintf(raw, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		return nil, fmt.Errorf("send CONNECT: %w", err)
	}
	br := bufio.NewReader(raw)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("endpoint refused the tunnel: %s: %s", resp.Status, body)
	}
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: raw, r: br}, nil
	}
	return raw, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

type tunnelConn struct {
	net.Conn
	teardown func()
	once     sync.Once
}

func (c *tunnelConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.teardown)
	return err
}

// Connect opens the tunnel and the session's stream. Closing the stream tears
// the tunnel down.
func Connect(ctx context.Context, clientKey ed25519.PrivateKey, sessionUID string, c Connection) (*Stream, error) {
	conn, err := Dial(ctx, clientKey, c)
	if err != nil {
		return nil, err
	}
	s, err := Start(ctx, conn, clientKey, sessionUID, c.Target)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return s, nil
}
