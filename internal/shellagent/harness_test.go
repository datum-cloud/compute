// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6455 derives the accept key with SHA-1
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/consolesession"
)

const (
	testAgentNamespace = "compute-shell-system"
	testNamespace      = "ns-project-uid"
	testInstance       = "web-0"
	testInstanceUID    = types.UID("cell-instance-uid")
	testDeploymentUID  = "cell-wd-uid"
	testContainer      = "app"
	testSession        = "s1"
	testUID            = "uid-1"
)

// clock is a settable time source shared by the agents in a test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type harness struct {
	t     *testing.T
	ctx   context.Context
	cell  client.Client
	exec  *fakeAPIServer
	clock *clock
}

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(computev1alpha.AddToScheme(scheme))
	return scheme
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	scheme := newScheme()
	owner := metav1.OwnerReference{
		APIVersion: computev1alpha.GroupVersion.String(),
		Kind:       "Instance",
		Name:       testInstance,
		UID:        testInstanceUID,
		Controller: ptr.To(true),
	}
	instance := &computev1alpha.Instance{ObjectMeta: metav1.ObjectMeta{
		Name:      testInstance,
		Namespace: testNamespace,
		UID:       testInstanceUID,
		Labels:    map[string]string{computev1alpha.WorkloadDeploymentUIDLabel: testDeploymentUID},
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testInstance,
			Namespace:       testNamespace,
			UID:             "pod-uid",
			Labels:          map[string]string{managedByLabel: "kata-provider"},
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: testContainer}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:        testContainer,
				ContainerID: "containerd://abc",
				State:       corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
	session := &computev1alpha.InstanceConsoleSession{}
	return &harness{
		t:   t,
		ctx: ctrl.LoggerInto(context.Background(), ctrl.Log),
		cell: fake.NewClientBuilder().WithScheme(scheme).
			WithStatusSubresource(session).
			WithObjects(instance, pod).Build(),
		exec:  newFakeAPIServer(),
		clock: &clock{t: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)},
	}
}

// agent returns an agent whose endpoint ID derives from a fresh key and whose
// liveness Lease is current.
func (h *harness) agent(mutate ...func(*Config)) *Agent {
	h.t.Helper()
	cfg := DefaultConfig()
	cfg.Namespace = testAgentNamespace
	cfg.Target = "exec-agent-0.exec-agent.compute-shell-system.svc.cluster.local:7777"
	cfg.RelayURLs = []string{"https://relay.example.test"}
	cfg.KillGrace = 0
	for _, m := range mutate {
		m(&cfg)
	}
	a, err := New(cfg, h.cell, h.cell, h.exec, "test")
	if err != nil {
		h.t.Fatal(err)
	}
	a.now = h.clock.now
	seed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(seed)
	a.identity.Store(&identity{endpointID: EndpointID(seed), created: h.clock.now()})
	if err := a.renewLease(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	return a
}

// session creates a session's cell copy and returns the client's key.
func (h *harness) session(name, uid string, mutate ...func(*computev1alpha.InstanceConsoleSession)) ed25519.PrivateKey {
	h.t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	s := &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         testNamespace,
			CreationTimestamp: metav1.NewTime(h.clock.now()),
			Labels: map[string]string{
				computev1alpha.InstanceConsoleSessionUIDLabel:          uid,
				computev1alpha.InstanceConsoleSessionInstanceNameLabel: testInstance,
				computev1alpha.WorkloadDeploymentUIDLabel:              testDeploymentUID,
			},
		},
		Spec: computev1alpha.InstanceConsoleSessionSpec{
			InstanceRef:     computev1alpha.InstanceConsoleSessionInstanceRef{Name: testInstance, UID: "project-instance-uid"},
			ContainerName:   testContainer,
			Command:         []string{"sh"},
			Stdin:           true,
			Terminal:        true,
			ClientPublicKey: consolesession.PublicKey(key),
			TTL:             &metav1.Duration{Duration: 15 * time.Minute},
		},
	}
	for _, m := range mutate {
		m(s)
	}
	if err := h.cell.Create(h.ctx, s); err != nil {
		h.t.Fatal(err)
	}
	return key
}

func (h *harness) cellSession(name string) *computev1alpha.InstanceConsoleSession {
	h.t.Helper()
	var s computev1alpha.InstanceConsoleSession
	if err := h.cell.Get(h.ctx, client.ObjectKey{Namespace: testNamespace, Name: name}, &s); err != nil {
		h.t.Fatal(err)
	}
	return &s
}

func (h *harness) reconcile(a *Agent, name string) ctrl.Result {
	h.t.Helper()
	res, err := a.Reconcile(h.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: name}})
	if err != nil {
		h.t.Fatalf("reconcile %s: %v", name, err)
	}
	return res
}

func (h *harness) slots() []coordinationv1.Lease {
	h.t.Helper()
	var leases coordinationv1.LeaseList
	if err := h.cell.List(h.ctx, &leases, client.MatchingLabels{componentLabel: slotComponent}); err != nil {
		h.t.Fatal(err)
	}
	return leases.Items
}

func requireReason(t *testing.T, s *computev1alpha.InstanceConsoleSession, want string) {
	t.Helper()
	if got := readyReason(s); got != want {
		c := readyCondition(s)
		t.Fatalf("Ready reason = %q, want %q (condition %+v)", got, want, c)
	}
}

