// SPDX-License-Identifier: AGPL-3.0-only

// Package endpointprobe reports whether a cell's tunnel endpoint can be
// reached the way a session's client reaches it: through the relays its agent
// publishes, and on to the agent's target. An endpoint that is not homed on
// one of those relays accepts no connection through them, so a session its
// agent claimed would time out at the client's dial.
package endpointprobe

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"go.datum.net/compute/internal/consoleclient"
)

// Config is a prober's configuration.
type Config struct {
	// KeyFile holds the endpoint's raw 32-byte identity key. It is read on
	// every probe, so a rotated key is followed.
	KeyFile string
	// RelayURLs are the relays the agent publishes for the endpoint.
	RelayURLs []string
	// Target is the host:port the endpoint proxies to its agent.
	Target string
	// Interval is how often the endpoint is probed.
	Interval time.Duration
	// Timeout bounds one probe.
	Timeout time.Duration
	// AllowInsecureRelays accepts http and ws relay URLs, for local testing
	// only.
	AllowInsecureRelays bool
}

// Dialer opens a tunnel the way a session's client does.
type Dialer func(ctx context.Context, clientKey ed25519.PrivateKey, c consoleclient.Connection) (net.Conn, error)

// Prober probes an endpoint and serves the result as a readiness check.
type Prober struct {
	cfg  Config
	dial Dialer
	now  func() time.Time

	mu        sync.Mutex
	succeeded time.Time
	lastErr   error
}

// New returns a prober that dials with consoleclient.Dial.
func New(cfg Config) (*Prober, error) {
	if cfg.KeyFile == "" || len(cfg.RelayURLs) == 0 || cfg.Target == "" {
		return nil, errors.New("key file, relay URLs and target are required")
	}
	if cfg.Interval <= 0 || cfg.Timeout <= 0 {
		return nil, errors.New("interval and timeout must be positive")
	}
	return &Prober{cfg: cfg, dial: consoleclient.Dial, now: time.Now, lastErr: errors.New("not probed yet")}, nil
}

// Run probes the endpoint every interval until ctx ends.
func (p *Prober) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()
	for {
		p.probe(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (p *Prober) probe(ctx context.Context) {
	err := p.reach(ctx)
	p.mu.Lock()
	changed := (err == nil) != (p.lastErr == nil)
	p.lastErr = err
	if err == nil {
		p.succeeded = p.now()
	}
	p.mu.Unlock()
	if changed && err != nil {
		log.FromContext(ctx).Info("endpoint is not reachable through its relays", "error", err.Error())
	} else if changed {
		log.FromContext(ctx).Info("endpoint is reachable through its relays")
	}
}

func (p *Prober) reach(ctx context.Context) error {
	seed, err := os.ReadFile(p.cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("read endpoint key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return fmt.Errorf("endpoint key holds %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	endpointID := hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	conn, err := p.dial(ctx, clientKey, consoleclient.Connection{
		EndpointID:          endpointID,
		RelayURLs:           p.cfg.RelayURLs,
		Target:              p.cfg.Target,
		AllowInsecureRelays: p.cfg.AllowInsecureRelays,
	})
	if err != nil {
		return err
	}
	return conn.Close()
}

// Ready reports nil once the latest probe reached the endpoint, and why not
// otherwise. A result older than three intervals means probing has stalled
// and is not trusted.
func (p *Prober) Ready() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastErr != nil {
		return p.lastErr
	}
	if age := p.now().Sub(p.succeeded); age > 3*p.cfg.Interval {
		return fmt.Errorf("the endpoint was last reached %s ago", age.Round(time.Second))
	}
	return nil
}

// ServeHTTP answers a readiness probe.
func (p *Prober) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if err := p.Ready(); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}
