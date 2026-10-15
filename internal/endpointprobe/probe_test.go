// SPDX-License-Identifier: AGPL-3.0-only

package endpointprobe

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func startRelay(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(relayserver.New())
	t.Cleanup(srv.Close)
	return srv.URL
}

func startAgent(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().String()
}

// startEndpoint stands in for datum-connect serve: an endpoint homed on
// homeRelay that proxies CONNECT to target. It returns the key file the
// endpoint's pod mounts.
func startEndpoint(ctx context.Context, t *testing.T, homeRelay, target string) string {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	sk, err := key.SecretKeyFromEd25519(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	relayURL, err := netaddr.ParseRelayURL(homeRelay)
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
		for {
			conn, err := ep.Accept(ctx)
			if err != nil {
				return
			}
			go func() {
				stream, err := conn.AcceptStreamConn(ctx)
				if err != nil {
					return
				}
				defer func() { _ = stream.Close() }()
				req, err := http.ReadRequest(bufio.NewReader(stream))
				if err != nil || req.Method != http.MethodConnect || req.Host != target {
					_, _ = io.WriteString(stream, "HTTP/1.1 403 Forbidden\r\n\r\n")
					return
				}
				upstream, err := net.Dial("tcp", target)
				if err != nil {
					_, _ = io.WriteString(stream, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				_ = upstream.Close()
				_, _ = io.WriteString(stream, "HTTP/1.1 200 OK\r\n\r\n")
			}()
		}
	}()

	keyFile := filepath.Join(t.TempDir(), "0")
	if err := os.WriteFile(keyFile, seed, 0o400); err != nil {
		t.Fatal(err)
	}
	return keyFile
}

func newProber(t *testing.T, keyFile string, relays []string, target string) *Prober {
	t.Helper()
	p, err := New(Config{
		KeyFile:             keyFile,
		RelayURLs:           relays,
		Target:              target,
		Interval:            time.Second,
		Timeout:             3 * time.Second,
		AllowInsecureRelays: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func readiness(p *Prober) int {
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return rec.Code
}

func TestReadyWhenHomedOnAPublishedRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	published := startRelay(t)
	target := startAgent(t)
	keyFile := startEndpoint(ctx, t, published, target)

	p := newProber(t, keyFile, []string{published}, target)
	if readiness(p) != http.StatusServiceUnavailable {
		t.Fatal("ready before the first probe")
	}
	p.probe(ctx)
	if err := p.Ready(); err != nil {
		t.Fatalf("Ready() = %v, want the endpoint reachable through its published relay", err)
	}
	if code := readiness(p); code != http.StatusOK {
		t.Fatalf("readiness = %d, want 200", code)
	}
}

func TestNotReadyWhenHomedOffThePublishedRelays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	published, elsewhere := startRelay(t), startRelay(t)
	target := startAgent(t)
	keyFile := startEndpoint(ctx, t, elsewhere, target)

	p := newProber(t, keyFile, []string{published}, target)
	p.probe(ctx)
	if p.Ready() == nil {
		t.Fatal("ready although no client can reach the endpoint through the relays its agent publishes")
	}
	if code := readiness(p); code != http.StatusServiceUnavailable {
		t.Fatalf("readiness = %d, want 503", code)
	}
}

func TestNotReadyWithoutAKey(t *testing.T) {
	p := newProber(t, filepath.Join(t.TempDir(), "missing"), []string{"http://127.0.0.1:1"}, "agent:7777")
	p.probe(context.Background())
	if err := p.Ready(); err == nil || !strings.Contains(err.Error(), "read endpoint key") {
		t.Fatalf("Ready() = %v, want the missing key reported", err)
	}
}

func TestStaleSuccessIsNotTrusted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	published := startRelay(t)
	target := startAgent(t)
	keyFile := startEndpoint(ctx, t, published, target)
	p := newProber(t, keyFile, []string{published}, target)
	p.probe(ctx)
	if err := p.Ready(); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Add(4 * time.Second)
	p.now = func() time.Time { return now }
	if p.Ready() == nil {
		t.Fatal("a success older than three intervals was trusted")
	}
}
