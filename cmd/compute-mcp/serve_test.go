// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// File names the tests write the throwaway certificate and key under.
const (
	testCertFile = "tls.crt"
	testKeyFile  = "tls.key"
)

const (
	// serveTimeout bounds every wait on the server under test.
	serveTimeout = 10 * time.Second

	// rotationTimeout covers certwatcher's polling fallback, which re-reads the
	// files every ten seconds when a filesystem event is missed.
	rotationTimeout = 30 * time.Second
)

// writeSelfSignedCert writes a throwaway serving certificate for 127.0.0.1 into
// dir and returns the file pair plus the certificate, which is its own CA.
func writeSelfSignedCert(t *testing.T, dir string, serial int64) (tlsFiles, *x509.Certificate) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "compute-mcp"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling key: %v", err)
	}

	files := tlsFiles{
		certFile: filepath.Join(dir, testCertFile),
		keyFile:  filepath.Join(dir, testKeyFile),
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(files.certFile, certPEM, 0o600); err != nil {
		t.Fatalf("writing certificate: %v", err)
	}
	if err := os.WriteFile(files.keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("writing key: %v", err)
	}
	return files, cert
}

// startServer runs serve on a loopback port and returns its address, the
// channel serve's result arrives on, and the cancel that shuts it down.
func startServer(t *testing.T, files tlsFiles) (string, <-chan error, context.CancelFunc) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, "ok")
	})
	go func() { done <- serve(ctx, ln, handler, files) }()

	// Runs after the test body, so a test that already drained done is not
	// waited on twice: cancel is idempotent and the server is gone by then.
	t.Cleanup(cancel)
	return ln.Addr().String(), done, cancel
}

// clientTrusting returns a client that trusts only the given certificates and
// opens a fresh connection per request, so each request sees the certificate
// the server presents now.
func clientTrusting(certs ...*x509.Certificate) *http.Client {
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return &http.Client{
		Timeout: serveTimeout,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
		},
	}
}

func TestTLSFilesValidate(t *testing.T) {
	cases := []struct {
		name       string
		files      tlsFiles
		wantErr    string
		wantScheme string
	}{
		{name: "neither set serves http", files: tlsFiles{}, wantScheme: "http"},
		{
			name:       "both set serves https",
			files:      tlsFiles{certFile: testCertFile, keyFile: testKeyFile},
			wantScheme: "https",
		},
		{
			name:    "certificate without key",
			files:   tlsFiles{certFile: testCertFile},
			wantErr: "--tls-key-file",
		},
		{
			name:    "key without certificate",
			files:   tlsFiles{keyFile: testKeyFile},
			wantErr: "--tls-cert-file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.files.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validate: %v", err)
				}
				if got := tc.files.scheme(); got != tc.wantScheme {
					t.Errorf("scheme = %q, want %q", got, tc.wantScheme)
				}
				return
			}
			if err == nil {
				t.Fatal("validate accepted a half-configured certificate")
			}
			// The error must name the flag that is missing, so the operator
			// does not have to guess which half they left out.
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not name the missing %s", err, tc.wantErr)
			}
		})
	}
}

// TestServeRejectsHalfConfiguredTLS guards the fallback: a server given only
// one file must not quietly serve plain http.
func TestServeRejectsHalfConfiguredTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer func() { _ = ln.Close() }()

	err = serve(context.Background(), ln, http.NotFoundHandler(), tlsFiles{certFile: testCertFile})
	if err == nil {
		t.Fatal("serve started with a certificate and no key")
	}
}

func TestServeWithoutCertificateServesPlainHTTP(t *testing.T) {
	addr, _, _ := startServer(t, tlsFiles{})

	resp, err := (&http.Client{Timeout: serveTimeout}).Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET over http: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServeWithCertificateServesTLS(t *testing.T) {
	files, cert := writeSelfSignedCert(t, t.TempDir(), 1)
	addr, _, _ := startServer(t, files)

	resp, err := clientTrusting(cert).Get("https://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET over https: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Errorf("connection state = %+v, want TLS 1.2 or newer", resp.TLS)
	}

	// A plain http request must not be answered as if it were fine: the caller
	// would be sending a bearer token in the clear.
	plain, err := (&http.Client{Timeout: serveTimeout}).Get("http://" + addr + "/healthz")
	if err == nil {
		defer func() { _ = plain.Body.Close() }()
		if plain.StatusCode == http.StatusOK {
			t.Error("TLS server answered a plain http request with 200")
		}
	}
}

// TestServePicksUpARotatedCertificate is why the server watches the files: the
// cert-manager CSI driver rewrites them in place on renewal, and a server that
// read them once would serve an expired certificate until its next restart.
func TestServePicksUpARotatedCertificate(t *testing.T) {
	dir := t.TempDir()
	files, first := writeSelfSignedCert(t, dir, 1)
	addr, _, _ := startServer(t, files)

	if _, err := servedSerial(addr, first); err != nil {
		t.Fatalf("before rotation: %v", err)
	}

	_, second := writeSelfSignedCert(t, dir, 2)

	deadline := time.Now().Add(rotationTimeout)
	for {
		serial, err := servedSerial(addr, first, second)
		if err == nil && serial.Cmp(second.SerialNumber) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server still presents the old certificate (serial %v, err %v)", serial, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// servedSerial returns the serial of the certificate the server presents.
func servedSerial(addr string, trusted ...*x509.Certificate) (*big.Int, error) {
	resp, err := clientTrusting(trusted...).Get("https://" + addr + "/healthz")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.TLS.PeerCertificates[0].SerialNumber, nil
}

func TestServeFailsOnUnreadableCertificate(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer func() { _ = ln.Close() }()

	missing := filepath.Join(t.TempDir(), "absent")
	err = serve(context.Background(), ln, http.NotFoundHandler(),
		tlsFiles{certFile: missing + ".crt", keyFile: missing + ".key"})
	if err == nil {
		t.Fatal("serve started without a readable certificate")
	}
}

func TestServeStopsWhenContextIsCancelled(t *testing.T) {
	for _, tc := range []struct {
		name string
		tls  bool
	}{{name: "http"}, {name: "https", tls: true}} {
		t.Run(tc.name, func(t *testing.T) {
			files := tlsFiles{}
			if tc.tls {
				files, _ = writeSelfSignedCert(t, t.TempDir(), 1)
			}
			_, done, cancel := startServer(t, files)

			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("serve returned %v on a clean shutdown, want nil", err)
				}
			case <-time.After(serveTimeout):
				t.Fatal("serve did not return after its context was cancelled")
			}
		})
	}
}