// fakeAPIServer plays the apiserver's pods/exec endpoint. Commands the agent
// runs to completion answer from scripts; session streams are handed to
// onSession.
type fakeAPIServer struct {
	mu        sync.Mutex
	commands  [][]string
	probeExit int
	probeDir  string
	markers   string
	killExit  int
	killHangs bool
	onSession func(conn net.Conn, r *bufio.Reader)
}

func newFakeAPIServer() *fakeAPIServer {
	return &fakeAPIServer{probeDir: "/tmp"}
}

func (f *fakeAPIServer) ran(script string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.commands {
		if len(c) > 2 && c[2] == script {
			n++
		}
	}
	return n
}

func (f *fakeAPIServer) Open(_ context.Context, _ types.NamespacedName, opts ExecOptions, handshake http.Header) (*http.Response, net.Conn, *bufio.Reader, error) {
	f.mu.Lock()
	f.commands = append(f.commands, opts.Command)
	onSession := f.onSession
	f.mu.Unlock()

	agentSide, serverSide := net.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Upgrade":                []string{"websocket"},
			"Connection":             []string{"Upgrade"},
			"Sec-Websocket-Protocol": []string{consolesession.SubProtocol},
			"Sec-Websocket-Accept":   []string{acceptKey(handshake.Get("Sec-WebSocket-Key"))},
		},
	}
	serverReader := bufio.NewReader(serverSide)
	go func() {
		defer func() { _ = serverSide.Close() }()
		if handshake != nil && onSession != nil {
			onSession(serverSide, serverReader)
			return
		}
		f.answer(serverSide, opts.Command)
	}()
	return resp, agentSide, bufio.NewReader(agentSide), nil
}

func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (f *fakeAPIServer) answer(conn net.Conn, command []string) {
	f.mu.Lock()
	probeExit, probeDir, markers, killExit, killHangs := f.probeExit, f.probeDir, f.markers, f.killExit, f.killHangs
	f.mu.Unlock()
	switch command[2] {
	case probeScript:
		if probeExit == 0 {
			writeServerFrame(conn, channelStdout, []byte(probeDir+"\n"))
		}
		writeExitStatus(conn, probeExit)
	case listMarkersScript:
		writeServerFrame(conn, channelStdout, []byte(markers))
		writeExitStatus(conn, 0)
	case killScript:
		if killHangs {
			_, _ = io.Copy(io.Discard, conn)
			return
		}
		writeExitStatus(conn, killExit)
	default:
		writeExitStatus(conn, 0)
	}
}

func writeServerFrame(conn net.Conn, channel byte, payload []byte) {
	_, _ = conn.Write(encodeFrame(opBinary, append([]byte{channel}, payload...), false))
}

func writeExitStatus(conn net.Conn, code int) {
	var status metav1.Status
	if code == 0 {
		status = metav1.Status{Status: metav1.StatusSuccess}
	} else {
		c := int32(code)
		status = closingStatus(computev1alpha.InstanceConsoleSessionReasonCompleted, &c, "")
	}
	body, _ := json.Marshal(status)
	writeServerFrame(conn, channelStatus, body)
	_, _ = conn.Write(encodeFrame(opClose, closePayload(closeNormal), false))
}

// wsClient is a minimal client side of the connection protocol.
type wsClient struct {
	conn   net.Conn
	reader *bufio.Reader
	status int
	body   string
}

func dialSession(t *testing.T, addr, uid string, key ed25519.PrivateKey, at time.Time) *wsClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+consolesession.ExecPath(uid), nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Protocol", consolesession.SubProtocol)
	if key != nil {
		ts, sig := consolesession.Sign(key, uid, at)
		req.Header.Set(consolesession.HeaderTimestamp, ts)
		req.Header.Set(consolesession.HeaderSignature, sig)
	}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		t.Fatal(err)
	}
	c := &wsClient{conn: conn, reader: reader, status: resp.StatusCode}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		var b strings.Builder
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		b.Write(buf[:n])
		c.body = b.String()
	}
	return c
}

// readUntilClose returns every data frame by channel, the status message and
// the close code.
func (c *wsClient) readUntilClose(t *testing.T) (map[byte]string, *metav1.Status, int) {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	out := map[byte]string{}
	var status *metav1.Status
	for {
		f, err := readFrame(c.reader)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if f.opcode == opClose {
			code := 0
			if len(f.payload) >= 2 {
				code = int(f.payload[0])<<8 | int(f.payload[1])
			}
			return out, status, code
		}
		if f.control() || len(f.payload) == 0 {
			continue
		}
		if f.payload[0] == channelStatus {
			if status != nil {
				t.Fatal("received more than one status message")
			}
			status = &metav1.Status{}
			if err := json.Unmarshal(f.payload[1:], status); err != nil {
				t.Fatal(err)
			}
			continue
		}
		out[f.payload[0]] += string(f.payload[1:])
	}
}

func (c *wsClient) send(t *testing.T, opcode byte, payload []byte) {
	t.Helper()
	if _, err := c.conn.Write(encodeFrame(opcode, payload, true)); err != nil {
		t.Fatal(err)
	}
}
