// SPDX-License-Identifier: AGPL-3.0-only

// Package shellagent is the cell side of instance shell sessions. An agent
// claims InstanceConsoleSessions delivered to its cell, serves each one's
// single connection through its paired tunnel endpoint, runs the session's
// command in the instance through the cell apiserver, and stops that command
// when the session ends for any reason.
//
// Every agent reads and writes only the session copies in its cell. Karmada
// reflects their status back to the hub, and the management plane copies it
// on to the project. The agent holds no hub credential.
package shellagent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

const managedByLabel = "managed-by"

// Config is an agent's configuration.
type Config struct {
	// Namespace holds the agent's Leases and its endpoint's key Secret.
	Namespace string
	// Ordinal pairs the agent with the tunnel endpoint of the same ordinal.
	Ordinal int
	// EndpointPodName is the paired tunnel endpoint's pod, restarted when its
	// key rotates.
	EndpointPodName string
	// Target is the host:port the paired endpoint proxies to this agent.
	Target string
	// RelayURLs are the relays the paired endpoint is reachable through,
	// nearest first.
	RelayURLs []string
	// ManagedBy lists the managed-by label values of pods that may take
	// sessions.
	ManagedBy []string
	// Cell names the cell the agent serves, on its metrics and logs. Empty
	// leaves the label empty.
	Cell string
	// Agent identifies this agent among the cell's, typically its pod name,
	// on its metrics and logs. Empty leaves the label empty.
	Agent string

	ConnectTimeout time.Duration
	// ClaimStagger is how long each claiming agent waits after the one
	// before it in a session's claim order. Zero lets every agent claim at
	// once.
	ClaimStagger      time.Duration
	SlotsPerInstance  int
	MaxOpenSessions   int
	DrainTimeout      time.Duration
	KillGrace         time.Duration
	KeyRotationPeriod time.Duration
	SweepInterval     time.Duration
	// PingInterval is how often the agent pings a connected client.
	PingInterval time.Duration
	// PongTimeout is how long a connected client may send nothing, not even a
	// pong, before the agent ends the session as Disconnected. It must be
	// longer than PingInterval.
	PongTimeout time.Duration
	// ExecTimeout bounds each command the agent runs in an instance to probe,
	// list or stop session processes, so a hung exec cannot hold a reconcile
	// worker or the sweep. Zero means KillGrace plus 30 seconds.
	ExecTimeout time.Duration
	// ExitPollInterval is how often the agent checks for a connected
	// command's exit code itself, in a container too bare for the exit watch
	// to wait in. Zero means two seconds.
	ExitPollInterval time.Duration
}

// DefaultConfig returns the contract's limits.
func DefaultConfig() Config {
	return Config{
		ManagedBy:         []string{"kata-provider"},
		ConnectTimeout:    60 * time.Second,
		ClaimStagger:      3 * time.Second,
		SlotsPerInstance:  3,
		MaxOpenSessions:   200,
		DrainTimeout:      30 * time.Second,
		KillGrace:         3 * time.Second,
		KeyRotationPeriod: 30 * 24 * time.Hour,
		SweepInterval:     time.Minute,
		PingInterval:      10 * time.Second,
		PongTimeout:       20 * time.Second,
	}
}

// Agent serves shell sessions in one cell.
type Agent struct {
	cfg Config

	sessions client.Reader
	cell     client.Client
	exec     Executor
	now      func() time.Time

	incarnation string
	identity    atomic.Pointer[identity]
	draining    atomic.Bool
	rotating    atomic.Bool

	mu   sync.Mutex
	live map[string]*liveSession
	open map[string]types.NamespacedName

	probes sync.Map
}

