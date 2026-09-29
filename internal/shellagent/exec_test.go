// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"go.datum.net/compute/internal/consolesession"
)

func TestAPIServerOpensAuthorizedExecStream(t *testing.T) {
	var got *http.Request
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Protocol: " + consolesession.SubProtocol + "\r\n\r\n"))
		writeServerFrame(conn, channelStdout, []byte("/tmp\n"))
		writeExitStatus(conn, 0)
	}))
	defer srv.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	exec, err := NewAPIServer(&rest.Config{
		Host:            srv.URL,
		BearerToken:     "agent-token",
		TLSClientConfig: rest.TLSClientConfig{CAData: caPEM},
	})
	if err != nil {
		t.Fatal(err)
	}

	pod := types.NamespacedName{Namespace: "ns-1", Name: "web-0"}
	out, code, err := run(t.Context(), exec, pod, "app", probeCommand("bash"))

	if err != nil || code != 0 || out != "/tmp\n" {
		t.Fatalf("run() = %q, %d, %v", out, code, err)
	}
	if got.URL.Path != "/api/v1/namespaces/ns-1/pods/web-0/exec" {
		t.Fatalf("path = %s", got.URL.Path)
	}
	q := got.URL.Query()
	if !reflect.DeepEqual(q["command"], probeCommand("bash")) || q.Get("container") != "app" ||
		q.Get("stdout") != "true" || q.Get("stderr") != "true" || q.Has("stdin") || q.Has("tty") {
		t.Fatalf("query = %v", q)
	}
	if got.Header.Get("Authorization") != "Bearer agent-token" ||
		got.Header.Get("Sec-WebSocket-Protocol") != consolesession.SubProtocol ||
		got.Header.Get("Sec-WebSocket-Key") == "" {
		t.Fatalf("headers = %v", got.Header)
	}
}

func TestWrappedCommandRecordsProcessID(t *testing.T) {
	got := wrappedCommand("/tmp", "uid-1", []string{"ls", "-l"})
	want := []string{"sh", "-c", wrapperScript, "sh", "/tmp/.datum-exec-uid-1", "ls", "-l"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrappedCommand() = %q, want %q", got, want)
	}
}
