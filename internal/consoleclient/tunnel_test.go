// SPDX-License-Identifier: AGPL-3.0-only

package consoleclient

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tmc/go-iroh/iroh"
	"github.com/tmc/go-iroh/key"
	"github.com/tmc/go-iroh/netaddr"
	"github.com/tmc/go-iroh/relay"
	"github.com/tmc/go-iroh/relayserver"

	"go.datum.net/compute/internal/consolesession"
)

func TestConnectThroughRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	clientKey := newKey(t)
	agent := &fakeAgent{
		t:         t,
		publicKey: consolesession.PublicKey(clientKey),
		ending:    `{"status":"Success"}`,
		output:    "through the relay",
	}
	agentSrv := httptest.NewServer(agent)
	t.Cleanup(agentSrv.Close)
	target := strings.TrimPrefix(agentSrv.URL, "http://")

	relaySrv := httptest.NewServer(relayserver.New())
	t.Cleanup(relaySrv.Close)
	relayURL, err := netaddr.ParseRelayURL(relaySrv.URL)
	if err != nil {
		t.Fatal(err)
	}

	endpoint := startTunnelEndpoint(ctx, t, relayURL, target)

	s, err := Connect(ctx, clientKey, testSessionUID, Connection{
		EndpointID: endpoint.ID().String(),
		RelayURLs:  []string{relaySrv.URL},
		Target:     target,
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := s.CloseStdin(); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	got, err := s.Wait(&stdout, io.Discard)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if got != (Result{}) || stdout.String() != "through the relay" {
		t.Errorf("Wait() = %+v with output %q", got, stdout.String())
	}
}

// startTunnelEndpoint stands in for the cell's tunnel endpoint: it accepts
// CONNECT on a stream and proxies it to the one target it allows.
func startTunnelEndpoint(ctx context.Context, t *testing.T, relayURL netaddr.RelayURL, target string) *iroh.Endpoint {
	t.Helper()
	sk, err := key.GenerateSecretKey()
	if err != nil {
		t.Fatal(err)
	}
	ep, err := iroh.Bind(ctx,
		iroh.WithSecretKey(sk),
		iroh.WithALPNs(consolesession.ALPN),
		iroh.WithRelayMode(relay.ModeCustom(relay.MapFromURLs(relayURL))),
		iroh.WithoutIPTransports(),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ep.Shutdown(context.Background()) })
	if err := ep.Online(ctx); err != nil {
		t.Fatal(err)
	}

	go func() {
		conn, err := ep.Accept(ctx)
		if err != nil {
			return
		}
		stream, err := conn.AcceptStreamConn(ctx)
		if err != nil {
			return
		}
		defer func() { _ = stream.Close() }()
		br := bufio.NewReader(stream)
		req, err := http.ReadRequest(br)
		if err != nil || req.Method != http.MethodConnect || req.Host != target {
			_, _ = io.WriteString(stream, "HTTP/1.1 403 Forbidden\r\n\r\n")
			return
		}
		upstream, err := net.Dial("tcp", target)
		if err != nil {
			_, _ = io.WriteString(stream, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			return
		}
		defer func() { _ = upstream.Close() }()
		_, _ = io.WriteString(stream, "HTTP/1.1 200 OK\r\n\r\n")
		go func() { _, _ = io.Copy(upstream, br) }()
		_, _ = io.Copy(stream, upstream)
	}()
	return ep
}