// New returns an agent. sessions reads session copies and Instances from the
// cell, typically through a cache; cell reaches the cell apiserver directly.
func New(cfg Config, sessions client.Reader, cell client.Client, exec Executor, incarnation string) (*Agent, error) {
	if cfg.Namespace == "" || cfg.Target == "" || len(cfg.RelayURLs) == 0 || len(cfg.ManagedBy) == 0 {
		return nil, errors.New("namespace, target, relay URLs and managed-by values are required")
	}
	if cfg.ExecTimeout <= 0 {
		cfg.ExecTimeout = cfg.KillGrace + 30*time.Second
	}
	if cfg.ExitPollInterval <= 0 {
		cfg.ExitPollInterval = 2 * time.Second
	}
	if cfg.PingInterval <= 0 || cfg.PongTimeout <= cfg.PingInterval {
		return nil, errors.New("the ping interval must be positive and shorter than the pong timeout")
	}
	return &Agent{
		cfg:         cfg,
		sessions:    sessions,
		cell:        cell,
		exec:        exec,
		now:         time.Now,
		incarnation: incarnation,
		live:        map[string]*liveSession{},
		open:        map[string]types.NamespacedName{},
	}, nil
}

// EndpointID is the paired tunnel endpoint's current iroh endpoint ID.
func (a *Agent) EndpointID() string {
	if id := a.identity.Load(); id != nil {
		return id.endpointID
	}
	return ""
}

func (a *Agent) claiming() bool {
	return !a.draining.Load() && !a.rotating.Load() && a.identity.Load() != nil
}

type liveSession struct {
	cancel context.CancelFunc
	done   chan struct{}
	stream *clientStream
	tty    bool
	reason string
}

// register records a connection. It returns false when the session already
// has one.
func (a *Agent) register(uid string, cancel context.CancelFunc, tty bool) (*liveSession, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.live[uid]; ok {
		return nil, false
	}
	l := &liveSession{cancel: cancel, done: make(chan struct{}), tty: tty}
	a.live[uid] = l
	return l, true
}

func (a *Agent) attach(l *liveSession, s *clientStream) {
	a.mu.Lock()
	l.stream = s
	a.mu.Unlock()
}

func (a *Agent) unregister(uid string) {
	a.mu.Lock()
	delete(a.live, uid)
	a.mu.Unlock()
}

func (a *Agent) isLive(uid string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.live[uid]
	return ok
}

func (a *Agent) stopReason(uid string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if l := a.live[uid]; l != nil {
		return l.reason
	}
	return ""
}

func (a *Agent) liveSessions() map[string]*liveSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]*liveSession, len(a.live))
	for uid, l := range a.live {
		out[uid] = l
	}
	return out
}

// stop ends a connected session for reason and waits until its processes
// have been stopped and its end recorded.
func (a *Agent) stop(uid, reason string) {
	a.mu.Lock()
	l := a.live[uid]
	if l != nil && l.reason == "" {
		l.reason = reason
	}
	a.mu.Unlock()
	if l == nil {
		return
	}
	l.cancel()
	select {
	case <-l.done:
	case <-time.After(a.cfg.KillGrace + 30*time.Second):
	}
}

// track counts a session claimed by this agent toward its open sessions.
func (a *Agent) track(uid string, key types.NamespacedName) {
	a.mu.Lock()
	a.open[uid] = key
	a.recordOpen()
	a.mu.Unlock()
}

func (a *Agent) forget(uid string) {
	a.mu.Lock()
	delete(a.open, uid)
	a.recordOpen()
	a.mu.Unlock()
}

func (a *Agent) openSessions() map[string]types.NamespacedName {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]types.NamespacedName, len(a.open))
	for uid, key := range a.open {
		out[uid] = key
	}
	return out
}

func (a *Agent) openCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.open)
}

func sessionUID(s *computev1alpha.InstanceConsoleSession) string {
	return s.Labels[computev1alpha.InstanceConsoleSessionUIDLabel]
}

func endpointOf(s *computev1alpha.InstanceConsoleSession) string {
	if s.Status.Connection == nil {
		return ""
	}
	return s.Status.Connection.EndpointID
}

func sessionTTL(s *computev1alpha.InstanceConsoleSession) time.Duration {
	if s.Spec.TTL == nil || s.Spec.TTL.Duration <= 0 {
		return 15 * time.Minute
	}
	return s.Spec.TTL.Duration
}
